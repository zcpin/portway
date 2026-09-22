// Package daemon 封装守护进程的完整生命周期：加载配置、启动 API 服务、
// 写服务发现文件、维持隧道、处理退出。
//
// 前台运行与系统服务两种模式共用这里的实现，差别只在于退出信号的来源
// （终端信号 vs 服务管理器的 Stop 调用）以及服务发现文件的写入位置。
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/byteporter/portway/internal/app"
	"github.com/byteporter/portway/internal/discovery"
	"github.com/byteporter/portway/internal/logger"
	"github.com/byteporter/portway/internal/server"
	"github.com/byteporter/portway/internal/update"
)

// Options 是启动守护进程所需的全部参数。
type Options struct {
	ConfigPath  string
	Addr        string
	NoAuth      bool
	LogLevel    string
	Version     string
	ServiceMode bool // 由系统服务管理器拉起
	HideConsole bool // 隐藏控制台窗口（Windows 开机自启用）
}

// Daemon 是一个可启动、可停止的守护进程实例。
type Daemon struct {
	opts         Options
	app          *app.App
	srv          *server.Server
	quit         chan struct{}
	infoPath     string
	shutdownOnce sync.Once
}

// Run 阻塞运行守护进程，直到收到退出信号或 Shutdown 被调用。
func Run(opts Options) error {
	d, err := New(opts)
	if err != nil {
		return err
	}
	return d.Run()
}

// New 完成所有不需要阻塞的初始化：配置、API 服务监听、服务发现文件。
func New(opts Options) (*Daemon, error) {
	if opts.HideConsole {
		hideConsole()
	}

	server.Version = opts.Version

	cfgPath, err := ResolveConfigPath(opts.ConfigPath)
	if err != nil {
		return nil, err
	}

	if opts.LogLevel != "" {
		if err := logger.InitGlobalLogger(opts.LogLevel, logger.IsTerminal(os.Stdout)); err != nil {
			return nil, err
		}
	}

	application, err := app.New(cfgPath)
	if err != nil {
		return nil, err
	}

	hub := server.NewHub()
	application.SetEmitter(hub)

	token := ""
	if !opts.NoAuth {
		if token, err = generateToken(); err != nil {
			return nil, err
		}
	}

	srv := server.New(application, hub, opts.Addr, token)
	if err := srv.Listen(); err != nil {
		return nil, err
	}

	host, portStr, err := net.SplitHostPort(srv.Addr().String())
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}

	d := &Daemon{
		opts: opts,
		app:  application,
		srv:  srv,
		quit: make(chan struct{}),
	}
	if !opts.ServiceMode && !opts.NoAuth {
		srv.SetUpdateShutdown(d.Shutdown)
	}

	// 把连接信息写给客户端做服务发现，进程退出时清理
	executable, _ := os.Executable()
	infoPath, err := discovery.Write(discovery.Info{
		Host:           host,
		Port:           port,
		Token:          token,
		PID:            os.Getpid(),
		Version:        opts.Version,
		ConfigPath:     cfgPath,
		ExecutablePath: executable,
		ServiceMode:    &opts.ServiceMode,
	}, opts.ServiceMode)
	if err != nil {
		logger.Warn("写入服务发现文件失败，客户端需手动配置连接信息: %v", err)
	} else {
		d.infoPath = infoPath
		logger.Info("服务发现文件: %s", infoPath)
	}

	return d, nil
}

// Run 启动隧道与 API 服务，并阻塞等待退出。
func (d *Daemon) Run() error {
	defer discovery.Remove(d.infoPath, os.Getpid())
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	unlock, err := update.LockRunningExecutable(executable)
	if err != nil {
		_ = d.srv.Shutdown(context.Background())
		return err
	}
	defer unlock()

	if err := d.app.Start(); err != nil {
		logger.Error("启动隧道失败: %v", err)
	}
	monitorCtx, cancelMonitor := context.WithCancel(context.Background())
	monitorDone := make(chan struct{})
	go func() { defer close(monitorDone); d.app.WatchNetwork(monitorCtx) }()
	stopMonitor := func() { cancelMonitor(); <-monitorDone }
	defer stopMonitor()

	// 系统服务模式由服务管理器发送停止指令，不接管终端信号
	if !d.opts.ServiceMode {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigCh
			logger.Info("收到退出信号，正在停止...")
			d.Shutdown()
		}()
	}

	errCh := make(chan error, 1)
	go func() { errCh <- d.srv.Serve() }()

	select {
	case err := <-errCh:
		stopMonitor()
		if err != nil {
			logger.Error("API 服务异常退出: %v", err)
		}
		d.app.Stop()
		return err
	case <-d.quit:
		stopMonitor()
		logger.Info("正在停止守护进程...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.srv.Shutdown(ctx)
		d.app.Stop()
		return nil
	}
}

// Shutdown 请求守护进程停止，可被服务管理器从其他 goroutine 调用。
func (d *Daemon) Shutdown() {
	d.shutdownOnce.Do(func() { close(d.quit) })
}

// generateToken 生成 32 字节随机令牌。
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ResolveConfigPath 确定配置文件路径，不存在时创建一份最小可用配置。
//
// 系统服务的工作目录是系统目录，因此这里始终把路径转成绝对路径，
// 避免相对路径在服务语境下指向意外位置。安装服务时需用它把路径固化下来。
func ResolveConfigPath(input string) (string, error) {
	path := strings.TrimSpace(input)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		fallback := filepath.Join(home, ".portway", "config.toml")

		for _, candidate := range []string{"portway.toml", "config.toml", fallback} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
		if path == "" {
			path = fallback
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0700); err != nil {
		return "", err
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		if err := os.WriteFile(absPath, []byte("log_level = \"info\"\n"), 0600); err != nil {
			return "", err
		}
	}

	return absPath, nil
}
