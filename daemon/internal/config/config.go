package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config represents the main configuration structure
type Config struct {
	LogLevel       string          `toml:"log_level"`
	SSHConnections []SSHConnection `toml:"ssh_connections"`
	Tunnels        []Tunnel        `toml:"tunnels"`
	configDir      string
}

// SSHConnection represents a reusable SSH connection configuration
type SSHConnection struct {
	Name    string `toml:"name" json:"name"`
	Host    string `toml:"host" json:"host"`
	User    string `toml:"user" json:"user"`
	KeyFile string `toml:"key_file" json:"key_file"`
	// HostKeyCheck 控制主机密钥校验：known_hosts（默认）或 insecure（不校验）
	HostKeyCheck string `toml:"host_key_check,omitempty" json:"host_key_check,omitempty"`
	// KnownHostsFile 指定 known_hosts 文件；留空时用 ~/.ssh/known_hosts
	KnownHostsFile string `toml:"known_hosts_file,omitempty" json:"known_hosts_file,omitempty"`
}

// Tunnel represents a single SSH tunnel configuration
type Tunnel struct {
	Name       string `toml:"name" json:"name"`
	LocalPort  int    `toml:"local_port" json:"local_port"`
	RemoteHost string `toml:"remote_host" json:"remote_host"`
	RemotePort int    `toml:"remote_port" json:"remote_port"`
	// New: Reference to SSH connection
	SSHConnection string `toml:"ssh_connection,omitempty" json:"ssh_connection,omitempty"` // Reference to SSH connection name
	// Old: Direct SSH config (for backward compatibility)
	SSHHost              string `toml:"ssh_host,omitempty" json:"ssh_host,omitempty"`
	SSHUser              string `toml:"ssh_user,omitempty" json:"ssh_user,omitempty"`
	KeyFile              string `toml:"key_file,omitempty" json:"key_file,omitempty"`
	HostKeyCheck         string `toml:"host_key_check,omitempty" json:"host_key_check,omitempty"`     // known_hosts（默认）或 insecure
	KnownHostsFile       string `toml:"known_hosts_file,omitempty" json:"known_hosts_file,omitempty"` // 留空时用 ~/.ssh/known_hosts
	ReconnectStrategy    string `toml:"reconnect_strategy" json:"reconnect_strategy"`                 // "fixed" or "exponential"
	ReconnectInterval    string `toml:"reconnect_interval" json:"reconnect_interval"`                 // duration string, e.g., "5s"
	MaxReconnectAttempts int    `toml:"max_reconnect_attempts" json:"max_reconnect_attempts"`         // 0 = infinite
}

// ReconnectStrategy represents the reconnection strategy
type ReconnectStrategy string

const (
	StrategyFixed       ReconnectStrategy = "fixed"
	StrategyExponential ReconnectStrategy = "exponential"
)

// 主机密钥校验策略。
const (
	// HostKeyCheckKnownHosts 用 known_hosts 文件校验主机密钥
	HostKeyCheckKnownHosts = "known_hosts"
	// HostKeyCheckInsecure 不校验主机密钥（仅限可信网络）
	HostKeyCheckInsecure = "insecure"
	// DefaultHostKeyCheck 是未显式配置时的默认策略：安全优先
	DefaultHostKeyCheck = HostKeyCheckKnownHosts
)

// ParsedTunnel represents a tunnel with parsed configuration values
type ParsedTunnel struct {
	Name                 string
	LocalPort            int
	RemoteHost           string
	RemotePort           int
	SSHHost              string
	SSHUser              string
	KeyFile              string
	HostKeyCheck         string
	KnownHostsFile       string
	ReconnectStrategy    ReconnectStrategy
	ReconnectInterval    time.Duration
	MaxReconnectAttempts int
}

