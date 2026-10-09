package tunnel

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/byteporter/portway/internal/logger"
)

type connectionLog struct {
	level   string
	message string
}

func captureConnectionLogs(t *testing.T) <-chan connectionLog {
	t.Helper()
	entries := make(chan connectionLog, 64)
	logger.GetGlobalLogger().SetLevel(logger.INFO)
	logger.SetGlobalLogHook(func(level, message string) {
		select {
		case entries <- connectionLog{level: level, message: message}:
		default:
		}
	})
	t.Cleanup(func() { logger.SetGlobalLogHook(nil) })
	return entries
}

func awaitConnectionLog(t *testing.T, entries <-chan connectionLog, level string, fragments ...string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case entry := <-entries:
			matches := entry.level == level
			for _, fragment := range fragments {
				matches = matches && strings.Contains(entry.message, fragment)
			}
			if matches {
				return
			}
		case <-timer.C:
			t.Fatalf("missing %s connection log containing %q", level, fragments)
		}
	}
}

func TestForwarderLogsTargetEOFAndLateWriteFailure(t *testing.T) {
	for _, lateWrite := range []bool{false, true} {
		name := "target_eof"
		if lateWrite {
			name = "write_failure_after_target_eof"
		}
		t.Run(name, func(t *testing.T) {
			entries := captureConnectionLogs(t)
			remoteClient, remoteServer := net.Pipe()
			client := &pipeSSHConn{fakeSSHConn: newFakeSSHConn(), remote: remoteClient}
			f := NewForwarder("connection-logs", "127.0.0.1", "0", "127.0.0.1", "3306", client)
			t.Cleanup(func() { remoteClient.Close(); remoteServer.Close(); f.Stop() })
			if err := f.Start(); err != nil {
				t.Fatal(err)
			}
			local, err := net.DialTimeout("tcp", f.listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { local.Close() })
			_ = local.SetDeadline(time.Now().Add(3 * time.Second))
			_ = remoteServer.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := local.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			payload := make([]byte, 4)
			if _, err := io.ReadFull(remoteServer, payload); err != nil {
				t.Fatal(err)
			}
			_ = remoteServer.Close()
			if _, err := local.Read(payload); err != io.EOF {
				t.Fatalf("target close should reach the client as EOF, got %v", err)
			}
			if lateWrite {
				// The first copy already ended normally. A failure in the other
				// direction must still be reported, rather than discarded.
				if _, err := local.Write([]byte("next")); err != nil {
					t.Fatal(err)
				}
				awaitConnectionLog(t, entries, "warn", "[connection-logs]", "local->remote", io.ErrClosedPipe.Error())
			} else {
				awaitConnectionLog(t, entries, "info", "[connection-logs]", "remote->local", "EOF")
			}
			select {
			case <-f.Done():
				t.Fatal("a single target connection closing stopped the listener")
			default:
			}
		})
	}
}

func TestTunnelDisconnectLogPreservesTransportError(t *testing.T) {
	entries := captureConnectionLogs(t)
	dialed := make(chan *fakeSSHConn, 8)
	tun := newTestTunnel(t, dialed)
	t.Cleanup(tun.Stop)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	first := waitConn(t, dialed)
	_ = first.Close()
	_ = waitConn(t, dialed)
	awaitConnectionLog(t, entries, "warn", "Connection lost", "fake: connection closed")
}

func TestTunnelKeepAliveFailureIsLoggedAtInfoLevel(t *testing.T) {
	entries := captureConnectionLogs(t)
	dialed := make(chan *fakeSSHConn, 8)
	tun := newTestTunnel(t, dialed)
	tun.keepAliveInterval = 10 * time.Millisecond
	t.Cleanup(tun.Stop)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	first := waitConn(t, dialed)
	first.failReq.Store(true)
	_ = waitConn(t, dialed)
	awaitConnectionLog(t, entries, "warn", "SSH keepalive failed", "fake: keepalive failed")
}
