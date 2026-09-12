package config

import (
	"reflect"
	"testing"
)

func TestForwardModesAndLocalListenerValidation(t *testing.T) {
	base := Tunnel{Name: "local", LocalPort: 15432, RemoteHost: "127.0.0.1", RemotePort: 5432,
		SSHHost: "example:22", SSHUser: "test", KeyFile: "key"}
	for _, mode := range []string{"local", "remote", "dynamic"} {
		entry := base
		entry.Mode = mode
		if mode == "dynamic" {
			entry.RemoteHost, entry.RemotePort = "", 0
		}
		cfg := Config{Tunnels: []Tunnel{entry}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		parsed, err := cfg.ParseTunnels()
		if err != nil || parsed[0].Mode != mode || parsed[0].LocalHost != "127.0.0.1" {
			t.Fatalf("%s: %+v %v", mode, parsed, err)
		}
	}
	for _, mode := range []string{"local", "dynamic"} {
		entry := base
		entry.Mode, entry.LocalHost = mode, "0.0.0.0"
		if err := (&Config{Tunnels: []Tunnel{entry}}).Validate(); err == nil {
			t.Fatal("public local listener accepted")
		}
	}
	remote := base
	remote.Name, remote.Mode = "remote", "remote"
	if err := (&Config{Tunnels: []Tunnel{base, remote}}).Validate(); err != nil {
		t.Fatalf("remote destination was treated as a local listener: %v", err)
	}
}

func TestJumpChainExpansionAndValidation(t *testing.T) {
	connection := func(name string, jumps ...string) SSHConnection {
		return SSHConnection{
			Name: name, Host: name + ":22", User: "test", AuthMethod: "agent", ProxyJump: jumps}
	}
	cfg := Config{SSHConnections: []SSHConnection{connection("first"), connection("second", "first"), connection("target", "second")},
		Tunnels: []Tunnel{{Name: "db", LocalPort: 15432, RemoteHost: "db", RemotePort: 5432, SSHConnection: "target"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	parsed, err := cfg.ParseTunnels()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, hop := range parsed[0].Jumps {
		names = append(names, hop.Name)
		if hop.AuthMethod != "agent" || hop.AgentSocket != "" {
			t.Fatalf("jump auth: %+v", hop)
		}
	}
	if !reflect.DeepEqual(names, []string{"first", "second"}) {
		t.Fatalf("chain: %v", names)
	}
	for _, invalid := range [][]SSHConnection{
		{connection("first", "missing")},
		{connection("first", "first")},
		{connection("first", "second"), connection("second", "first")},
		{connection("first"), connection("second", "first", "first")},
	} {
		if err := (&Config{SSHConnections: invalid}).Validate(); err == nil {
			t.Fatalf("invalid chain accepted: %+v", invalid)
		}
	}
	var long []SSHConnection
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		var jumps []string
		if len(long) != 0 {
			jumps = []string{long[len(long)-1].Name}
		}
		long = append(long, connection(name, jumps...))
	}
	long = append(long, connection("target", "i"))
	if err := (&Config{SSHConnections: long}).Validate(); err == nil {
		t.Fatal("overlong chain accepted")
	}
}

func TestJumpSlicesAreIsolatedFromConfigSnapshots(t *testing.T) {
	cio, _ := configIOFixture(t)
	conn := cio.GetConfig().SSHConnections[0]
	conn.ProxyJump = []string{"spare"}
	if err := cio.UpdateSSHConnection(conn.Name, conn); err != nil {
		t.Fatal(err)
	}
	conn.ProxyJump[0] = "changed"
	copy := cio.GetConfig()
	copy.SSHConnections[0].ProxyJump[0] = "also changed"
	if cio.GetConfig().SSHConnections[0].ProxyJump[0] != "spare" {
		t.Fatal("jump slices share mutable storage")
	}
}
