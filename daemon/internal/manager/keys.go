package manager

import (
	"context"

	"github.com/byteporter/ssh-tunnel/internal/config"
	"github.com/byteporter/ssh-tunnel/internal/tunnel"
)

func (m *Manager) UnlockKey(path string, passphrase []byte) error {
	return m.keys.Unlock(m.GetConfig().ResolveKeyPath(path), passphrase)
}
func (m *Manager) LockKey(path string)                { m.keys.Lock(m.GetConfig().ResolveKeyPath(path)) }
func (m *Manager) KeyStatus(path string) (bool, bool) { return m.keys.KeyStatus(path) }

func (m *Manager) TestSSHConnection(ctx context.Context, conn config.SSHConnection) (tunnel.Diagnostic, error) {
	parsed, err := m.GetConfig().ParseSSHConnection(conn)
	if err != nil {
		return tunnel.Diagnostic{}, err
	}
	return tunnel.TestConnection(ctx, parsed, m.keys), nil
}
