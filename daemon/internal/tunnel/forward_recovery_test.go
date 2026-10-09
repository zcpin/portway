package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The first session can stop answering channel opens while still replying to
// keepalives. Replacement sessions continue forwarding normally.
type channelRecoveryServer struct {
	addr      string
	sessions  atomic.Int32
	stall     atomic.Bool
	reject    atomic.Bool
	stalled   chan struct{}
	heartbeat chan struct{}
}

func newChannelRecoveryServer(t *testing.T) *channelRecoveryServer {
	t.Helper()
	_, signer := authTestKey(t)
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &channelRecoveryServer{
		addr: listener.Addr().String(), stalled: make(chan struct{}, 1), heartbeat: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stopClose()
				transport, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer transport.Close()
				session := server.sessions.Add(1)
				var stalled atomic.Bool
				wg.Add(1)
				go func() {
					defer wg.Done()
					for request := range requests {
						if err := request.Reply(true, nil); err == nil && stalled.Load() {
							select {
							case server.heartbeat <- struct{}{}:
							default:
							}
						}
					}
				}()
				for incoming := range channels {
					if incoming.ChannelType() != "direct-tcpip" {
						_ = incoming.Reject(ssh.UnknownChannelType, "unsupported channel")
						continue
					}
					if server.reject.Load() {
						_ = incoming.Reject(ssh.ConnectionFailed, "target unavailable")
						continue
					}
					if session == 1 && server.stall.Load() {
						stalled.Store(true)
						select {
						case server.stalled <- struct{}{}:
						default:
						}
						continue // Neither accept nor reject this channel.
					}
					channel, channelRequests, err := incoming.Accept()
					if err != nil {
						continue
					}
					wg.Add(2)
					go func() { defer wg.Done(); ssh.DiscardRequests(channelRequests) }()
					go func() {
						defer wg.Done()
						defer channel.Close()
						_, _ = io.Copy(channel, channel)
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); wg.Wait() })
	return server
}

type shortChannelTimeoutClient struct{ *ssh.Client }

func (c *shortChannelTimeoutClient) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	return c.Client.DialContext(ctx, network, addr)
}

func newChannelRecoveryTunnel(t *testing.T, server *channelRecoveryServer) *Tunnel {
	t.Helper()
	// Use a fixed local port so reconnection must make the original endpoint usable.
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tun := newTestTunnel(t, nil)
	tun.config.LocalPort = port.Addr().(*net.TCPAddr).Port
	_ = port.Close()
	tun.config.SSHHost = server.addr
	tun.config.KeyFile = writeTestKey(t)
	tun.keepAliveInterval = 10 * time.Millisecond
	tun.keepAliveTimeout = time.Second
	tun.dial = func(ctx context.Context) (sshConn, error) {
		client, err := tun.createSSHConnection(ctx)
		if err != nil {
			return nil, err
		}
		return &shortChannelTimeoutClient{Client: client.(*ssh.Client)}, nil
	}
	t.Cleanup(tun.Stop)
	return tun
}

func recoveryClient(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func assertRecoveryEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("forwarding did not recover: %v", err)
	}
	if string(response) != "ping" {
		t.Fatalf("unexpected forwarded data: %q", response)
	}
}

