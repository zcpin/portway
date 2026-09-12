package manager

import (
	"context"

	"github.com/byteporter/ssh-tunnel/internal/config"
	"github.com/byteporter/ssh-tunnel/internal/tunnel"
)

func (m *Manager) InspectHostKey(ctx context.Context, conn config.SSHConnection) (tunnel.HostKeyInfo, error) {
	conn.HostKeyCheck = config.HostKeyCheckKnownHosts
	parsed, err := m.GetConfig().ParseSSHConnection(conn)
	if err != nil {
		return tunnel.HostKeyInfo{}, err
	}
	return tunnel.InspectHostKey(ctx, parsed)
}

func (m *Manager) TrustHostKey(ctx context.Context, conn config.SSHConnection, fingerprint string, replace bool) (tunnel.HostKeyInfo, error) {
	conn.HostKeyCheck = config.HostKeyCheckKnownHosts
	parsed, err := m.GetConfig().ParseSSHConnection(conn)
	if err != nil {
		return tunnel.HostKeyInfo{}, err
	}
	return tunnel.TrustHostKey(ctx, parsed, fingerprint, replace)
}
