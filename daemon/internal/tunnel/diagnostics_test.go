package tunnel

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/byteporter/portway/internal/config"
)

func diagnosticTunnel(t *testing.T, server *relayTestServer, mode string) *Tunnel {
	t.Helper()
	tun := newTestTunnel(t, nil)
	tun.config.Mode = mode
	tun.config.KeyFile = writeTestKey(t)
	tun.config.SSHHost = server.addr
	tun.config.HostKeyCheck = config.HostKeyCheckInsecure
	tun.config.LocalHost = "127.0.0.1"
	t.Cleanup(tun.Stop)
	return tun
}

func diagnosticCheck(t *testing.T, result TunnelDiagnostic, stage, status string) DiagnosticCheck {
	t.Helper()
	for _, check := range result.Checks {
		if check.Stage == stage {
			if check.Status != status {
				t.Fatalf("%s = %+v, want %s; report: %+v", stage, check, status, result)
			}
			return check
		}
	}
	t.Fatalf("missing stage %s: %+v", stage, result)
	return DiagnosticCheck{}
}

func diagnosticTarget(t *testing.T) (net.Listener, <-chan int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	data := make(chan int, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := io.Copy(io.Discard, conn)
		data <- int(n)
	}()
	return listener, data
}

func TestTunnelDiagnosticChecksTargetThroughSSHWithoutStarting(t *testing.T) {
	server := newRelayTestServer(t)
	tun := diagnosticTunnel(t, server, "local")
	target, received := diagnosticTarget(t)
	tun.config.RemoteHost = "127.0.0.1"
	tun.config.RemotePort = target.Addr().(*net.TCPAddr).Port
	before := tun.Status()
	result := tun.Diagnose(context.Background())
	if !result.OK || tun.Status() != before || tun.forwarder != nil {
		t.Fatalf("diagnosis changed tunnel state or failed: %+v", result)
	}
	diagnosticCheck(t, result, "local_listener", "ok")
	diagnosticCheck(t, result, "ssh", "ok")
	diagnosticCheck(t, result, "target", "ok")
	if server.direct.Load() != 1 {
		t.Fatal("target was not probed through SSH")
	}
	select {
	case count := <-received:
		if count != 0 {
			t.Fatal("diagnostic sent application data")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("target probe did not close")
	}
}

func TestTunnelDiagnosticSeparatesSSHSuccessFromTargetFailure(t *testing.T) {
	server := newRelayTestServer(t)
	tun := diagnosticTunnel(t, server, "local")
	target, _ := diagnosticTarget(t)
	tun.config.RemoteHost = "127.0.0.1"
	tun.config.RemotePort = target.Addr().(*net.TCPAddr).Port
	_ = target.Close()
	result := tun.Diagnose(context.Background())
	if result.OK {
		t.Fatal("unavailable target reported healthy")
	}
	diagnosticCheck(t, result, "ssh", "ok")
	if diagnosticCheck(t, result, "target", "failed").Error == "" {
		t.Fatal("missing target failure detail")
	}
}

func TestTunnelDiagnosticDetectsPortConflictAndSkipsDynamicTarget(t *testing.T) {
	server := newRelayTestServer(t)
	tun := diagnosticTunnel(t, server, "dynamic")
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	tun.config.LocalPort = occupied.Addr().(*net.TCPAddr).Port
	result := tun.Diagnose(context.Background())
	if result.OK {
		t.Fatal("occupied local port reported healthy")
	}
	diagnosticCheck(t, result, "local_listener", "failed")
	diagnosticCheck(t, result, "ssh", "ok")
	diagnosticCheck(t, result, "target", "skipped")
	if server.direct.Load() != 0 {
		t.Fatal("dynamic diagnostics opened an unspecified target")
	}
	_ = occupied.Close()
	result = tun.Diagnose(context.Background())
	if !result.OK {
		t.Fatalf("free dynamic listener failed: %+v", result)
	}
	listener, err := net.Listen("tcp", result.Checks[0].Address)
	if err != nil {
		t.Fatalf("diagnostic retained the port: %v", err)
	}
	_ = listener.Close()
}

func TestTunnelDiagnosticPreservesRunningConnection(t *testing.T) {
	server := newRelayTestServer(t)
	tun := diagnosticTunnel(t, server, "dynamic")
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	before := awaitState(t, tun, StateConnected)
	tun.mu.Lock()
	client := tun.sshClient
	tun.mu.Unlock()
	result := tun.Diagnose(context.Background())
	if !result.OK || tun.Status() != before {
		t.Fatalf("running tunnel was disturbed: %+v, %+v", result, tun.Status())
	}
	diagnosticCheck(t, result, "local_listener", "ok")
	tun.mu.Lock()
	unchanged := tun.sshClient == client
	tun.mu.Unlock()
	if !unchanged {
		t.Fatal("diagnostic replaced the active SSH transport")
	}
	conn, err := net.DialTimeout("tcp", tunnelListenAddr(t, tun), time.Second)
	if err != nil {
		t.Fatalf("active listener was closed: %v", err)
	}
	_ = conn.Close()
}

func TestTunnelDiagnosticReverseModeChecksLocalTarget(t *testing.T) {
	server := newRelayTestServer(t)
	tun := diagnosticTunnel(t, server, "remote")
	target, _ := diagnosticTarget(t)
	tun.config.LocalPort = target.Addr().(*net.TCPAddr).Port
	result := tun.Diagnose(context.Background())
	if !result.OK {
		t.Fatalf("reverse target failed: %+v", result)
	}
	diagnosticCheck(t, result, "remote_listener", "skipped")
	check := diagnosticCheck(t, result, "target", "ok")
	if check.Address != target.Addr().String() || server.direct.Load() != 0 {
		t.Fatal("reverse target was not checked on the local machine")
	}
}

func TestTunnelDiagnosticCancellationReleasesSSHHandshake(t *testing.T) {
	tun := newTestTunnel(t, nil)
	tun.config.KeyFile = writeTestKey(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
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
	result := tun.Diagnose(ctx)
	if result.OK {
		t.Fatal("cancelled diagnosis succeeded")
	}
	diagnosticCheck(t, result, "ssh", "failed")
	diagnosticCheck(t, result, "target", "skipped")
	awaitSignal(t, done, "cancelled diagnostic handshake leaked")
}

func TestTargetDiagnosticCancellationReleasesPendingChannel(t *testing.T) {
	client := &pendingDialSSHConn{fakeSSHConn: newFakeSSHConn(), started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var check DiagnosticCheck
	go func() {
		defer close(done)
		check = diagnoseTarget(ctx, client, "127.0.0.1:5432")
	}()
	awaitSignal(t, client.started, "target probe did not start")
	cancel()
	awaitSignal(t, done, "cancelled target channel leaked")
	if check.Status != "failed" || !strings.Contains(check.Error, "canceled") {
		t.Fatalf("cancellation result: %+v", check)
	}
}
