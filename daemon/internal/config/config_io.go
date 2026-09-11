package config

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// ConfigIO handles reading and writing configuration files
type ConfigIO struct {
	mu     sync.RWMutex
	path   string
	config *Config
}

// NewConfigIO creates a new configuration I/O handler
func NewConfigIO(path string) (*ConfigIO, error) {
	var cfg *Config
	if path != "" {
		var err error
		cfg, err = Load(path)
		if err != nil {
			return nil, err
		}
	} else {
		cfg = &Config{LogLevel: "info"}
	}

	return &ConfigIO{
		path:   path,
		config: cfg,
	}, nil
}

// GetConfig returns a copy of the current configuration
func (cio *ConfigIO) GetConfig() *Config {
	cio.mu.RLock()
	defer cio.mu.RUnlock()

	// Return a deep copy to prevent concurrent modification
	result := &Config{
		LogLevel:             cio.config.LogLevel,
		ReconnectStrategy:    cio.config.ReconnectStrategy,
		ReconnectInterval:    cio.config.ReconnectInterval,
		MaxReconnectAttempts: cio.config.MaxReconnectAttempts,
		// configDir 未导出，必须手动带上：否则拿到副本的一方解析相对路径时
		// 会丢掉「配置文件所在目录」这一候选位置
		configDir: cio.config.configDir,
	}

	result.SSHConnections = make([]SSHConnection, len(cio.config.SSHConnections))
	copy(result.SSHConnections, cio.config.SSHConnections)

	result.Tunnels = make([]Tunnel, len(cio.config.Tunnels))
	copy(result.Tunnels, cio.config.Tunnels)

	return result
}

// Replace 用一份已加载的配置整体替换内存中的配置。
//
// 供 Reload 使用：配置来自磁盘，无需再写回文件，
// 但必须同步到这里，否则后续 GetConfig 会返回旧配置。
func (cio *ConfigIO) Replace(cfg *Config) {
	cio.mu.Lock()
	defer cio.mu.Unlock()
	cio.config = cfg
}

// GlobalSettings 返回当前全局配置项（日志级别、重连默认值）。
//
// 未在配置文件中显式给出的字段返回生效的默认值（与 Config.Validate 一致），
// 客户端据此回显，而不是看到一堆空字符串。
func (cio *ConfigIO) GlobalSettings() GlobalSettings {
	cio.mu.RLock()
	defer cio.mu.RUnlock()

	s := GlobalSettings{
		LogLevel:             cio.config.LogLevel,
		ReconnectStrategy:    cio.config.ReconnectStrategy,
		ReconnectInterval:    cio.config.ReconnectInterval,
		MaxReconnectAttempts: cio.config.MaxReconnectAttempts,
	}
	if s.LogLevel == "" {
		s.LogLevel = "info"
	}
	if s.ReconnectStrategy == "" {
		s.ReconnectStrategy = "fixed"
	}
	if s.ReconnectInterval == "" {
		s.ReconnectInterval = "5s"
	}
	return s
}

// SetGlobalSettings 更新全局配置项并写回配置文件。
//
// 只改动顶层字段，不会触碰 ssh_connections / tunnels 的内容。
// 校验规则与 Config.Validate 保持一致。
func (cio *ConfigIO) SetGlobalSettings(s GlobalSettings) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	s.LogLevel = strings.TrimSpace(s.LogLevel)
	s.ReconnectStrategy = strings.TrimSpace(s.ReconnectStrategy)
	s.ReconnectInterval = strings.TrimSpace(s.ReconnectInterval)

	if s.ReconnectStrategy == "" {
		s.ReconnectStrategy = "fixed"
	}
	if s.ReconnectStrategy != "fixed" && s.ReconnectStrategy != "exponential" {
		return fmt.Errorf("reconnect_strategy must be 'fixed' or 'exponential', got %q", s.ReconnectStrategy)
	}

	if s.ReconnectInterval == "" {
		s.ReconnectInterval = "5s"
	}
	if d, err := time.ParseDuration(s.ReconnectInterval); err != nil {
		return fmt.Errorf("invalid reconnect_interval: %w", err)
	} else if d <= 0 {
		return fmt.Errorf("reconnect_interval must be greater than 0, got %s", s.ReconnectInterval)
	}

	if s.MaxReconnectAttempts < 0 {
		return fmt.Errorf("max_reconnect_attempts must be >= 0, got %d", s.MaxReconnectAttempts)
	}

	cio.config.LogLevel = s.LogLevel
	cio.config.ReconnectStrategy = s.ReconnectStrategy
	cio.config.ReconnectInterval = s.ReconnectInterval
	cio.config.MaxReconnectAttempts = s.MaxReconnectAttempts

	return cio.save()
}

// AddTunnel adds a new tunnel to the configuration
func (cio *ConfigIO) AddTunnel(tunnel Tunnel) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	// Check for duplicate name
	for _, t := range cio.config.Tunnels {
		if t.Name == tunnel.Name {
			return fmt.Errorf("tunnel with name '%s' already exists", tunnel.Name)
		}
	}

	// Check for duplicate local port
	for _, t := range cio.config.Tunnels {
		if t.LocalPort == tunnel.LocalPort {
			return fmt.Errorf("local port %d is already in use by tunnel '%s'", tunnel.LocalPort, t.Name)
		}
	}

	// Validate tunnel configuration
	if err := cio.validateTunnel(tunnel); err != nil {
		return err
	}

	cio.config.Tunnels = append(cio.config.Tunnels, tunnel)

	return cio.save()
}

