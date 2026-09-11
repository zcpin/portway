package config

import (
	"fmt"
	"os"
	"strings"
	"sync"

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

	return cloneConfig(cio.config)
}

func cloneConfig(cfg *Config) *Config {
	result := *cfg // 保留 configDir，以及尚未显式设置的重连字段。
	result.SSHConnections = make([]SSHConnection, len(cfg.SSHConnections))
	copy(result.SSHConnections, cfg.SSHConnections)
	result.Tunnels = make([]Tunnel, len(cfg.Tunnels))
	copy(result.Tunnels, cfg.Tunnels)
	return &result
}

// validateConfig 在副本上补齐默认值，防止校验把继承关系变成显式配置。
func validateConfig(cfg *Config) error {
	validated := cloneConfig(cfg)
	if err := validated.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if _, err := validated.ParseTunnels(); err != nil {
		return fmt.Errorf("failed to parse tunnels: %w", err)
	}
	return nil
}

// commit 在调用方持锁时先校验、再持久化，全部成功后才替换内存状态。
func (cio *ConfigIO) commit(candidate *Config) error {
	if err := validateConfig(candidate); err != nil {
		return err
	}
	if err := cio.save(candidate); err != nil {
		return err
	}
	cio.config = candidate
	return nil
}

// Replace 用一份已加载的配置整体替换内存中的配置。
//
// 供 Reload 使用：配置来自磁盘，无需再写回文件，
// 但必须同步到这里，否则后续 GetConfig 会返回旧配置。
func (cio *ConfigIO) Replace(cfg *Config) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()
	candidate := cloneConfig(cfg)
	if err := validateConfig(candidate); err != nil {
		return err
	}
	cio.config = candidate
	return nil
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

	if s.LogLevel == "" {
		s.LogLevel = "info"
	}
	if s.ReconnectStrategy == "" {
		s.ReconnectStrategy = "fixed"
	}
	if s.ReconnectInterval == "" {
		s.ReconnectInterval = "5s"
	}

	candidate := cloneConfig(cio.config)
	candidate.LogLevel = s.LogLevel
	candidate.ReconnectStrategy = s.ReconnectStrategy
	candidate.ReconnectInterval = s.ReconnectInterval
	candidate.MaxReconnectAttempts = s.MaxReconnectAttempts
	return cio.commit(candidate)
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

	candidate := cloneConfig(cio.config)
	candidate.Tunnels = append(candidate.Tunnels, tunnel)
	return cio.commit(candidate)
}

// UpdateTunnel updates an existing tunnel
func (cio *ConfigIO) UpdateTunnel(name string, updated Tunnel) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	// Find and update the tunnel
	candidate := cloneConfig(cio.config)
	found := false
	for i, t := range cio.config.Tunnels {
		if t.Name == name {
			// Check for port conflict with other tunnels
			for j, other := range cio.config.Tunnels {
				if j != i && other.LocalPort == updated.LocalPort {
					return fmt.Errorf("local port %d is already in use by tunnel '%s'", updated.LocalPort, other.Name)
				}
			}

			candidate.Tunnels[i] = updated
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("tunnel '%s' not found", name)
	}

	return cio.commit(candidate)
}

// DeleteTunnel removes a tunnel from the configuration
func (cio *ConfigIO) DeleteTunnel(name string) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	found := false
	newTunnels := make([]Tunnel, 0, len(cio.config.Tunnels))

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

	candidate := cloneConfig(cio.config)
	candidate.Tunnels = newTunnels
	return cio.commit(candidate)
}

// save writes the configuration to disk
func (cio *ConfigIO) save(candidate *Config) error {
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
	if err := encoder.Encode(candidate); err != nil {
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

	candidate := cloneConfig(cio.config)
	candidate.SSHConnections = append(candidate.SSHConnections, conn)
	return cio.commit(candidate)
}

// UpdateSSHConnection updates an SSH connection
func (cio *ConfigIO) UpdateSSHConnection(name string, conn SSHConnection) error {
	cio.mu.Lock()
	defer cio.mu.Unlock()

	candidate := cloneConfig(cio.config)
	found := false
	for i, c := range cio.config.SSHConnections {
		if c.Name == name {
			candidate.SSHConnections[i] = conn
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("SSH connection '%s' not found", name)
	}

	return cio.commit(candidate)
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
	newConns := make([]SSHConnection, 0, len(cio.config.SSHConnections))

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

	candidate := cloneConfig(cio.config)
	candidate.SSHConnections = newConns
	return cio.commit(candidate)
}
