package manager

import (
	"errors"
	"fmt"
	"sync"

	"github.com/byteporter/ssh-tunnel/internal/config"
	"github.com/byteporter/ssh-tunnel/internal/logger"
	"github.com/byteporter/ssh-tunnel/internal/tunnel"
)

// Manager manages multiple SSH tunnels
type Manager struct {
	tunnels    map[string]*tunnel.Tunnel
	configIO   *config.ConfigIO
	configPath string
	mu         sync.RWMutex
	updateMu   sync.Mutex // 串行化配置提交及其运行状态、日志级别的应用。
	stopOnce   sync.Once
	running    bool
	keys       *tunnel.KeyStore
}

// NewManager creates a new tunnel manager
func NewManager(cfg *config.Config, configPath string) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	parsedTunnels, err := cfg.ParseTunnels()
	if err != nil {
		return nil, fmt.Errorf("failed to parse tunnels: %w", err)
	}

	// Create ConfigIO for dynamic configuration updates
	configIO, err := config.NewConfigIO(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create config IO: %w", err)
	}

	mgr := &Manager{
		tunnels:    make(map[string]*tunnel.Tunnel),
		configIO:   configIO,
		configPath: configPath,
		keys:       tunnel.NewKeyStore(),
	}

	// Create tunnels from configuration
	for _, parsedTunnel := range parsedTunnels {
		tun, err := tunnel.NewTunnel(parsedTunnel, mgr.keys)
		if err != nil {
			return nil, fmt.Errorf("failed to create tunnel %s: %w", parsedTunnel.Name, err)
		}
		mgr.tunnels[parsedTunnel.Name] = tun
	}

	return mgr, nil
}

// Start starts all managed tunnels
func (m *Manager) Start() error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	autoStart := automaticTunnels(m.configIO.GetConfig())
	logger.Info("Starting %d tunnel(s)...", len(m.tunnels))

	m.mu.Lock()
	defer m.mu.Unlock()

	// Start all tunnels
	for name, tun := range m.tunnels {
		if !autoStart[name] {
			continue
		}
		if err := tun.Start(); err != nil {
			logger.Error("Failed to start tunnel %s: %v", name, err)
			// Stop any started tunnels
			for _, t := range m.tunnels {
				if t.IsRunning() {
					t.Stop()
				}
			}
			return fmt.Errorf("failed to start tunnel %s: %w", name, err)
		}
	}

	m.running = true
	logger.Info("All tunnels started successfully")

	return nil
}

// Stop stops all managed tunnels
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		logger.Info("Stopping all tunnels...")

		m.mu.RLock()
		tunnelsToStop := make([]*tunnel.Tunnel, 0)
		for _, tun := range m.tunnels {
			if tun.IsRunning() {
				tunnelsToStop = append(tunnelsToStop, tun)
			}
		}
		m.mu.RUnlock()

		for _, tun := range tunnelsToStop {
			logger.Debug("Stopping tunnel %s...", tun.GetName())
			tun.Stop()
		}

		m.mu.Lock()
		m.running = false
		m.mu.Unlock()

		logger.Info("All tunnels stopped")
		m.keys.Clear()
	})
}

// GetStatus returns the status of all tunnels
func (m *Manager) GetStatus() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := make(map[string]bool)
	for name, tun := range m.tunnels {
		status[name] = tun.IsRunning()
	}

	return status
}

func (m *Manager) GetRuntimeStatus() map[string]tunnel.RuntimeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := make(map[string]tunnel.RuntimeStatus, len(m.tunnels))
	for name, tun := range m.tunnels {
		status[name] = tun.Status()
	}
	return status
}

// StartTunnel starts a specific tunnel
func (m *Manager) StartTunnel(name string) error {
	m.mu.RLock()
	tun, exists := m.tunnels[name]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("tunnel %s not found", name)
	}

	if tun.IsRunning() {
		return fmt.Errorf("tunnel %s is already running", name)
	}

	return tun.Start()
}

