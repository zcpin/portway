package manager

import (
	"context"
	"fmt"

	"github.com/byteporter/ssh-tunnel/internal/tunnel"
)

func (m *Manager) DiagnoseTunnel(ctx context.Context, name string) (tunnel.TunnelDiagnostic, error) {
	m.mu.RLock()
	tun, exists := m.tunnels[name]
	m.mu.RUnlock()
	if !exists {
		return tunnel.TunnelDiagnostic{}, fmt.Errorf("tunnel %s not found", name)
	}
	return tun.Diagnose(ctx), nil
}
