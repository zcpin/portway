// Package app 聚合隧道管理、配置读写、密钥管理与日志缓冲，
// 是与 UI 无关的业务层：它不知道事件最终发给谁，只通过 EventEmitter 抽象向外推送。
package app

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/config"
	"github.com/byteporter/ssh-tunnel/internal/logger"
	"github.com/byteporter/ssh-tunnel/internal/manager"
	"github.com/byteporter/ssh-tunnel/internal/tunnel"
)

const logBufferSize = 500

// TunnelInfo 是隧道配置与运行状态的合并视图，供 UI 直接渲染。
type TunnelInfo struct {
	Name                 string `json:"name"`
	Group                string `json:"group,omitempty"`
	AutoStart            *bool  `json:"auto_start,omitempty"`
	LocalPort            int    `json:"local_port"`
	RemoteHost           string `json:"remote_host"`
	RemotePort           int    `json:"remote_port"`
	SSHConnection        string `json:"ssh_connection"`
	SSHHost              string `json:"ssh_host"`
	SSHUser              string `json:"ssh_user"`
	KeyFile              string `json:"key_file,omitempty"`
	AuthMethod           string `json:"auth_method,omitempty"`
	AgentSocket          string `json:"agent_socket,omitempty"`
	HostKeyCheck         string `json:"host_key_check,omitempty"`
	KnownHostsFile       string `json:"known_hosts_file,omitempty"`
	ReconnectStrategy    string `json:"reconnect_strategy"`
	ReconnectInterval    string `json:"reconnect_interval"`
	MaxReconnectAttempts int    `json:"max_reconnect_attempts"`
	tunnel.RuntimeStatus
}

// LogEntry 是一条日志，同时用于历史回放与实时推送。
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Tunnel    string `json:"tunnel,omitempty"`
}

// KeyInfo 描述配置中引用的一个 SSH 私钥文件。
//
// 私钥以本地文件路径的形式引用，不需要上传到任何目录，因此这里同时给出
// 配置里填写的原始路径与实际解析出的路径，便于界面判断文件是否还在。
type KeyInfo struct {
	Name      string   `json:"name"`     // 文件名
	Path      string   `json:"path"`     // 配置中填写的原始路径
	Resolved  string   `json:"resolved"` // 解析后的绝对路径
	Exists    bool     `json:"exists"`   // 文件是否存在
	Size      int64    `json:"size"`
	Modified  string   `json:"modified"`
	UsedBy    []string `json:"used_by"` // 引用它的 SSH 连接或隧道名
	Encrypted bool     `json:"encrypted"`
	Unlocked  bool     `json:"unlocked"`
}

// EventEmitter 把业务事件推送出去，具体实现由 server 层注入（WebSocket Hub）。
type EventEmitter interface {
	Emit(event string, data interface{})
}

// App 是业务层门面。
type App struct {
	mgr        *manager.Manager
	configPath string
	logBuffer  []LogEntry
	logMu      sync.RWMutex
	statusMu   sync.Mutex // 快照与状态事件按生成顺序入队。
	emitMu     sync.RWMutex
	emitter    EventEmitter
	stopOnce   sync.Once
	stopChan   chan struct{}
}

// New 加载配置并创建隧道管理器。
func New(configPath string) (*App, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load configuration failed: %w", err)
	}

	if err := logger.InitGlobalLogger(cfg.LogLevel, logger.IsTerminal(os.Stdout)); err != nil {
		return nil, fmt.Errorf("initialize logger failed: %w", err)
	}

	mgr, err := manager.NewManager(cfg, configPath)
	if err != nil {
		return nil, fmt.Errorf("create manager failed: %w", err)
	}

	a := &App{
		mgr:        mgr,
		configPath: configPath,
		logBuffer:  make([]LogEntry, 0, logBufferSize),
		stopChan:   make(chan struct{}),
	}

	logger.SetGlobalLogHook(func(level, message string) {
		a.appendLog(level, message, extractTunnelName(message))
	})

	logger.Info("Configuration loaded from: %s", configPath)
	return a, nil
}

