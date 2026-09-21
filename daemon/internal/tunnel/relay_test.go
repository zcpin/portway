package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/byteporter/portway/internal/config"
	"golang.org/x/crypto/ssh"
)

// relayTestServer implements OpenSSH's forwarding protocol over real SSH sockets.
type relayTestServer struct {
	addr   string
	direct atomic.Int32
	active atomic.Int32
	mu     sync.Mutex
	closed bool
	owned  []io.Closer
	wg     sync.WaitGroup
}

func (s *relayTestServer) own(closer io.Closer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		closer.Close()
	} else {
		s.owned = append(s.owned, closer)
	}
}

func (s *relayTestServer) worker(fn func()) { s.wg.Add(1); go func() { defer s.wg.Done(); fn() }() }

func relayPair(a, b io.ReadWriteCloser) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	copyHalf := func(dst, src io.ReadWriteCloser) {
		_, err := io.Copy(dst, src)
		if err != nil {
			a.Close()
			b.Close()
		}
		if writer, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = writer.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	<-done
	<-done
}

func newRelayTestServer(t *testing.T) *relayTestServer {
	t.Helper()
	_, signer := authTestKey(t)
	options := &ssh.ServerConfig{NoClientAuth: true}
	options.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &relayTestServer{addr: listener.Addr().String()}
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.own(conn)
			s.worker(func() {
				defer conn.Close()
				transport, channels, requests, err := ssh.NewServerConn(conn, options)
				if err != nil {
					return
				}
				s.own(transport)
				s.active.Add(1)
				defer s.active.Add(-1)
				defer transport.Close()
				listeners := make(map[string]net.Listener)
				defer func() {
					for _, listener := range listeners {
						listener.Close()
					}
				}()
				for channels != nil || requests != nil {
					select {
					case channel, ok := <-channels:
						if !ok {
							channels = nil
							continue
						}
						if channel.ChannelType() != "direct-tcpip" {
							channel.Reject(ssh.UnknownChannelType, "unsupported")
							continue
						}
						s.direct.Add(1)
						s.worker(func() {
							var target struct {
								Host       string
								Port       uint32
								Origin     string
								OriginPort uint32
							}
							if err := ssh.Unmarshal(channel.ExtraData(), &target); err != nil {
								channel.Reject(ssh.ConnectionFailed, err.Error())
								return
							}
							out, err := net.DialTimeout("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))), time.Second)
							if err != nil {
								channel.Reject(ssh.ConnectionFailed, err.Error())
								return
							}
							s.own(out)
							incoming, reqs, err := channel.Accept()
							if err != nil {
								out.Close()
								return
							}
							s.own(incoming)
							go ssh.DiscardRequests(reqs)
							relayPair(incoming, out)
						})
					case request, ok := <-requests:
						if !ok {
							requests = nil
							continue
						}
						var bind struct {
							Host string
							Port uint32
						}
						if err := ssh.Unmarshal(request.Payload, &bind); err != nil {
							request.Reply(false, nil)
							continue
						}
						address := net.JoinHostPort(bind.Host, strconv.Itoa(int(bind.Port)))
						if request.Type == "cancel-tcpip-forward" {
							if l := listeners[address]; l != nil {
								l.Close()
							}
							request.Reply(true, nil)
							continue
						}
						if request.Type != "tcpip-forward" {
							request.Reply(false, nil)
							continue
						}
						remote, err := net.Listen("tcp", address)
						if err != nil {
							request.Reply(false, nil)
							continue
						}
						s.own(remote)
						port := uint32(remote.Addr().(*net.TCPAddr).Port)
						listeners[net.JoinHostPort(bind.Host, strconv.Itoa(int(port)))] = remote
						var reply []byte
						if bind.Port == 0 {
							reply = ssh.Marshal(struct{ Port uint32 }{port})
						}
						request.Reply(true, reply)
						s.worker(func() {
							for {
								incoming, err := remote.Accept()
								if err != nil {
									return
								}
								s.own(incoming)
								s.worker(func() {
									origin := incoming.RemoteAddr().(*net.TCPAddr)
									payload := ssh.Marshal(struct {
										Host       string
										Port       uint32
										Origin     string
										OriginPort uint32
									}{bind.Host, port, origin.IP.String(), uint32(origin.Port)})
									channel, reqs, err := transport.OpenChannel("forwarded-tcpip", payload)
									if err != nil {
										incoming.Close()
										return
									}
									s.own(channel)
									go ssh.DiscardRequests(reqs)
									relayPair(channel, incoming)
								})
							}
						})
					}
				}
			})
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-accepted
		s.mu.Lock()
		s.closed = true
		owned := append([]io.Closer(nil), s.owned...)
		s.mu.Unlock()
		for _, closer := range owned {
			closer.Close()
		}
		s.wg.Wait()
	})
	return s
}

