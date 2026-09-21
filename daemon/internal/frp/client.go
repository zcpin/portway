package frp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	frpclient "github.com/fatedier/frp/client"
	"github.com/fatedier/frp/client/proxy"
	frpconfig "github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	frplog "github.com/fatedier/frp/pkg/util/log"
	"github.com/fatedier/frp/pkg/util/xlog"
	glog "github.com/fatedier/golib/log"

	"github.com/byteporter/portway/internal/logger"
)

// stopTimeout 是停止一个客户端时的等待上限。
const stopTimeout = 5 * time.Second

// frpLogSink 把 frp 的日志送进本项目的日志系统。
//
// 实现 golib 的 Writer 接口而不是裸的 io.Writer：这样能直接拿到日志级别，
// 不必去解析格式化后的文本行。
type frpLogSink struct{}

func (s frpLogSink) Write(p []byte) (int, error) {
	return s.WriteLog(p, glog.InfoLevel, time.Now())
}

func (s frpLogSink) WriteLog(line []byte, level glog.Level, _ time.Time) (int, error) {
	message := strings.TrimRight(string(line), "\r\n")
	if message == "" {
		return len(line), nil
	}

	switch level {
	case glog.TraceLevel, glog.DebugLevel:
		logger.Debug("%s", message)
	case glog.WarnLevel:
		logger.Warn("%s", message)
	case glog.ErrorLevel:
		logger.Error("%s", message)
	default:
		logger.Info("%s", message)
	}
	return len(line), nil
}

// setupLogger 把 frp 的全局日志出口接到本项目的日志上。
//
// frp 的日志出口是包级的（多个客户端共用一个），级别统一设为 debug：
// 是否输出由本项目的日志级别决定，这样「设置 → 日志级别」能一并管住 frp 的日志。
func setupLogger() {
	frplog.Logger = frplog.Logger.WithOptions(
		glog.WithOutput(frpLogSink{}),
		glog.WithLevel(glog.DebugLevel),
	)
}

// Instance 是一个运行中的 frpc 客户端。
//
// 每次启动都重新解析配置文件并新建 frp 服务：frp 的 Service 关闭后不能复用，
// 重建比维护"可重启"状态简单，代价只是读一次文件。
type Instance struct {
	name string
	path string

	mu        sync.Mutex
	svc       *frpclient.Service
	exporter  frpclient.StatusExporter
	proxyName []string
	cancel    context.CancelFunc
	done      chan struct{}
	startedAt time.Time
	lastError string
}

// buildService 解析配置并构建 frp 客户端服务（不发起连接）。
func buildService(path string) (*frpclient.Service, *v1.ClientCommonConfig, []string, error) {
	result, err := frpconfig.LoadClientConfigResult(path, false)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("读取 FRP 配置失败: %w", err)
	}
	if result.IsLegacyFormat {
		return nil, nil, nil, errors.New("不支持 frp 旧版 INI 格式的配置")
	}

	common := result.Common
	// 服务器暂时不可达时保持重试，而不是让实例直接退出：
	// 启停由用户在界面上控制，进程自己退掉会让状态难以解释。
	if common.LoginFailExit == nil {
		loginFailExit := false
		common.LoginFailExit = &loginFailExit
	}

	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(result.Proxies, result.Visitors); err != nil {
		return nil, nil, nil, fmt.Errorf("初始化配置源失败: %w", err)
	}
	aggregator := source.NewAggregator(configSource)

	proxies, visitors := frpconfig.FilterClientConfigurers(common, result.Proxies, result.Visitors)
	proxies = frpconfig.CompleteProxyConfigurers(proxies)
	visitors = frpconfig.CompleteVisitorConfigurers(visitors)
	if _, err := validation.ValidateAllClientConfig(common, proxies, visitors, nil); err != nil {
		return nil, nil, nil, fmt.Errorf("FRP 配置校验失败: %w", err)
	}

	svc, err := frpclient.NewService(frpclient.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: aggregator,
		ConfigFilePath:         path,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// 状态查询要按名字逐个取，这里记下配置里的代理名（含被禁用的，
	// 界面需要把它们显示为"已停用"而不是消失）。
	names := make([]string, 0, len(result.Proxies)+len(result.Visitors))
	for _, p := range result.Proxies {
		names = append(names, p.GetBaseConfig().Name)
	}
	for _, v := range result.Visitors {
		names = append(names, v.GetBaseConfig().Name)
	}
	return svc, common, names, nil
}

// newInstance 创建一个未启动的实例。
func newInstance(name, path string) *Instance {
	return &Instance{name: name, path: path}
}

// Name 返回客户端名。
func (i *Instance) Name() string { return i.name }