// SetEmitter 注入事件推送实现。
func (a *App) SetEmitter(e EventEmitter) {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()
	a.emitter = e
}

func (a *App) getEmitter() EventEmitter {
	a.emitMu.RLock()
	defer a.emitMu.RUnlock()
	return a.emitter
}

// Start 启动所有隧道并开启状态广播。
func (a *App) Start() error {
	go a.broadcastStatus()
	return a.mgr.Start()
}

// Stop 停止所有隧道。
func (a *App) Stop() {
	a.stopOnce.Do(func() {
		a.mgr.Stop()
		close(a.stopChan)
	})
}

// Wait 阻塞直到 Stop 被调用。
func (a *App) Wait() {
	<-a.stopChan
}

// ConfigPath 返回当前使用的配置文件路径。
func (a *App) ConfigPath() string {
	return a.configPath
}

// GetTunnels 返回所有隧道及其运行状态。
func (a *App) GetTunnels() []TunnelInfo {
	cfg := a.mgr.GetConfig()
	status := a.mgr.GetRuntimeStatus()

	result := make([]TunnelInfo, 0, len(cfg.Tunnels))
	for _, t := range cfg.Tunnels {
		result = append(result, TunnelInfo{
			Name:                 t.Name,
			Group:                t.Group,
			AutoStart:            t.AutoStart,
			LocalPort:            t.LocalPort,
			RemoteHost:           t.RemoteHost,
			RemotePort:           t.RemotePort,
			SSHConnection:        t.SSHConnection,
			SSHHost:              t.SSHHost,
			SSHUser:              t.SSHUser,
			KeyFile:              t.KeyFile,
			AuthMethod:           t.AuthMethod,
			AgentSocket:          t.AgentSocket,
			HostKeyCheck:         t.HostKeyCheck,
			KnownHostsFile:       t.KnownHostsFile,
			ReconnectStrategy:    t.ReconnectStrategy,
			ReconnectInterval:    t.ReconnectInterval,
			MaxReconnectAttempts: t.MaxReconnectAttempts,
			RuntimeStatus:        status[t.Name],
		})
	}
	return result
}

// GetStatus 返回所有隧道的运行状态。
func (a *App) GetStatus() map[string]bool {
	return a.mgr.GetStatus()
}

func (a *App) StartTunnel(name string) error {
	if err := a.mgr.StartTunnel(name); err != nil {
		return err
	}
	a.emitStatus()
	return nil
}

func (a *App) StopTunnel(name string) error {
	if err := a.mgr.StopTunnel(name); err != nil {
		return err
	}
	a.emitStatus()
	return nil
}

// RestartTunnel 先停后启。Stop 会等到 listener 与连接全部关闭，
// 端口此时已经释放，无需额外等待。
func (a *App) RestartTunnel(name string) error {
	if err := a.mgr.StopTunnel(name); err != nil {
		return err
	}
	if err := a.mgr.StartTunnel(name); err != nil {
		return err
	}
	a.emitStatus()
	return nil
}

func (a *App) GetSSHConnections() []config.SSHConnection {
	return a.mgr.GetSSHConnections()
}

func (a *App) TestSSHConnection(ctx context.Context, conn config.SSHConnection) (tunnel.Diagnostic, error) {
	return a.mgr.TestSSHConnection(ctx, conn)
}

func (a *App) UnlockKey(path string, passphrase []byte) error {
	return a.mgr.UnlockKey(path, passphrase)
}
func (a *App) LockKey(path string) { a.mgr.LockKey(path) }

// GetGlobalSettings 返回全局配置项（日志级别、重连默认值）。
func (a *App) GetGlobalSettings() config.GlobalSettings {
	return a.mgr.GetGlobalSettings()
}

