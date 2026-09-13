package tunnel

import (
	"context"
	"sync"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/logger"
)

// startLocked creates a new run generation. Its caller must launch run exactly
// once, including when Stop cancels this generation before it starts dialing.
func (t *Tunnel) startLocked(state string) (context.Context, *sync.WaitGroup) {
	ctx, cancel := context.WithCancel(context.Background())
	t.ctx, t.cancel = ctx, cancel
	t.sshClient, t.forwarder = nil, nil
	t.wg = &sync.WaitGroup{}
	t.wg.Add(1)
	t.isRunning = true
	t.manualStop = false
	t.status = RuntimeStatus{State: state}
	return ctx, t.wg
}

// RecoverNetwork preserves healthy connections and the user's start/stop intent.
// Exhausting a retry limit ends the run, but a later network change can retry it.
func (t *Tunnel) RecoverNetwork(ctx context.Context, reason string) bool {
	t.mu.Lock()
	previous, client := t.ctx, t.sshClient
	wanted := previous != nil && !t.manualStop
	connected := t.status.State == StateConnected
	t.mu.Unlock()
	if !wanted || ctx.Err() != nil {
		return false
	}
	if connected && client != nil && connectionHealthy(ctx, client) {
		return false
	}
	t.mu.Lock()
	if t.ctx != previous || t.manualStop || ctx.Err() != nil {
		t.mu.Unlock()
		return false
	}
	oldCancel, oldClient, oldForwarder, oldWG := t.cancel, t.sshClient, t.forwarder, t.wg
	runCtx, wg := t.startLocked(StateReconnecting)
	t.mu.Unlock()
	go func() {
		// Release the old listener before starting a replacement. Publishing the
		// new generation first lets a concurrent manual Stop cancel recovery.
		if oldCancel != nil {
			oldCancel()
		}
		if oldClient != nil {
			_ = oldClient.Close()
		}
		if oldForwarder != nil {
			oldForwarder.Stop()
		}
		if oldWG != nil {
			oldWG.Wait()
		}
		if runCtx.Err() == nil {
			logger.Info("[%s] Recovering after %s", t.config.Name, reason)
		}
		t.run(runCtx, wg)
	}()
	return true
}

func connectionHealthy(ctx context.Context, client sshConn) bool {
	result := make(chan error, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		result <- err
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		return err == nil
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}
