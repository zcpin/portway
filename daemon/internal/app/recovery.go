package app

import (
	"context"

	"github.com/byteporter/ssh-tunnel/internal/networkwatch"
)

func (a *App) WatchNetwork(ctx context.Context) {
	for reason := range networkwatch.Watch(ctx) {
		if ctx.Err() != nil {
			continue
		}
		a.mgr.RecoverNetwork(ctx, reason)
		if ctx.Err() == nil {
			a.emitStatus()
		}
	}
}