// Load loads and parses the configuration file
func Load(configPath string) (*Config, error) {
	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config path: %w", err)
	}

	data, err := os.ReadFile(absConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	cfg.configDir = filepath.Dir(absConfigPath)

	// Set default log level
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	return &cfg, nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	// Build SSH connection map
	sshConnMap := make(map[string]SSHConnection)
	for _, conn := range c.SSHConnections {
		if conn.Name == "" {
			return fmt.Errorf("SSH connection: name is required")
		}
		if conn.Host == "" {
			return fmt.Errorf("SSH connection %s: host is required", conn.Name)
		}
		if conn.User == "" {
			return fmt.Errorf("SSH connection %s: user is required", conn.Name)
		}
		if conn.KeyFile == "" {
			return fmt.Errorf("SSH connection %s: key_file is required", conn.Name)
		}
		if err := validateHostKeyCheck("SSH connection "+conn.Name, conn.HostKeyCheck); err != nil {
			return err
		}
		sshConnMap[conn.Name] = conn
	}

	// Check for duplicate local ports
	localPorts := make(map[int]bool)
	// 注意用下标取指针：默认值必须写回 c.Tunnels，只改 range 的副本会被丢弃，
	// 导致 ParseTunnels 拿到空字符串的 reconnect_interval。
	for i := range c.Tunnels {
		tunnel := &c.Tunnels[i]
		if tunnel.Name == "" {
			return fmt.Errorf("tunnel %d: name is required", i)
		}
		if tunnel.LocalPort == 0 {
			return fmt.Errorf("tunnel %s: local_port is required", tunnel.Name)
		}
		if tunnel.RemoteHost == "" {
			return fmt.Errorf("tunnel %s: remote_host is required", tunnel.Name)
		}
		if tunnel.RemotePort == 0 {
			return fmt.Errorf("tunnel %s: remote_port is required", tunnel.Name)
		}

		// Validate SSH configuration (either connection reference or direct config)
		if tunnel.SSHConnection != "" {
			if _, exists := sshConnMap[tunnel.SSHConnection]; !exists {
				return fmt.Errorf("tunnel %s: SSH connection '%s' not found", tunnel.Name, tunnel.SSHConnection)
			}
		} else if tunnel.SSHHost == "" {
			return fmt.Errorf("tunnel %s: must specify either ssh_connection or ssh_host", tunnel.Name)
		} else {
			// Direct SSH config validation
			if tunnel.SSHUser == "" {
				return fmt.Errorf("tunnel %s: ssh_user is required", tunnel.Name)
			}
			if tunnel.KeyFile == "" {
				return fmt.Errorf("tunnel %s: key_file is required", tunnel.Name)
			}
		}

		// Check for duplicate local ports
		if localPorts[tunnel.LocalPort] {
			return fmt.Errorf("tunnel %s: local_port %d is already in use", tunnel.Name, tunnel.LocalPort)
		}
		localPorts[tunnel.LocalPort] = true

		// Validate reconnect strategy
		if tunnel.ReconnectStrategy == "" {
			tunnel.ReconnectStrategy = "fixed"
		}
		if tunnel.ReconnectStrategy != "fixed" && tunnel.ReconnectStrategy != "exponential" {
			return fmt.Errorf("tunnel %s: reconnect_strategy must be 'fixed' or 'exponential'", tunnel.Name)
		}

		// Validate reconnect interval
		if tunnel.ReconnectInterval == "" {
			tunnel.ReconnectInterval = "5s"
		}
		interval, err := time.ParseDuration(tunnel.ReconnectInterval)
		if err != nil {
			return fmt.Errorf("tunnel %s: invalid reconnect_interval: %w", tunnel.Name, err)
		}
		// 0 或负数会让重连变成空转热循环，必须在这里拦下
		if interval <= 0 {
			return fmt.Errorf("tunnel %s: reconnect_interval must be greater than 0, got %s", tunnel.Name, tunnel.ReconnectInterval)
		}

		if err := validateHostKeyCheck("tunnel "+tunnel.Name, tunnel.HostKeyCheck); err != nil {
			return err
		}
	}

	return nil
}

