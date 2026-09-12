package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func awaitState(t *testing.T, tun *Tunnel, want string) RuntimeStatus {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status := tun.Status()
		if status.State == want {
			return status
		}
		select {
		case <-deadline:
			t.Fatalf("state = %+v, want %s", status, want)
		case <-ticker.C:
		}
	}
}

func TestRuntimeStatusLifecycle(t *testing.T) {
	tun := newTestTunnel(t, nil)
	client := newFakeSSHConn()
	release := make(chan struct{})
	tun.strategy, _ = ParseStrategy("fixed", time.Hour)
	tun.dial = func(ctx context.Context) (sshConn, error) {
		select {
		case <-release:
			return client, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	t.Cleanup(tun.Stop)
	if got := tun.Status(); got.State != StateStopped || got.IsRunning {
		t.Fatalf("initial: %+v", got)
	}
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	if got := tun.Status(); got.State != StateConnecting || !got.IsRunning {
		t.Fatalf("starting: %+v", got)
	}
	close(release)
	connected := awaitState(t, tun, StateConnected)
	if connected.ConnectedAt == "" || connected.LastError != "" {
		t.Fatalf("connected: %+v", connected)
	}
	client.Close()
	reconnecting := awaitState(t, tun, StateReconnecting)
	if !reconnecting.IsRunning || reconnecting.LastError == "" || reconnecting.RetryCount != 1 {
		t.Fatalf("retry: %+v", reconnecting)
	}
	tun.Stop()
	if got := tun.Status(); got.State != StateStopped || got.IsRunning || got.ConnectedAt != "" {
		t.Fatalf("stopped: %+v", got)
	}
}

func TestRuntimeStatusRetainsTerminalError(t *testing.T) {
	tun := newTestTunnel(t, nil)
	tun.config.MaxReconnectAttempts = 1
	tun.dial = func(context.Context) (sshConn, error) { return nil, errors.New("authentication rejected") }
	t.Cleanup(tun.Stop)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	awaitState(t, tun, StateFailed)
	tun.mu.Lock()
	wg := tun.wg
	tun.mu.Unlock()
	wg.Wait()
	if got := tun.Status(); got.IsRunning || got.LastError != "authentication rejected" || got.RetryCount != 0 {
		t.Fatalf("failed: %+v", got)
	}
}

func TestConnectionDiagnosticClosesSuccessfulHandshake(t *testing.T) {
	tun := newTestTunnel(t, nil)
	tun.config.KeyFile = writeTestKey(t)
	key, err := os.ReadFile(tun.config.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		server, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		go func() {
			for channel := range channels {
				channel.Reject(ssh.Prohibited, "diagnostics do not open channels")
			}
		}()
		_ = server.Wait()
	}()
	tun.config.SSHHost = listener.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := TestConnection(ctx, tun.config)
	if !result.OK || result.Error != "" {
		t.Fatalf("diagnostic: %+v", result)
	}
	awaitSignal(t, done, "diagnostic connection leaked")
	if tun.IsRunning() {
		t.Fatal("diagnostic started a tunnel")
	}
}

func TestConnectionDiagnosticHonorsCancellation(t *testing.T) {
	tun := newTestTunnel(t, nil)
	tun.config.KeyFile = writeTestKey(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	tun.config.SSHHost = listener.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if result := TestConnection(ctx, tun.config); result.OK || result.Error == "" {
		t.Fatalf("cancelled: %+v", result)
	}
	awaitSignal(t, done, "cancelled handshake leaked")
}