// StopTunnel stops a specific tunnel
func (m *Manager) StopTunnel(name string) error {
	m.mu.RLock()
	tun, exists := m.tunnels[name]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("tunnel %s not found", name)
	}

	if !tun.IsRunning() {
		return fmt.Errorf("tunnel %s is not running", name)
	}

	tun.Stop()
	return nil
}

// GetConfigPath returns the configuration file path
func (m *Manager) GetConfigPath() string {
	return m.configPath
}

// GetConfig returns the current configuration
func (m *Manager) GetConfig() *config.Config {
	return m.configIO.GetConfig()
}

// GetGlobalSettings 返回全局配置项（日志级别、重连默认值）。
func (m *Manager) GetGlobalSettings() config.GlobalSettings {
	return m.configIO.GlobalSettings()
}

// SetGlobalSettings 更新全局配置项并重载隧道。
//
// 重载会让未显式配置重连字段的隧道立刻套用新默认值（其运行状态会随之
// 停止再启动）；显式配置了自己的重连字段的隧道不受影响。
func (m *Manager) SetGlobalSettings(s config.GlobalSettings) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.configIO.SetGlobalSettings(s); err != nil {
		return err
	}
	if err := m.reloadInternal(); err != nil {
		return err
	}
	return m.applyLogLevel()
}

func (m *Manager) applyLogLevel() error {
	level, err := logger.ParseLevel(m.configIO.GlobalSettings().LogLevel)
	if err != nil {
		return err
	}
	logger.GetGlobalLogger().SetLevel(level)
	return nil
}

// GetSSHConnections returns all SSH connections
func (m *Manager) GetSSHConnections() []config.SSHConnection {
	return m.configIO.GetConfig().SSHConnections
}

// AddSSHConnection adds a new SSH connection
func (m *Manager) AddSSHConnection(conn config.SSHConnection) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.configIO.AddSSHConnection(conn); err != nil {
		return err
	}
	return m.reloadInternal()
}

// UpdateSSHConnection updates an SSH connection
func (m *Manager) UpdateSSHConnection(name string, conn config.SSHConnection) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.configIO.UpdateSSHConnection(name, conn); err != nil {
		return err
	}
	return m.reloadInternal()
}

// DeleteSSHConnection deletes an SSH connection
func (m *Manager) DeleteSSHConnection(name string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.configIO.DeleteSSHConnection(name); err != nil {
		return err
	}
	return m.reloadInternal()
}

// AddTunnel adds a new tunnel
func (m *Manager) AddTunnel(tunnel config.Tunnel) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.configIO.AddTunnel(tunnel); err != nil {
		return err
	}

	// Reload configuration
	return m.reloadInternal()
}

// UpdateTunnel updates an existing tunnel
func (m *Manager) UpdateTunnel(name string, tunnel config.Tunnel) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.configIO.UpdateTunnel(name, tunnel); err != nil {
		return err
	}

	// Reload configuration
	return m.reloadInternal()
}

// DeleteTunnel deletes a tunnel
func (m *Manager) DeleteTunnel(name string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	// 保存成功后由 reloadInternal 停止已移除的隧道。
	if err := m.configIO.DeleteTunnel(name); err != nil {
		return err
	}

	// Reload configuration
	return m.reloadInternal()
}

