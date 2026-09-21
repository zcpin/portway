package app

import (
	"context"

	"github.com/byteporter/portway/internal/tunnel"
)

func (a *App) DiagnoseTunnel(ctx context.Context, name string) (tunnel.TunnelDiagnostic, error) {
	return a.mgr.DiagnoseTunnel(ctx, name)
}
