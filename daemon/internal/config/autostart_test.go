package config

import "testing"

func TestAutoStartPointersAreIsolated(t *testing.T) {
	cio, err := NewConfigIO("")
	if err != nil {
		t.Fatal(err)
	}
	value := false
	entry := Tunnel{Name: "manual", LocalPort: 15432, RemoteHost: "127.0.0.1", RemotePort: 5432,
		SSHHost: "127.0.0.1:1", SSHUser: "test", KeyFile: "missing", AutoStart: &value}
	if err := cio.AddTunnel(entry); err != nil {
		t.Fatal(err)
	}
	value = true
	if cio.GetConfig().Tunnels[0].AutoStartEnabled() {
		t.Fatal("caller changed committed config through a pointer")
	}
	copy := cio.GetConfig()
	*copy.Tunnels[0].AutoStart = true
	if cio.GetConfig().Tunnels[0].AutoStartEnabled() {
		t.Fatal("returned snapshot aliases internal config")
	}
}
