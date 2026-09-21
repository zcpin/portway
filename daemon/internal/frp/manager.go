package frp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "github.com/fatedier/frp/pkg/config/v1"

	"github.com/byteporter/portway/internal/logger"
)

// Manager 管理一组 FRP 客户端。
//
// 客户端的存在与否由磁盘上的配置文件决定，这里只额外记录"哪些正在运行"，
// 因此界面看到的列表始终与磁盘一致，外部编辑过的配置文件也会反映出来。
type Manager struct {
	store *Store

	mu      sync.RWMutex
	running map[string]*Instance
	started bool

	// updateMu 串行化配置变更与启停：它们都要读写配置文件并决定是否重启实例，
	// 并发执行会出现"先写后停"这种互相覆盖的顺序问题。
	updateMu sync.Mutex
}

// NewManager 打开配置目录并接管 frp 的日志出口。
func NewManager(dir string) (*Manager, error) {
	store, err := NewStore(dir)
	if err != nil {
		return nil, err
	}
	setupLogger()
	return &Manager{store: store, running: make(map[string]*Instance)}, nil
}

// Dir 返回配置目录，供界面显示。
func (m *Manager) Dir() string { return m.store.Dir() }

// Start 启动所有标记为自动启动的客户端。
//
// 单个客户端失败不影响其余客户端：FRP 配置写错是很常见的情况，
// 不应该让它连带把别的客户端也拦住。
func (m *Manager) Start() error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	names, err := m.store.List()
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.started = true
	m.mu.Unlock()

	var errs []error
	started := 0
	for _, name := range names {
		cfg, err := m.store.Load(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("客户端 %s 配置无效: %w", name, err))
			continue
		}
		if !clientAutoStart(&cfg.ClientCommonConfig) {
			continue
		}
		if err := m.startClient(name); err != nil {
			errs = append(errs, err)
			continue
		}
		started++
	}

	if started > 0 {
		logger.Info("已启动 %d 个 FRP 客户端", started)
	}
	return errors.Join(errs...)
}

// Stop 停止所有客户端。
func (m *Manager) Stop() {
	m.mu.Lock()
	instances := make([]*Instance, 0, len(m.running))
	for _, instance := range m.running {
		instances = append(instances, instance)
	}
	m.running = make(map[string]*Instance)
	m.started = false
	m.mu.Unlock()

	for _, instance := range instances {
		instance.Stop()
	}
	if len(instances) > 0 {
		logger.Info("已停止全部 FRP 客户端")
	}
}

// Running 报告管理器是否已启动（用于判断配置变更后该不该自动拉起客户端）。
func (m *Manager) Running() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.started
}

// List 返回全部客户端的配置与运行状态。
func (m *Manager) List() []ClientInfo {
	names, err := m.store.List()
	if err != nil {
		logger.Error("读取 FRP 客户端列表失败: %v", err)
		return nil
	}

	infos := make([]ClientInfo, 0, len(names))
	for _, name := range names {
		info, err := m.Info(name)
		if err != nil {
			// 配置文件坏掉时也要列出来：否则用户在界面上既看不到它，
			// 也没法删掉它，只能去手工翻目录。
			infos = append(infos, ClientInfo{Name: name, LastError: err.Error()})
			continue
		}
		infos = append(infos, info)
	}
	return infos
}

// Info 返回单个客户端的配置与运行状态。
func (m *Manager) Info(name string) (ClientInfo, error) {
	cfg, err := m.store.Load(name)
	if err != nil {
		return ClientInfo{}, err
	}

	info := clientInfo(name, cfg)
	instance := m.instance(name)
	if instance == nil {
		return info, nil
	}

	info.Running = true
	if startedAt := instance.StartedAt(); !startedAt.IsZero() {
		info.StartedAt = startedAt.Format(time.RFC3339)
	}
	info.LastError = instance.LastError()

	statuses := instance.ProxyStatuses()
	for i := range info.Proxies {
		status, ok := statuses[info.Proxies[i].Name]
		if !ok {
			continue
		}
		info.Proxies[i].Phase = status.Phase
		info.Proxies[i].RemoteAddr = remoteAddr(info, status.RemoteAddr)
		info.Proxies[i].LastError = status.Err
	}
	return info, nil
}