// ParseTunnels parses and validates all tunnels, expanding environment variables
func (c *Config) ParseTunnels() ([]ParsedTunnel, error) {
	var parsed []ParsedTunnel

	// Build a map of SSH connections for quick lookup
	sshConnMap := make(map[string]SSHConnection)
	for _, conn := range c.SSHConnections {
		sshConnMap[conn.Name] = conn
	}

	for _, tunnel := range c.Tunnels {
		var sshHost, sshUser, keyFile string
		var hostKeyCheck, knownHostsFile string
		var err error

		// Resolve SSH configuration
		if tunnel.SSHConnection != "" {
			// Use SSH connection reference
			conn, exists := sshConnMap[tunnel.SSHConnection]
			if !exists {
				return nil, fmt.Errorf("tunnel %s: SSH connection '%s' not found", tunnel.Name, tunnel.SSHConnection)
			}
			sshHost = conn.Host
			sshUser = conn.User
			// 与 ResolveKeyPath 用同一套规则（环境变量 + ~ + 候选目录），
			// 否则界面能解析的 ~ 路径在隧道启动时会被当成相对路径
			keyFile = c.ResolveKeyPath(conn.KeyFile)
			hostKeyCheck = conn.HostKeyCheck
			knownHostsFile = conn.KnownHostsFile
		} else {
			// Use direct SSH config (backward compatibility)
			if tunnel.SSHHost == "" {
				return nil, fmt.Errorf("tunnel %s: must specify either ssh_connection or ssh_host", tunnel.Name)
			}
			sshHost = tunnel.SSHHost
			sshUser = tunnel.SSHUser
			keyFile = c.ResolveKeyPath(tunnel.KeyFile)
			hostKeyCheck = tunnel.HostKeyCheck
			knownHostsFile = tunnel.KnownHostsFile
		}

		if hostKeyCheck == "" {
			hostKeyCheck = DefaultHostKeyCheck
		}
		if hostKeyCheck == HostKeyCheckKnownHosts {
			if knownHostsFile == "" {
				knownHostsFile = defaultKnownHostsPath()
			} else {
				knownHostsFile = c.ResolveKeyPath(knownHostsFile)
			}
			// 文件此时不必存在：连接时会再次校验并给出明确错误，
			// 这样单个 known_hosts 缺失不会让整个 daemon 起不来
			if knownHostsFile == "" {
				return nil, fmt.Errorf("tunnel %s: unable to determine known_hosts path, set known_hosts_file or host_key_check = %q", tunnel.Name, HostKeyCheckInsecure)
			}
		}

		normalizedSSHHost, err := normalizeSSHHost(sshHost)
		if err != nil {
			return nil, fmt.Errorf("tunnel %s: invalid ssh host '%s': %w", tunnel.Name, sshHost, err)
		}
		sshHost = normalizedSSHHost

		// Check if key file exists
		if _, err := os.Stat(keyFile); err != nil {
			return nil, fmt.Errorf("tunnel %s: key file not found: %s", tunnel.Name, keyFile)
		}

		// Parse reconnect interval
		interval, err := time.ParseDuration(tunnel.ReconnectInterval)
		if err != nil {
			return nil, fmt.Errorf("tunnel %s: invalid reconnect_interval: %w", tunnel.Name, err)
		}

		// Default to fixed strategy if not specified
		strategy := StrategyFixed
		if tunnel.ReconnectStrategy == "exponential" {
			strategy = StrategyExponential
		}

		parsed = append(parsed, ParsedTunnel{
			Name:                 tunnel.Name,
			LocalPort:            tunnel.LocalPort,
			RemoteHost:           tunnel.RemoteHost,
			RemotePort:           tunnel.RemotePort,
			SSHHost:              sshHost,
			SSHUser:              sshUser,
			KeyFile:              keyFile,
			HostKeyCheck:         hostKeyCheck,
			KnownHostsFile:       knownHostsFile,
			ReconnectStrategy:    strategy,
			ReconnectInterval:    interval,
			MaxReconnectAttempts: tunnel.MaxReconnectAttempts,
		})
	}

	return parsed, nil
}

// validateHostKeyCheck 校验 host_key_check 取值，空串表示使用默认策略。
func validateHostKeyCheck(owner, value string) error {
	switch value {
	case "", HostKeyCheckKnownHosts, HostKeyCheckInsecure:
		return nil
	default:
		return fmt.Errorf("%s: host_key_check must be %q or %q, got %q", owner, HostKeyCheckKnownHosts, HostKeyCheckInsecure, value)
	}
}

// defaultKnownHostsPath 返回 ~/.ssh/known_hosts，主目录不可知时返回空串。
func defaultKnownHostsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}

func normalizeSSHHost(host string) (string, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return "", fmt.Errorf("host is empty")
	}

	if h, p, err := net.SplitHostPort(trimmed); err == nil {
		if p == "" {
			return "", fmt.Errorf("port is empty")
		}
		if _, err := strconv.Atoi(p); err != nil {
			return "", fmt.Errorf("invalid port: %s", p)
		}
		return net.JoinHostPort(h, p), nil
	}

	if strings.Count(trimmed, ":") == 0 {
		return net.JoinHostPort(trimmed, "22"), nil
	}

	if strings.Count(trimmed, ":") > 1 && !strings.HasPrefix(trimmed, "[") {
		return net.JoinHostPort(trimmed, "22"), nil
	}

	if strings.HasSuffix(trimmed, "]") || strings.HasPrefix(trimmed, "[") {
		return net.JoinHostPort(strings.Trim(trimmed, "[]"), "22"), nil
	}

	parts := strings.Split(trimmed, ":")
	if len(parts) == 2 {
		if parts[1] == "" {
			return net.JoinHostPort(parts[0], "22"), nil
		}
		if _, err := strconv.Atoi(parts[1]); err != nil {
			return "", fmt.Errorf("invalid port: %s", parts[1])
		}
		return net.JoinHostPort(parts[0], parts[1]), nil
	}

	return "", fmt.Errorf("invalid host format")
}

