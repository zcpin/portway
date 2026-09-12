package config

import "fmt"

type SSHHop struct {
	Name, Host, User, KeyFile, AuthMethod, AgentSocket, HostKeyCheck, KnownHostsFile string
}

func (p ParsedTunnel) TargetHop() SSHHop {
	return SSHHop{Name: p.Name, Host: p.SSHHost, User: p.SSHUser, KeyFile: p.KeyFile,
		AuthMethod: p.AuthMethod, AgentSocket: p.AgentSocket, HostKeyCheck: p.HostKeyCheck, KnownHostsFile: p.KnownHostsFile}
}

func jumpNames(connections map[string]SSHConnection, names []string, owner string) ([]string, error) {
	stack, seen := map[string]bool{}, map[string]bool{}
	if owner != "" {
		stack[owner] = true
	}
	result := make([]string, 0)
	var visit func(string, int) error
	visit = func(name string, depth int) error {
		if stack[name] {
			return fmt.Errorf("proxy_jump cycle at %q", name)
		}
		if seen[name] {
			return fmt.Errorf("proxy_jump repeats %q", name)
		}
		if depth >= 8 || len(result) >= 8 {
			return fmt.Errorf("proxy_jump supports at most 8 hops")
		}
		conn, found := connections[name]
		if !found {
			return fmt.Errorf("proxy_jump connection %q not found", name)
		}
		stack[name] = true
		defer delete(stack, name)
		for _, dependency := range conn.ProxyJump {
			if err := visit(dependency, depth+1); err != nil {
				return err
			}
		}
		if len(result) >= 8 {
			return fmt.Errorf("proxy_jump supports at most 8 hops")
		}
		seen[name] = true
		result = append(result, name)
		return nil
	}
	for _, name := range names {
		if err := visit(name, 0); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (c *Config) parseHop(conn SSHConnection) (SSHHop, error) {
	host, err := normalizeSSHHost(conn.Host)
	if err != nil {
		return SSHHop{}, err
	}
	hop := SSHHop{Name: conn.Name, Host: host, User: conn.User, AuthMethod: conn.AuthMethod,
		AgentSocket: expandHome(expandEnv(conn.AgentSocket)), HostKeyCheck: conn.HostKeyCheck}
	if hop.AuthMethod == "" {
		hop.AuthMethod = "key"
	}
	if hop.AuthMethod != "agent" {
		hop.KeyFile = c.ResolveKeyPath(conn.KeyFile)
	}
	if hop.HostKeyCheck == "" {
		hop.HostKeyCheck = DefaultHostKeyCheck
	}
	if hop.HostKeyCheck == HostKeyCheckKnownHosts {
		hop.KnownHostsFile = defaultKnownHostsPath()
		if conn.KnownHostsFile != "" {
			hop.KnownHostsFile = c.ResolveKeyPath(conn.KnownHostsFile)
		}
		if hop.KnownHostsFile == "" {
			return SSHHop{}, fmt.Errorf("jump %s: known_hosts path is unavailable", conn.Name)
		}
	}
	return hop, nil
}