// SetGlobalSettings 更新全局配置项；运行中的隧道若套用了新默认值会被重启。
func (a *App) SetGlobalSettings(s config.GlobalSettings) error {
	if err := a.mgr.SetGlobalSettings(s); err != nil {
		return err
	}
	a.emitSnapshot()
	return nil
}

func (a *App) AddSSHConnection(conn config.SSHConnection) error {
	if err := a.mgr.AddSSHConnection(conn); err != nil {
		return err
	}
	a.emitSnapshot()
	return nil
}

func (a *App) UpdateSSHConnection(name string, conn config.SSHConnection) error {
	if err := a.mgr.UpdateSSHConnection(name, conn); err != nil {
		return err
	}
	a.emitSnapshot()
	return nil
}

func (a *App) DeleteSSHConnection(name string) error {
	if err := a.mgr.DeleteSSHConnection(name); err != nil {
		return err
	}
	a.emitSnapshot()
	return nil
}

func (a *App) AddTunnel(tunnel config.Tunnel) error {
	if err := a.mgr.AddTunnel(tunnel); err != nil {
		return err
	}
	a.emitSnapshot()
	return nil
}

func (a *App) UpdateTunnel(name string, tunnel config.Tunnel) error {
	if err := a.mgr.UpdateTunnel(name, tunnel); err != nil {
		return err
	}
	a.emitSnapshot()
	return nil
}

func (a *App) DeleteTunnel(name string) error {
	if err := a.mgr.DeleteTunnel(name); err != nil {
		return err
	}
	a.emitSnapshot()
	return nil
}

// ReloadConfig 从磁盘重新加载配置。
func (a *App) ReloadConfig() error {
	if err := a.mgr.Reload(a.configPath); err != nil {
		return err
	}
	a.emitSnapshot()
	return nil
}

// GetLogs 返回日志缓冲的快照。
func (a *App) GetLogs() []LogEntry {
	a.logMu.RLock()
	defer a.logMu.RUnlock()

	result := make([]LogEntry, len(a.logBuffer))
	copy(result, a.logBuffer)
	return result
}

// ---------- 密钥文件管理 ----------

// ListKeys 列出配置中引用到的所有私钥，并报告它们是否仍存在于磁盘上。
//
// 私钥不复制到任何固定目录，界面只需展示「配置引用了哪些文件、文件还在不在」，
// 因此这里从 ssh_connections 与 tunnels 两处聚合路径。
func (a *App) ListKeys() []KeyInfo {
	cfg := a.mgr.GetConfig()

	// 同一个文件可能被多个连接引用，按原始路径去重并累积引用方
	type entry struct {
		raw    string
		usedBy []string
	}
	order := make([]string, 0, len(cfg.SSHConnections)+len(cfg.Tunnels))
	byRaw := make(map[string]*entry)

	add := func(raw, owner string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if e, ok := byRaw[raw]; ok {
			e.usedBy = append(e.usedBy, owner)
			return
		}
		byRaw[raw] = &entry{raw: raw, usedBy: []string{owner}}
		order = append(order, raw)
	}

	for _, c := range cfg.SSHConnections {
		if c.AuthMethod != "agent" {
			add(c.KeyFile, c.Name)
		}
	}
	for _, t := range cfg.Tunnels {
		if t.SSHConnection == "" && t.AuthMethod != "agent" {
			add(t.KeyFile, t.Name)
		}
	}

	keys := make([]KeyInfo, 0, len(order))
	for _, raw := range order {
		e := byRaw[raw]
		resolved := cfg.ResolveKeyPath(raw)

		info := KeyInfo{
			Name:     filepath.Base(resolved),
			Path:     raw,
			Resolved: resolved,
			UsedBy:   e.usedBy,
		}
		if st, err := os.Stat(resolved); err == nil && !st.IsDir() {
			info.Exists = true
			info.Size = st.Size()
			info.Modified = st.ModTime().Format(time.RFC3339)
			info.Encrypted, info.Unlocked = a.mgr.KeyStatus(resolved)
		}
		keys = append(keys, info)
	}
	return keys
}

