package manager

import (
	"context"
	"sync"

	"github.com/byteporter/ssh-tunnel/internal/tunnel"
)

func (m *Manager) RecoverNetwork(ctx context.Context, reason string) {
	m.mu.RLock()
	tunnels := make([]*tunnel.Tunnel, 0, len(m.tunnels))
	for _, tun := range m.tunnels {
		tunnels = append(tunnels, tun)
	}
	m.mu.RUnlock()
	jobs := make(chan *tunnel.Tunnel)
	var workers sync.WaitGroup
	for range min(8, len(tunnels)) {
		workers.Go(func() {
			for tun := range jobs {
				tun.RecoverNetwork(ctx, reason)
			}
		})
	}
	for _, tun := range tunnels {
		select {
		case jobs <- tun:
		case <-ctx.Done():
		}
	}
	close(jobs)
	workers.Wait()
}