// Start 启动客户端。连接与重试都在后台进行，本方法立即返回。
func (i *Instance) Start() error {
	i.mu.Lock()
	if i.cancel != nil {
		i.mu.Unlock()
		return fmt.Errorf("FRP 客户端 %s 已在运行", i.name)
	}

	// 每次启动都重新解析配置：这样配置文件被外部改过也能生效，
	// 而且上面的锁只保护状态，不包含解析与建连。
	service, _, names, err := buildService(i.path)
	if err != nil {
		i.mu.Unlock()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	// frp 的 Service 会沿用 ctx 里已有的 logger，因此每个实例的日志都带上客户端名，
	// 多个客户端跑在同一个进程里也能分清来源。
	ctx = xlog.NewContext(ctx, xlog.New().AddPrefix(xlog.LogPrefix{
		Name:     "client",
		Value:    i.name,
		Priority: 100,
	}))

	done := make(chan struct{})
	i.svc = service
	i.exporter = service.StatusExporter()
	i.proxyName = names
	i.cancel = cancel
	i.done = done
	i.startedAt = time.Now()
	i.lastError = ""
	i.mu.Unlock()

	go func() {
		defer close(done)
		// Run 会一直阻塞到 Close，返回值只在出错时才有意义。
		if err := service.Run(ctx); err != nil && ctx.Err() == nil {
			i.setError(err)
		}
		i.markExited(done, cancel)
	}()

	return nil
}

// Stop 停止客户端。未运行时是空操作。
func (i *Instance) Stop() {
	i.mu.Lock()
	if i.cancel == nil {
		i.mu.Unlock()
		return
	}
	cancel, done, svc := i.cancel, i.done, i.svc
	i.mu.Unlock()

	// 先让 frp 关掉连接，再取消 ctx；反过来会让 Run 里的清理逻辑看到已取消的上下文。
	if svc != nil {
		svc.GracefulClose(500 * time.Millisecond)
	}
	cancel()

	if done != nil {
		select {
		case <-done:
		case <-time.After(stopTimeout):
			logger.Warn("[%s] 等待 FRP 客户端退出超时", i.name)
		}
	}

	i.mu.Lock()
	if i.done == done {
		i.cancel, i.done, i.svc, i.exporter = nil, nil, nil, nil
	}
	i.mu.Unlock()
}

// markExited 在服务自行退出（例如配置错误）时把实例状态收回"未运行"。
//
// 同时释放 ctx：服务是自己退出的，没有人会再调 Stop，不在这里取消就会漏掉一个
// 保持存活的后台 context。
func (i *Instance) markExited(done chan struct{}, cancel context.CancelFunc) {
	i.mu.Lock()
	exited := i.done == done
	if exited {
		i.cancel, i.done, i.svc, i.exporter = nil, nil, nil, nil
	}
	i.mu.Unlock()

	if exited && cancel != nil {
		cancel()
	}
}

// Running 报告客户端是否在运行。
func (i *Instance) Running() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.cancel != nil
}

// StartedAt 返回本次启动时间。
func (i *Instance) StartedAt() time.Time {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.startedAt
}

// LastError 返回最近一次运行错误。
func (i *Instance) LastError() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastError
}

func (i *Instance) setError(err error) {
	if err == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.lastError = err.Error()
}

// Reload 让运行中的实例重新读取配置文件。
//
// 走 frp 的热更新通道：与服务器的连接保持不变，代理的增删改与启用状态
// 在几秒内生效，不需要重连。未运行时什么都不做——下次启动自然会读到新配置。
func (i *Instance) Reload() error {
	i.mu.Lock()
	svc := i.svc
	running := i.cancel != nil
	i.mu.Unlock()

	if !running || svc == nil {
		return nil
	}

	result, err := frpconfig.LoadClientConfigResult(i.path, false)
	if err != nil {
		return fmt.Errorf("读取 FRP 配置失败: %w", err)
	}
	if err := svc.UpdateConfigSource(result.Common, result.Proxies, result.Visitors); err != nil {
		return fmt.Errorf("热更新失败: %w", err)
	}

	names := make([]string, 0, len(result.Proxies)+len(result.Visitors))
	for _, p := range result.Proxies {
		names = append(names, p.GetBaseConfig().Name)
	}
	for _, v := range result.Visitors {
		names = append(names, v.GetBaseConfig().Name)
	}

	i.mu.Lock()
	i.proxyName = names
	i.mu.Unlock()
	return nil
}

// ProxyStatuses 返回每条代理的实时状态，键为代理名。
func (i *Instance) ProxyStatuses() map[string]proxy.WorkingStatus {
	i.mu.Lock()
	exporter, names := i.exporter, i.proxyName
	i.mu.Unlock()

	if exporter == nil {
		return nil
	}

	result := make(map[string]proxy.WorkingStatus, len(names))
	for _, name := range names {
		if status, ok := exporter.GetProxyStatus(name); ok {
			result[name] = *status
		}
	}
	return result
}