// remoteAddr 把 frp 返回的监听地址补全。
// frp 对 tcp / udp 代理只返回 ":13306" 这样的端口，对界面来说不知道连哪里没有意义。
func remoteAddr(info ClientInfo, addr string) string {
	if addr == "" || !strings.HasPrefix(addr, ":") {
		return addr
	}
	return info.ServerAddr + addr
}

// ---------- 客户端增删改与启停 ----------

// Add 新建一个客户端。
func (m *Manager) Add(payload ClientPayload) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	name := strings.TrimSpace(payload.Name)
	if err := ValidateName(name); err != nil {
		return err
	}
	if m.store.Exists(name) {
		return fmt.Errorf("FRP 客户端 %s 已存在", name)
	}

	cfg := &v1.ClientConfig{}
	cfg.Auth.Method = AuthToken
	applyClientPayload(cfg, payload)

	if err := m.store.Save(name, cfg); err != nil {
		return err
	}
	logger.Info("新增 FRP 客户端 %s", name)
	return nil
}

// Update 更新客户端配置。
//
// 改动到影响连接本身的字段（服务器地址、认证、TLS）时需要重建与服务器的连接，
// 因此这里按"公共配置是否真的变了"来决定重启还是什么都不做：
// 只是点了一次保存不该把连接掐断。
func (m *Manager) Update(name string, payload ClientPayload) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	cfg, err := m.store.Load(name)
	if err != nil {
		return err
	}

	before, _ := json.Marshal(cfg.ClientCommonConfig)
	applyClientPayload(cfg, payload)
	after, _ := json.Marshal(cfg.ClientCommonConfig)

	if err := m.store.Save(name, cfg); err != nil {
		return err
	}

	if bytes.Equal(before, after) || m.instance(name) == nil {
		return nil
	}
	return m.restartClient(name)
}

// Delete 删除客户端，同时停止它的实例。
func (m *Manager) Delete(name string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	if !m.store.Exists(name) {
		return fmt.Errorf("FRP 客户端 %s 不存在", name)
	}
	m.stopClient(name)

	if err := m.store.Delete(name); err != nil {
		return err
	}
	logger.Info("删除 FRP 客户端 %s", name)
	return nil
}

// StartClient 启动指定客户端。
func (m *Manager) StartClient(name string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	if !m.store.Exists(name) {
		return fmt.Errorf("FRP 客户端 %s 不存在", name)
	}
	return m.startClient(name)
}

// StopClient 停止指定客户端。
func (m *Manager) StopClient(name string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	if !m.store.Exists(name) {
		return fmt.Errorf("FRP 客户端 %s 不存在", name)
	}
	m.stopClient(name)
	return nil
}

// RestartClient 重启指定客户端。
func (m *Manager) RestartClient(name string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	if !m.store.Exists(name) {
		return fmt.Errorf("FRP 客户端 %s 不存在", name)
	}
	return m.restartClient(name)
}

// ---------- 代理增删改 ----------

// AddProxy 在客户端下新增一条代理。
func (m *Manager) AddProxy(client string, payload ProxyPayload) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	if err := validateProxyPayload(payload); err != nil {
		return err
	}

	cfg, err := m.store.Load(client)
	if err != nil {
		return err
	}
	if proxyNameTaken(cfg, payload.Name, -1) {
		return fmt.Errorf("代理名称 %s 已被占用", payload.Name)
	}

	proxy, err := newProxy(payload)
	if err != nil {
		return err
	}
	if err := applyProxyPayload(proxy, payload); err != nil {
		return err
	}
	if payload.Enabled != nil && !*payload.Enabled {
		base := proxy.GetBaseConfig()
		setProxyEnabled(&cfg.ClientCommonConfig.Start, &base.Enabled, base.Name, false)
	}

	cfg.Proxies = append(cfg.Proxies, v1.TypedProxyConfig{
		Type:            payload.Type,
		ProxyConfigurer: proxy,
	})
	return m.commit(client, cfg)
}

