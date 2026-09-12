package tunnel

import (
	"context"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/config"
)

const (
	StateStopped      = "stopped"
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateReconnecting = "reconnecting"
	StateFailed       = "failed"
)

// RuntimeStatus distinguishes a running retry loop from a usable connection.
type RuntimeStatus struct {
	IsRunning   bool   `json:"is_running"`
	State       string `json:"state"`
	LastError   string `json:"last_error"`
	RetryCount  int    `json:"retry_count"`
	ConnectedAt string `json:"connected_at"`
}

func (t *Tunnel) Status() RuntimeStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	status := t.status
	status.IsRunning = t.isRunning
	if status.State == "" {
		status.State = StateStopped
	}
	return status
}

func (t *Tunnel) setStatus(ctx context.Context, state string, err error, retry bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctx != ctx || ctx.Err() != nil {
		return
	}
	t.status.State = state
	if err != nil {
		t.status.LastError = err.Error()
	}
	if retry {
		t.status.RetryCount++
	}
	if state == StateConnected {
		t.status.LastError = ""
		t.status.ConnectedAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		t.status.ConnectedAt = ""
	}
}

type Diagnostic struct {
	OK        bool   `json:"ok"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Error     string `json:"error,omitempty"`
}

// TestConnection performs one bounded SSH handshake without starting a forwarder.
func TestConnection(ctx context.Context, cfg config.ParsedTunnel) Diagnostic {
	started := time.Now()
	t, err := NewTunnel(cfg)
	if err == nil {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		var conn sshConn
		conn, err = t.createSSHConnection(ctx)
		if conn != nil {
			_ = conn.Close()
		}
	}
	result := Diagnostic{OK: err == nil, ElapsedMS: time.Since(started).Milliseconds()}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}