// ResolveKeyPath 解析配置中填写的私钥路径。
//
// 私钥以「本地文件路径」的形式引用，不要求复制到任何固定目录，
// 因此这里沿用与隧道运行时一致的解析规则：先展开环境变量，
// 再按候选目录（配置目录、可执行文件目录、keys/、工作目录）查找。
func (c *Config) ResolveKeyPath(path string) string {
	return c.resolvePath(expandHome(expandEnv(path)))
}

// expandHome 把开头的 ~ 展开为用户主目录。
//
// 私钥路径常写成 ~/.ssh/id_ed25519，若不展开会被当作相对路径，
// 拼到可执行文件目录下，导致明明存在的文件被判定为缺失。
func expandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	if len(path) > 1 && path[1] != '/' && path[1] != os.PathSeparator {
		return path // 形如 ~user/... 的路径不做处理
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if len(path) == 1 {
		return home
	}
	return filepath.Join(home, path[1:])
}

func (c *Config) resolvePath(path string) string {
	if path == "" {
		return path
	}

	// candidateDirs 每次都做系统调用，取一次即可
	dirs := c.candidateDirs()

	if filepath.IsAbs(path) {
		if _, err := os.Stat(path); err == nil {
			return filepath.Clean(path)
		}
		// Absolute path doesn't exist — try to find the file by basename in candidate dirs
		base := filepath.Base(path)
		if base != "" && base != path {
			for _, candidate := range dirs {
				resolved := filepath.Join(candidate, base)
				if _, err := os.Stat(resolved); err == nil {
					return filepath.Clean(resolved)
				}
			}
		}
		return filepath.Clean(path)
	}

	for _, candidate := range dirs {
		resolved := filepath.Join(candidate, path)
		if _, err := os.Stat(resolved); err == nil {
			return filepath.Clean(resolved)
		}
	}

	if len(dirs) > 0 {
		return filepath.Clean(filepath.Join(dirs[0], path))
	}

	return filepath.Clean(path)
}

func (c *Config) candidateDirs() []string {
	candidates := make([]string, 0, 3)
	if c.configDir != "" {
		candidates = append(candidates, c.configDir)
	}

	if execPath, err := os.Executable(); err == nil {
		execDir := filepath.Dir(execPath)
		candidates = append(candidates, execDir)
		// Also check keys/ subdirectory under exe dir
		candidates = append(candidates, filepath.Join(execDir, "keys"))
	}

	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}

	return candidates
}

// expandEnv expands environment variables in a string
// Supports both Unix ($VAR) and Windows (%VAR%) syntax
func expandEnv(s string) string {
	// Handle Windows-style environment variables first (%VAR%)
	result := s

	// Process %VAR% patterns
	for {
		startIdx := strings.Index(result, "%")
		if startIdx == -1 {
			break
		}

		endIdx := strings.Index(result[startIdx+1:], "%")
		if endIdx == -1 {
			break
		}
		endIdx += startIdx + 1

		// Extract variable name
		varName := result[startIdx+1 : endIdx]
		varValue := os.Getenv(varName)

		// Replace with value
		result = result[:startIdx] + varValue + result[endIdx+1:]
	}

	// Handle Unix-style variables ($VAR) for compatibility
	// Note: We don't use os.ExpandEnv directly because it may interfere
	// Process $VAR patterns
	for {
		startIdx := strings.Index(result, "$")
		if startIdx == -1 {
			break
		}

		// Find end of variable name (alphanumeric and underscore)
		endIdx := startIdx + 1
		for endIdx < len(result) {
			c := result[endIdx]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				break
			}
			endIdx++
		}

		if endIdx == startIdx+1 {
			// No variable name, skip
			result = result[:startIdx] + result[startIdx+1:]
			continue
		}

		varName := result[startIdx+1 : endIdx]
		varValue := os.Getenv(varName)

		// Replace with value
		result = result[:startIdx] + varValue + result[endIdx:]
	}

	return filepath.Clean(result)
}