// UpdateProxy 更新一条代理。改名同样通过这里完成。
func (m *Manager) UpdateProxy(client, proxyName string, payload ProxyPayload) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	if err := validateProxyPayload(payload); err != nil {
		return err
	}

	cfg, err := m.store.Load(client)
	if err != nil {
		return err
	}

	typed, index := findProxy(cfg, proxyName)
	if typed == nil {
		// 访问端目前只支持删除与启停，不支持改字段
		if _, visitorIndex := findVisitor(cfg, proxyName); visitorIndex >= 0 {
			return fmt.Errorf("暂不支持编辑访问端 %s", proxyName)
		}
		return fmt.Errorf("代理 %s 不存在", proxyName)
	}
	if typed.Type != payload.Type {
		return fmt.Errorf("暂不支持修改代理类型，请删除后重新创建")
	}
	if payload.Name != proxyName && proxyNameTaken(cfg, payload.Name, index) {
		return fmt.Errorf("代理名称 %s 已被占用", payload.Name)
	}

	enabled := proxyEnabled(cfg.ClientCommonConfig.Start, typed.GetBaseConfig().Enabled, proxyName)
	if err := applyProxyPayload(typed.ProxyConfigurer, payload); err != nil {
		return err
	}
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	base := typed.GetBaseConfig()
	setProxyEnabled(&cfg.ClientCommonConfig.Start, &base.Enabled, base.Name, enabled)

	return m.commit(client, cfg)
}

// DeleteProxy 删除一条代理。
func (m *Manager) DeleteProxy(client, proxyName string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	cfg, err := m.store.Load(client)
	if err != nil {
		return err
	}

	if _, index := findProxy(cfg, proxyName); index >= 0 {
		cfg.Proxies = append(cfg.Proxies[:index], cfg.Proxies[index+1:]...)
	} else if _, index := findVisitor(cfg, proxyName); index >= 0 {
		cfg.Visitors = append(cfg.Visitors[:index], cfg.Visitors[index+1:]...)
	} else {
		return fmt.Errorf("代理 %s 不存在", proxyName)
	}

	return m.commit(client, cfg)
}

// ToggleProxy 切换一条代理的启用状态。
func (m *Manager) ToggleProxy(client, proxyName string, enabled bool) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	cfg, err := m.store.Load(client)
	if err != nil {
		return err
	}

	if typed, _ := findProxy(cfg, proxyName); typed != nil {
		base := typed.GetBaseConfig()
		setProxyEnabled(&cfg.ClientCommonConfig.Start, &base.Enabled, base.Name, enabled)
	} else if visitor, _ := findVisitor(cfg, proxyName); visitor != nil {
		base := visitor.GetBaseConfig()
		setProxyEnabled(&cfg.ClientCommonConfig.Start, &base.Enabled, base.Name, enabled)
	} else {
		return fmt.Errorf("代理 %s 不存在", proxyName)
	}

	return m.commit(client, cfg)
}

// ---------- 内部实现 ----------

// commit 落盘并让运行中的实例跟上新配置。
//
// 优先热更新：与服务器的连接保持不变，代理的增删改立刻生效。
// 热更新失败时退化为重启，保证磁盘上的配置一定会被应用。
func (m *Manager) commit(name string, cfg *v1.ClientConfig) error {
	if err := m.store.Save(name, cfg); err != nil {
		return err
	}

	instance := m.instance(name)
	if instance == nil {
		return nil
	}
	if err := instance.Reload(); err != nil {
		logger.Warn("[%s] FRP 配置热更新失败，改为重启：%v", name, err)
		return m.restartClient(name)
	}
	return nil
}

func (m *Manager) startClient(name string) error {
	if m.instance(name) != nil {
		return fmt.Errorf("FRP 客户端 %s 已在运行", name)
	}

	instance := newInstance(name, m.store.Path(name))
	if err := instance.Start(); err != nil {
		return err
	}

	m.mu.Lock()
	m.running[name] = instance
	m.mu.Unlock()

	logger.Info("[%s] FRP 客户端已启动", name)
	return nil
}

func (m *Manager) stopClient(name string) {
	m.mu.Lock()
	instance := m.running[name]
	delete(m.running, name)
	m.mu.Unlock()

	if instance != nil {
		instance.Stop()
		logger.Info("[%s] FRP 客户端已停止", name)
	}
}

func (m *Manager) restartClient(name string) error {
	m.stopClient(name)
	return m.startClient(name)
}

func (m *Manager) instance(name string) *Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running[name]
}