// UpdateTunnel updates an existing tunnel
func (cio *ConfigIO) UpdateTunnel(name string, updated Tunnel) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	// Find and update the tunnel
	found := false
	for i, t := range cio.config.Tunnels {
		if t.Name == name {
			// Check for port conflict with other tunnels
			for j, other := range cio.config.Tunnels {
				if j != i && other.LocalPort == updated.LocalPort {
					return fmt.Errorf("local port %d is already in use by tunnel '%s'", updated.LocalPort, other.Name)
				}
			}

			// Validate tunnel configuration
			if err := cio.validateTunnel(updated); err != nil {
				return err
			}

			cio.config.Tunnels[i] = updated
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("tunnel '%s' not found", name)
	}

	return cio.save()
}

// DeleteTunnel removes a tunnel from the configuration
func (cio *ConfigIO) DeleteTunnel(name string) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	found := false
	newTunnels := make([]Tunnel, 0, len(cio.config.Tunnels)-1)

	for _, t := range cio.config.Tunnels {
		if t.Name != name {
			newTunnels = append(newTunnels, t)
		} else {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("tunnel '%s' not found", name)
	}

	cio.config.Tunnels = newTunnels

	return cio.save()
}

// save writes the configuration to disk
func (cio *ConfigIO) save() error {
	if cio.path == "" {
		return nil
	}

	// Create a temporary file
	tmpPath := cio.path + ".tmp"

	file, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// Encode the configuration
	encoder := toml.NewEncoder(file)
	if err := encoder.Encode(cio.config); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to encode configuration: %w", err)
	}

	// Close the file（Windows 上必须先关闭才能 Rename）
	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Replace the original file
	if err := os.Rename(tmpPath, cio.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to replace config file: %w", err)
	}

	return nil
}

// AddSSHConnection adds a new SSH connection
func (cio *ConfigIO) AddSSHConnection(conn SSHConnection) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	// Check for duplicate name
	for _, c := range cio.config.SSHConnections {
		if c.Name == conn.Name {
			return fmt.Errorf("SSH connection with name '%s' already exists", conn.Name)
		}
	}

	// Validate
	if err := validateSSHConnection(conn); err != nil {
		return err
	}

	cio.config.SSHConnections = append(cio.config.SSHConnections, conn)

	return cio.save()
}

// UpdateSSHConnection updates an SSH connection
func (cio *ConfigIO) UpdateSSHConnection(name string, conn SSHConnection) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	found := false
	for i, c := range cio.config.SSHConnections {
		if c.Name == name {
			if err := validateSSHConnection(conn); err != nil {
				return err
			}
			cio.config.SSHConnections[i] = conn
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("SSH connection '%s' not found", name)
	}

	return cio.save()
}

// DeleteSSHConnection removes an SSH connection
func (cio *ConfigIO) DeleteSSHConnection(name string) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	// Check if any tunnel is using this connection
	for _, t := range cio.config.Tunnels {
		if t.SSHConnection == name {
			return fmt.Errorf("cannot delete SSH connection '%s': tunnel '%s' is using it", name, t.Name)
		}
	}

	found := false
	newConns := make([]SSHConnection, 0, len(cio.config.SSHConnections)-1)

	for _, c := range cio.config.SSHConnections {
		if c.Name != name {
			newConns = append(newConns, c)
		} else {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("SSH connection '%s' not found", name)
	}

	cio.config.SSHConnections = newConns

	return cio.save()
}

// validateTunnel validates a single tunnel configuration
func (cio *ConfigIO) validateTunnel(tunnel Tunnel) error {
	if tunnel.Name == "" {
		return fmt.Errorf("tunnel name is required")
	}
	if tunnel.LocalPort == 0 {
		return fmt.Errorf("local_port is required")
	}
	if tunnel.RemoteHost == "" {
		return fmt.Errorf("remote_host is required")
	}
	if tunnel.RemotePort == 0 {
		return fmt.Errorf("remote_port is required")
	}

	// Either SSH connection reference or direct config
	if tunnel.SSHConnection == "" && tunnel.SSHHost == "" {
		return fmt.Errorf("must specify either ssh_connection or ssh_host")
	}

	if tunnel.SSHConnection == "" {
		// Direct SSH config
		if tunnel.SSHUser == "" {
			return fmt.Errorf("ssh_user is required when not using ssh_connection")
		}
		if tunnel.KeyFile == "" {
			return fmt.Errorf("key_file is required when not using ssh_connection")
		}
	}

	return nil
}

// validateSSHConnection validates an SSH connection configuration
func validateSSHConnection(conn SSHConnection) error {
	if conn.Name == "" {
		return fmt.Errorf("SSH connection name is required")
	}
	if conn.Host == "" {
		return fmt.Errorf("SSH host is required")
	}
	if conn.User == "" {
		return fmt.Errorf("SSH user is required")
	}
	if conn.KeyFile == "" {
		return fmt.Errorf("SSH key file is required")
	}
	return nil
}