func TestTunnelReconnectsAfterSSHChannelTimeout(t *testing.T) {
	entries := captureConnectionLogs(t)
	server := newChannelRecoveryServer(t)
	tun := newChannelRecoveryTunnel(t, server)
	reconnecting, resume := make(chan struct{}), make(chan struct{})
	dial := tun.dial
	attempts := 0
	tun.dial = func(ctx context.Context) (sshConn, error) {
		attempts++
		if attempts == 2 {
			close(reconnecting)
			select {
			case <-resume:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return dial(ctx)
	}
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	addr := tunnelListenAddr(t, tun)
	first := recoveryClient(t, addr)
	assertRecoveryEcho(t, first)

	server.stall.Store(true)
	blocked := recoveryClient(t, addr)
	awaitSignal(t, server.stalled, "the SSH channel open did not reach the server")
	awaitSignal(t, server.heartbeat, "keepalive stopped working with the blocked channel")
	awaitSignal(t, reconnecting, "SSH channel timed out but the tunnel kept the unusable SSH session")
	status := tun.Status()
	if status.State != StateReconnecting || !strings.Contains(status.LastError, context.DeadlineExceeded.Error()) {
		t.Fatalf("channel failure did not update tunnel status: %+v", status)
	}
	awaitConnectionLog(t, entries, "warn", "Connection lost", "127.0.0.1:3306", context.DeadlineExceeded.Error())
	_ = blocked.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := blocked.Read(make([]byte, 1)); err == nil {
		t.Fatal("failed forwarding connection remained usable")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("failed forwarding connection was not closed")
	}

	close(resume)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for tun.Status().State != StateConnected {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("tunnel did not reconnect")
		}
	}
	if server.sessions.Load() != 2 {
		t.Fatalf("expected a replacement SSH session, got %d sessions", server.sessions.Load())
	}
	if restored := tunnelListenAddr(t, tun); restored != addr {
		t.Fatalf("local endpoint changed on reconnection: %s -> %s", addr, restored)
	}
	assertRecoveryEcho(t, recoveryClient(t, addr))
}

func TestTunnelKeepsSSHSessionAfterTargetRejectsConnection(t *testing.T) {
	entries := captureConnectionLogs(t)
	server := newChannelRecoveryServer(t)
	tun := newChannelRecoveryTunnel(t, server)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	addr := tunnelListenAddr(t, tun)
	established := recoveryClient(t, addr)
	assertRecoveryEcho(t, established)

	server.reject.Store(true)
	rejected := recoveryClient(t, addr)
	_ = rejected.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := rejected.Read(make([]byte, 1)); err == nil {
		t.Fatal("target rejection did not close the incoming connection")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("target rejection left the incoming connection hanging")
	}
	awaitConnectionLog(t, entries, "error", "forwarding failed", "target unavailable")
	// Give the run loop time to react, then verify existing traffic is unaffected.
	time.Sleep(100 * time.Millisecond)
	assertRecoveryEcho(t, established)
	server.reject.Store(false)
	assertRecoveryEcho(t, recoveryClient(t, addr))
	if server.sessions.Load() != 1 || tun.Status().State != StateConnected {
		t.Fatalf("target rejection unnecessarily restarted SSH: sessions=%d status=%+v", server.sessions.Load(), tun.Status())
	}
}

type failedChannelClient struct {
	*fakeSSHConn
	err error
}

func (c *failedChannelClient) Dial(string, string) (net.Conn, error) {
	return nil, c.err
}

func TestForwarderSSHFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		stopped    bool
		diagnostic bool
		wantFailed bool
	}{
		{name: "channel_timeout", err: context.DeadlineExceeded, wantFailed: true},
		{name: "transport_eof", err: io.EOF, wantFailed: true},
		{name: "transport_closed", err: net.ErrClosed, wantFailed: true},
		{name: "target_refused", err: &ssh.OpenChannelError{Reason: ssh.ConnectionFailed, Message: "connection refused"}},
		{name: "forwarding_prohibited", err: fmt.Errorf("open: %w", &ssh.OpenChannelError{Reason: ssh.Prohibited})},
		{name: "cancelled", err: context.Canceled},
		{name: "stopped", err: io.EOF, stopped: true},
		{name: "diagnostic_timeout", err: context.DeadlineExceeded, diagnostic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &failedChannelClient{fakeSSHConn: newFakeSSHConn(), err: tc.err}
			f := &Forwarder{sshClient: client}
			if !tc.diagnostic {
				f = NewForwarder(tc.name, "127.0.0.1", "0", "127.0.0.1", "3306", client)
				t.Cleanup(f.Stop)
				if tc.stopped {
					f.Stop()
				}
			}
			returned := make(chan error, 1)
			go func() {
				// A full notification channel or a diagnostic's nil channel must
				// never hold up another failed forwarding connection.
				_, _ = f.dialSSH(context.Background(), "127.0.0.1:3306")
				_, err := f.dialSSH(context.Background(), "127.0.0.1:3306")
				returned <- err
			}()
			select {
			case err := <-returned:
				if !errors.Is(err, tc.err) {
					t.Fatalf("dial lost the original error: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("SSH failure notification blocked the dial")
			}
			select {
			case err := <-f.Failure():
				if !tc.wantFailed || !errors.Is(err, tc.err) {
					t.Fatalf("unexpected SSH recovery notification: %v", err)
				}
			default:
				if tc.wantFailed {
					t.Fatal("SSH channel failure was not reported to the tunnel")
				}
			}
		})
	}
}