func halfCloseEcho(t *testing.T) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var ownedMu sync.Mutex
	var owned []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		ownedMu.Lock()
		owned = append(owned, conn)
		ownedMu.Unlock()
		defer conn.Close()
		data, err := io.ReadAll(conn)
		if err == nil {
			_, _ = conn.Write(append([]byte("echo:"), data...))
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		ownedMu.Lock()
		for _, conn := range owned {
			conn.Close()
		}
		ownedMu.Unlock()
		<-done
	})
	return "127.0.0.1", listener.Addr().(*net.TCPAddr).Port
}

func exchangeHalfClosed(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil || string(data) != "echo:hello" {
		t.Fatalf("half-close transfer: %q %v", data, err)
	}
}

func TestRealSSHJumpChainTransfersAndClosesAllHops(t *testing.T) {
	first, second, target := newRelayTestServer(t), newRelayTestServer(t), newRelayTestServer(t)
	host, port := halfCloseEcho(t)
	key := writeTestKey(t)
	cfg := config.ParsedTunnel{Name: "chain", LocalPort: 0, RemoteHost: host, RemotePort: port,
		SSHHost: target.addr, SSHUser: "test", KeyFile: key, HostKeyCheck: config.HostKeyCheckInsecure,
		ReconnectStrategy: config.StrategyFixed, ReconnectInterval: time.Second,
		Jumps: []config.SSHHop{{Name: "first", Host: first.addr, User: "test", KeyFile: key, HostKeyCheck: config.HostKeyCheckInsecure},
			{Name: "second", Host: second.addr, User: "test", KeyFile: key, HostKeyCheck: config.HostKeyCheckInsecure}}}
	scanConfig := cfg
	scanConfig.KeyFile = "" // The target's private credentials must not be read during inspection.
	scanConfig.KnownHostsFile = filepath.Join(t.TempDir(), "known_hosts")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if info, err := InspectHostKey(ctx, scanConfig); err != nil || info.Host != target.addr || info.Fingerprint == "" {
		t.Fatalf("host scan through jumps: %+v %v", info, err)
	}
	tun, err := NewTunnel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tun.Stop)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	exchangeHalfClosed(t, tunnelListenAddr(t, tun))
	if first.direct.Load() != 2 || second.direct.Load() != 2 || target.direct.Load() != 1 {
		t.Fatal("data bypassed a jump")
	}
	tun.Stop()
	deadline := time.After(2 * time.Second)
	for first.active.Load()+second.active.Load()+target.active.Load() != 0 {
		select {
		case <-deadline:
			t.Fatal("SSH hop leaked after stop")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRealSSHReverseForwardingPreservesHalfClose(t *testing.T) {
	server := newRelayTestServer(t)
	host, port := halfCloseEcho(t)
	cfg := config.ParsedTunnel{Name: "reverse", Mode: "remote", LocalHost: host, LocalPort: port, RemoteHost: "127.0.0.1", RemotePort: 0,
		SSHHost: server.addr, SSHUser: "test", KeyFile: writeTestKey(t), HostKeyCheck: config.HostKeyCheckInsecure,
		ReconnectStrategy: config.StrategyFixed, ReconnectInterval: time.Second}
	tun, err := NewTunnel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tun.Stop)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	exchangeHalfClosed(t, tunnelListenAddr(t, tun))
}

func TestJumpFailureReleasesEstablishedHop(t *testing.T) {
	server := newRelayTestServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	blocked := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(blocked)
		_, _ = io.Copy(io.Discard, conn)
	}()
	cfg := config.ParsedTunnel{Name: "blocked", SSHHost: listener.Addr().String(), SSHUser: "test", KeyFile: writeTestKey(t),
		HostKeyCheck: config.HostKeyCheckInsecure, ReconnectStrategy: config.StrategyFixed, ReconnectInterval: time.Second,
		Jumps: []config.SSHHop{{Name: "jump", Host: server.addr, User: "test", KeyFile: writeTestKey(t), HostKeyCheck: config.HostKeyCheckInsecure}}}
	tun, err := NewTunnel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := tun.createSSHConnection(ctx); err == nil {
		t.Fatal("unresponsive target accepted")
	}
	awaitSignal(t, blocked, "target was not reached through jump")
	deadline := time.After(2 * time.Second)
	for server.active.Load() != 0 {
		select {
		case <-deadline:
			t.Fatal(fmt.Sprintf("hop remains active: %d", server.active.Load()))
		case <-time.After(time.Millisecond):
		}
	}
}
