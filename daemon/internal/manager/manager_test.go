package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/byteporter/ssh-tunnel/internal/config"
)

// writeBaseConfig 写入一份没有隧道的配置，返回配置路径与可用的私钥路径。
func writeBaseConfig(t *testing.T) (cfgPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()

	keyPath = filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("dummy key"), 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}

	cfgPath = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("log_level = \"info\"\n"), 0600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return cfgPath, keyPath
}

func newTestManager(t *testing.T, cfgPath string) *Manager {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	mgr, err := NewManager(cfg, cfgPath)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return mgr
}

func TestManagerAddDeleteTunnel(t *testing.T) {
	cfgPath, keyPath := writeBaseConfig(t)
	mgr := newTestManager(t, cfgPath)

	tun := config.Tunnel{
		Name:              "t1",
		LocalPort:         13306,
		RemoteHost:        "127.0.0.1",
		RemotePort:        3306,
		SSHHost:           "example.com:22",
		SSHUser:           "root",
		KeyFile:           keyPath,
		ReconnectStrategy: "fixed",
		ReconnectInterval: "5s",
	}

	if err := mgr.AddTunnel(tun); err != nil {
		t.Fatalf("AddTunnel() error = %v", err)
	}
	status := mgr.GetStatus()
	if _, ok := status["t1"]; !ok {
		t.Fatal("AddTunnel 后 GetStatus 应包含 t1")
	}
	if status["t1"] {
		t.Error("未调用 Start 时隧道不应处于运行状态")
	}

	if err := mgr.DeleteTunnel("t1"); err != nil {
		t.Fatalf("DeleteTunnel() error = %v", err)
	}
	if _, ok := mgr.GetStatus()["t1"]; ok {
		t.Fatal("DeleteTunnel 后 GetStatus 不应包含 t1")
	}
}

// TestManagerReloadPicksUpDiskChanges 验证 Reload 会把磁盘上的新配置同步进
// ConfigIO（历史 bug：Reload 只更新了内部字段，GetConfig/GetStatus 仍是旧配置）。
func TestManagerReloadPicksUpDiskChanges(t *testing.T) {
	cfgPath, keyPath := writeBaseConfig(t)
	mgr := newTestManager(t, cfgPath)

	if got := len(mgr.GetStatus()); got != 0 {
		t.Fatalf("初始隧道数 = %d，期望 0", got)
	}

	content := fmt.Sprintf(`
[[ssh_connections]]
name = "c1"
host = "example.com:22"
user = "root"
key_file = %q

[[tunnels]]
name = "t2"
ssh_connection = "c1"
local_port = 13307
remote_host = "127.0.0.1"
remote_port = 3306
`, keyPath)
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatalf("改写配置失败: %v", err)
	}

	if err := mgr.Reload(cfgPath); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	if _, ok := mgr.GetStatus()["t2"]; !ok {
		t.Fatal("Reload 后 GetStatus 应包含磁盘上的新隧道")
	}
	if got := len(mgr.GetSSHConnections()); got != 1 {
		t.Errorf("Reload 后 SSH 连接数 = %d，期望 1", got)
	}
}
