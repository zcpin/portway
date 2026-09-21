package app

import (
	"context"

	"github.com/byteporter/portway/internal/config"
	"github.com/byteporter/portway/internal/tunnel"
)

func (a *App) InspectHostKey(ctx context.Context, conn config.SSHConnection) (tunnel.HostKeyInfo, error) {
	return a.mgr.InspectHostKey(ctx, conn)
}

func (a *App) TrustHostKey(ctx context.Context, conn config.SSHConnection, fingerprint string, replace bool) (tunnel.HostKeyInfo, error) {
	return a.mgr.TrustHostKey(ctx, conn, fingerprint, replace)
}
