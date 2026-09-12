package config

import "testing"

func TestAgentConfigurationDoesNotRequirePrivateKey(t *testing.T) {
	cio, _ := configIOFixture(t)
	connection := cio.GetConfig().SSHConnections[0]
	connection.KeyFile, connection.AuthMethod, connection.AgentSocket = "", "agent", "fixture-agent"
	if err := cio.UpdateSSHConnection(connection.Name, connection); err != nil {
		t.Fatal(err)
	}
	cfg := cio.GetConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	parsed, err := cfg.ParseTunnels()
	if err != nil {
		t.Fatal(err)
	}
	if parsed[0].AuthMethod != "agent" || parsed[0].AgentSocket != "fixture-agent" || parsed[0].KeyFile != "" {
		t.Fatalf("agent config: %+v", parsed[0])
	}
	connection.AuthMethod = "unsupported"
	if err := cio.UpdateSSHConnection(connection.Name, connection); err == nil {
		t.Fatal("unsupported authentication method accepted")
	}
}
