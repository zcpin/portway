package manager

import (
	"testing"

	"github.com/byteporter/portway/internal/config"
)

func TestAutoStartPolicyAndMetadataPreserveManualState(t *testing.T) {
	path, key := writeBaseConfig(t)
	mgr := newTestManager(t, path)
	t.Cleanup(mgr.Stop)
	disabled := false
	auto := config.Tunnel{Name: "auto", LocalPort: 15432, RemoteHost: "127.0.0.1", RemotePort: 5432,
		SSHHost: "127.0.0.1:1", SSHUser: "test", KeyFile: key}
	manual := auto
	manual.Name, manual.LocalPort, manual.AutoStart = "manual", 15433, &disabled
	for _, entry := range []config.Tunnel{auto, manual} {
		if err := mgr.AddTunnel(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	if status := mgr.GetStatus(); !status["auto"] || status["manual"] {
		t.Fatalf("startup: %v", status)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tunnels[1].AutoStartEnabled() {
		t.Fatal("explicit false was lost on disk")
	}
	if err := mgr.StartTunnel("manual"); err != nil {
		t.Fatal(err)
	}
	original := mgr.tunnels["manual"]
	manual.Group = "production"
	manual.AutoStart = nil
	if err := mgr.UpdateTunnel("manual", manual); err != nil {
		t.Fatal(err)
	}
	if mgr.tunnels["manual"] != original || !mgr.GetStatus()["manual"] {
		t.Fatal("metadata edit restarted or stopped the tunnel")
	}
	if err := mgr.StopTunnel("manual"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reload(path); err != nil {
		t.Fatal(err)
	}
	if mgr.GetStatus()["manual"] {
		t.Fatal("reload restarted a manually stopped tunnel")
	}
	manual.Name, manual.LocalPort, manual.AutoStart = "new-manual", 15434, &disabled
	if err := mgr.AddTunnel(manual); err != nil {
		t.Fatal(err)
	}
	if mgr.GetStatus()["new-manual"] {
		t.Fatal("new tunnel ignored auto_start=false")
	}
}