// StatKey 检查一个私钥路径是否可用，供客户端在用户选完文件后立即校验。
func (a *App) StatKey(rawPath string) (KeyInfo, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return KeyInfo{}, fmt.Errorf("路径不能为空")
	}

	resolved := a.mgr.GetConfig().ResolveKeyPath(rawPath)

	st, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return KeyInfo{
				Name:     filepath.Base(resolved),
				Path:     rawPath,
				Resolved: resolved,
				Exists:   false,
			}, nil
		}
		return KeyInfo{}, err
	}
	if st.IsDir() {
		return KeyInfo{}, fmt.Errorf("这是一个目录，请选择私钥文件")
	}
	encrypted, unlocked := a.mgr.KeyStatus(resolved)

	return KeyInfo{
		Name:      filepath.Base(resolved),
		Path:      rawPath,
		Resolved:  resolved,
		Exists:    true,
		Size:      st.Size(),
		Modified:  st.ModTime().Format(time.RFC3339),
		Encrypted: encrypted,
		Unlocked:  unlocked,
	}, nil
}

// ---------- 内部实现 ----------

var tunnelTagPattern = regexp.MustCompile(`^\[([^\]]+)\]`)

// extractTunnelName 从 "[tunnel-name] message" 形式的日志中提取隧道名。
func extractTunnelName(message string) string {
	m := tunnelTagPattern.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func (a *App) appendLog(level, message, tunnel string) {
	entry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     level,
		Message:   message,
		Tunnel:    tunnel,
	}

	a.logMu.Lock()
	a.logBuffer = append(a.logBuffer, entry)
	if len(a.logBuffer) > logBufferSize {
		a.logBuffer = a.logBuffer[1:]
	}
	a.logMu.Unlock()

	if e := a.getEmitter(); e != nil {
		e.Emit("log", entry)
	}
}

// broadcastStatus 兜底广播：隧道可能因断线自动重连而改变状态，
// 轮询能捕捉到这类非主动触发的变化，且仅在状态真正变化时推送。
func (a *App) broadcastStatus() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var last map[string]tunnel.RuntimeStatus

	for {
		select {
		case <-a.stopChan:
			return
		case <-ticker.C:
			a.statusMu.Lock()
			status := a.mgr.GetRuntimeStatus()
			if !maps.Equal(status, last) {
				last = status
				if e := a.getEmitter(); e != nil {
					emitRuntime(e, status)
				}
			}
			a.statusMu.Unlock()
		}
	}
}

func (a *App) emitStatus() {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	if e := a.getEmitter(); e != nil {
		emitRuntime(e, a.mgr.GetRuntimeStatus())
	}
}

// SendSnapshot 给新连接发送完整列表，随后保留 status 事件供旧客户端使用。
// 生成和入队与其他状态事件串行，避免初始快照覆盖更新的推送。
func (a *App) SendSnapshot(target EventEmitter) {
	if target == nil {
		return
	}
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	tunnels := a.GetTunnels()
	status := make(map[string]tunnel.RuntimeStatus, len(tunnels))
	for _, entry := range tunnels {
		status[entry.Name] = entry.RuntimeStatus
	}
	target.Emit("snapshot", tunnels)
	emitRuntime(target, status)
}

func emitRuntime(target EventEmitter, runtime map[string]tunnel.RuntimeStatus) {
	status := make(map[string]bool, len(runtime))
	for name, entry := range runtime {
		status[name] = entry.IsRunning
	}
	target.Emit("status", status)
	target.Emit("runtime", runtime)
}

func (a *App) emitSnapshot() {
	a.SendSnapshot(a.getEmitter())
}

func statusEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func cloneStatus(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