// reloadInternal applies the current in-memory configuration to the tunnel set.
// 配置变更（Add/Update/Delete）与 Reload 共用这一条路径。
//
// 第一遍只创建对象，任何创建失败都不会改动现有状态；第二遍才替换 map
// 并停止/启动受影响的隧道。
func (m *Manager) reloadInternal() error {
	newCfg := m.configIO.GetConfig()
	autoStart := automaticTunnels(newCfg)
	if err := newCfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	parsedTunnels, err := newCfg.ParseTunnels()
	if err != nil {
		return fmt.Errorf("failed to parse tunnels: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 第一遍：只创建对象，不改动任何现有状态
	created := make(map[string]*tunnel.Tunnel, len(parsedTunnels))
	for _, pt := range parsedTunnels {
		if oldTun, exists := m.tunnels[pt.Name]; exists && parsedTunnelEqual(oldTun.GetConfig(), pt) {
			// Unchanged config: keep old tunnel and runtime state.
			created[pt.Name] = oldTun
			continue
		}

		newTun, err := tunnel.NewTunnel(pt, m.keys)
		if err != nil {
			return fmt.Errorf("failed to create tunnel %s: %w", pt.Name, err)
		}
		created[pt.Name] = newTun
	}

	// 第二遍：替换 map，并停掉/启动受影响的隧道
	newTunnels := make(map[string]*tunnel.Tunnel, len(parsedTunnels))
	var startErrs []error

	for _, pt := range parsedTunnels {
		newTun := created[pt.Name]
		oldTun, exists := m.tunnels[pt.Name]

		if !exists {
			newTunnels[pt.Name] = newTun
			if m.running && autoStart[pt.Name] {
				if err := newTun.Start(); err != nil {
					startErrs = append(startErrs, fmt.Errorf("failed to start new tunnel %s: %w", pt.Name, err))
				}
			}
			continue
		}

		// Unchanged config: keep old tunnel and runtime state.
		if oldTun == newTun {
			newTunnels[pt.Name] = oldTun
			continue
		}

		wasRunning := oldTun.IsRunning()
		if wasRunning {
			oldTun.Stop()
		}
		newTunnels[pt.Name] = newTun
		if wasRunning {
			if err := newTun.Start(); err != nil {
				startErrs = append(startErrs, fmt.Errorf("failed to start tunnel %s after config update: %w", pt.Name, err))
			}
		}
	}

	// Stop tunnels that were removed from the configuration
	for name, oldTun := range m.tunnels {
		if _, exists := newTunnels[name]; !exists && oldTun.IsRunning() {
			oldTun.Stop()
		}
	}

	m.tunnels = newTunnels

	return errors.Join(startErrs...)
}

func automaticTunnels(cfg *config.Config) map[string]bool {
	result := make(map[string]bool, len(cfg.Tunnels))
	for _, entry := range cfg.Tunnels {
		result[entry.Name] = entry.AutoStartEnabled()
	}
	return result
}

// Reload reloads the configuration from disk and applies it to the tunnels.
func (m *Manager) Reload(configPath string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	logger.Info("Reloading configuration from %s...", configPath)

	// Load new configuration
	newConfig, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// 同步进 ConfigIO，否则后续 GetConfig 以及由此驱动的隧道重载仍是旧配置
	if err := m.configIO.Replace(newConfig); err != nil {
		return err
	}

	if err := m.reloadInternal(); err != nil {
		return err
	}
	if err := m.applyLogLevel(); err != nil {
		return err
	}

	logger.Info("Configuration reloaded successfully")
	return nil
}

func parsedTunnelEqual(a, b config.ParsedTunnel) bool {
	return a.Name == b.Name &&
		a.LocalPort == b.LocalPort &&
		a.RemoteHost == b.RemoteHost &&
		a.RemotePort == b.RemotePort &&
		a.SSHHost == b.SSHHost &&
		a.SSHUser == b.SSHUser &&
		a.KeyFile == b.KeyFile &&
		a.AuthMethod == b.AuthMethod &&
		a.AgentSocket == b.AgentSocket &&
		a.HostKeyCheck == b.HostKeyCheck &&
		a.KnownHostsFile == b.KnownHostsFile &&
		a.ReconnectStrategy == b.ReconnectStrategy &&
		a.ReconnectInterval == b.ReconnectInterval &&
		a.MaxReconnectAttempts == b.MaxReconnectAttempts
}
