package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig 把 TOML 内容写入临时文件并返回路径。
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return path
}

// writeKeyFile 创建一个充当私钥的占位文件并返回路径。
func writeKeyFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, []byte("dummy key"), 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}
	return path
}

// keyFileConfig 生成一份使用真实绝对私钥路径的配置，interval 为空串时省略该字段。
func keyFileConfig(keyPath, interval string) string {
	intervalLine := ""
	if interval != "" {
		intervalLine = fmt.Sprintf("reconnect_interval = %q\n", interval)
	}
	return fmt.Sprintf(`
[[ssh_connections]]
name = "c1"
host = "example.com:22"
user = "root"
key_file = %q

[[tunnels]]
name = "t1"
ssh_connection = "c1"
local_port = 13306
remote_host = "127.0.0.1"
remote_port = 3306
%s`, keyPath, intervalLine)
}

func TestParseTunnelsExpandsHomeInKeyFile(t *testing.T) {
	// 把主目录指到临时目录，让 ~ 展开可预测（Windows 读 USERPROFILE，Unix 读 HOME）
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatalf("创建 .ssh 目录失败: %v", err)
	}
	keyPath := filepath.Join(sshDir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("dummy key"), 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}

	cfgPath := writeConfig(t, `
[[ssh_connections]]
name = "c1"
host = "example.com:22"
user = "root"
key_file = "~/.ssh/id_ed25519"

[[tunnels]]
name = "t1"
ssh_connection = "c1"
local_port = 13306
remote_host = "127.0.0.1"
remote_port = 3306
`)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	parsed, err := cfg.ParseTunnels()
	if err != nil {
		t.Fatalf("ParseTunnels() error = %v, ~ 开头的 key_file 应当能解析", err)
	}
	if parsed[0].KeyFile != keyPath {
		t.Errorf("KeyFile = %q, want %q", parsed[0].KeyFile, keyPath)
	}
}

func TestValidatePersistsReconnectDefaults(t *testing.T) {
	keyPath := writeKeyFile(t, t.TempDir())
	cfgPath := writeConfig(t, keyFileConfig(keyPath, ""))

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	// 默认值必须写回配置本身，而不是只改了 range 的副本
	if cfg.Tunnels[0].ReconnectInterval != "5s" {
		t.Errorf("Validate 后 ReconnectInterval = %q, want %q", cfg.Tunnels[0].ReconnectInterval, "5s")
	}

	parsed, err := cfg.ParseTunnels()
	if err != nil {
		t.Fatalf("省略 reconnect_interval 时 ParseTunnels() 不应报错, got %v", err)
	}
	if parsed[0].ReconnectInterval != 5*time.Second {
		t.Errorf("ReconnectInterval = %v, want %v", parsed[0].ReconnectInterval, 5*time.Second)
	}
	if parsed[0].ReconnectStrategy != StrategyFixed {
		t.Errorf("ReconnectStrategy = %q, want %q", parsed[0].ReconnectStrategy, StrategyFixed)
	}
}

func TestValidateRejectsNonPositiveReconnectInterval(t *testing.T) {
	for _, interval := range []string{"0s", "-1s"} {
		t.Run(interval, func(t *testing.T) {
			keyPath := writeKeyFile(t, t.TempDir())
			cfgPath := writeConfig(t, keyFileConfig(keyPath, interval))

			cfg, err := Load(cfgPath)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate() 应当拒绝 reconnect_interval = %q", interval)
			}
		})
	}
}

func TestParseTunnelsDefaultsToKnownHostsCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfgPath := writeConfig(t, keyFileConfig(writeKeyFile(t, t.TempDir()), "5s"))
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	parsed, err := cfg.ParseTunnels()
	if err != nil {
		t.Fatalf("ParseTunnels() error = %v", err)
	}
	if parsed[0].HostKeyCheck != HostKeyCheckKnownHosts {
		t.Errorf("HostKeyCheck = %q，期望默认 %q", parsed[0].HostKeyCheck, HostKeyCheckKnownHosts)
	}
	want := filepath.Join(home, ".ssh", "known_hosts")
	if parsed[0].KnownHostsFile != want {
		t.Errorf("KnownHostsFile = %q，期望 %q", parsed[0].KnownHostsFile, want)
	}
}

func TestGlobalReconnectDefaultsFallback(t *testing.T) {
	keyPath := writeKeyFile(t, t.TempDir())
	cfgPath := writeConfig(t, fmt.Sprintf(`
reconnect_strategy = "exponential"
reconnect_interval = "30s"
max_reconnect_attempts = 4

[[ssh_connections]]
name = "c1"
host = "example.com:22"
user = "root"
key_file = %q

[[tunnels]]
name = "t1"
ssh_connection = "c1"
local_port = 13306
remote_host = "127.0.0.1"
remote_port = 3306

[[tunnels]]
name = "t2"
ssh_connection = "c1"
local_port = 13307
remote_host = "127.0.0.1"
remote_port = 3307
reconnect_interval = "10s"
max_reconnect_attempts = 2
`, keyPath))

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	parsed, err := cfg.ParseTunnels()
	if err != nil {
		t.Fatalf("ParseTunnels() error = %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed tunnels = %d, want 2", len(parsed))
	}

	// t1 未显式配置重连字段，应回退到全局值
	if parsed[0].ReconnectStrategy != StrategyExponential {
		t.Errorf("t1 ReconnectStrategy = %q, want %q", parsed[0].ReconnectStrategy, StrategyExponential)
	}
	if parsed[0].ReconnectInterval != 30*time.Second {
		t.Errorf("t1 ReconnectInterval = %v, want %v", parsed[0].ReconnectInterval, 30*time.Second)
	}
	if parsed[0].MaxReconnectAttempts != 4 {
		t.Errorf("t1 MaxReconnectAttempts = %d, want 4", parsed[0].MaxReconnectAttempts)
	}

	// t2 显式配置了间隔与次数，应保留自己的值；策略仍回退到全局
	if parsed[1].ReconnectStrategy != StrategyExponential {
		t.Errorf("t2 ReconnectStrategy = %q, want %q", parsed[1].ReconnectStrategy, StrategyExponential)
	}
	if parsed[1].ReconnectInterval != 10*time.Second {
		t.Errorf("t2 ReconnectInterval = %v, want %v", parsed[1].ReconnectInterval, 10*time.Second)
	}
	if parsed[1].MaxReconnectAttempts != 2 {
		t.Errorf("t2 MaxReconnectAttempts = %d, want 2", parsed[1].MaxReconnectAttempts)
	}
}

func TestConfigIOSetGlobalSettingsPersists(t *testing.T) {
	keyPath := writeKeyFile(t, t.TempDir())
	cfgPath := writeConfig(t, keyFileConfig(keyPath, "5s"))

	cio, err := NewConfigIO(cfgPath)
	if err != nil {
		t.Fatalf("NewConfigIO() error = %v", err)
	}
	if err := cio.SetGlobalSettings(GlobalSettings{
		LogLevel:             "debug",
		ReconnectStrategy:    "exponential",
		ReconnectInterval:    "30s",
		MaxReconnectAttempts: 4,
	}); err != nil {
		t.Fatalf("SetGlobalSettings() error = %v", err)
	}

	// 重新加载验证已写回文件，且隧道内容不受影响
	reloaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reloaded.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", reloaded.LogLevel, "debug")
	}
	if reloaded.ReconnectStrategy != "exponential" {
		t.Errorf("ReconnectStrategy = %q, want %q", reloaded.ReconnectStrategy, "exponential")
	}
	if reloaded.ReconnectInterval != "30s" {
		t.Errorf("ReconnectInterval = %q, want %q", reloaded.ReconnectInterval, "30s")
	}
	if reloaded.MaxReconnectAttempts != 4 {
		t.Errorf("MaxReconnectAttempts = %d, want 4", reloaded.MaxReconnectAttempts)
	}
	if len(reloaded.Tunnels) != 1 {
		t.Errorf("Tunnels = %d, want 1", len(reloaded.Tunnels))
	}
}

func TestHostKeyCheckValidation(t *testing.T) {
	for _, value := range []string{"insecure", "known_hosts"} {
		t.Run(value, func(t *testing.T) {
			keyPath := writeKeyFile(t, t.TempDir())
			content := keyFileConfig(keyPath, "5s") + fmt.Sprintf("host_key_check = %q\n", value)
			cfg, err := Load(writeConfig(t, content))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() 应当接受 host_key_check = %q: %v", value, err)
			}
		})
	}

	t.Run("unknown", func(t *testing.T) {
		keyPath := writeKeyFile(t, t.TempDir())
		content := keyFileConfig(keyPath, "5s") + "host_key_check = \"typo\"\n"
		cfg, err := Load(writeConfig(t, content))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate() 应当拒绝未知的 host_key_check")
		}
	})
}
