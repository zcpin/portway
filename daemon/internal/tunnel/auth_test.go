package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/byteporter/portway/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

func authTestKey(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return private, signer
}

type authTestServer struct {
	addr        string
	mu          sync.Mutex
	signer      ssh.Signer
	connections []net.Conn
}

func newAuthTestServer(t *testing.T, signer ssh.Signer, allowed ssh.PublicKey) *authTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &authTestServer{addr: listener.Addr().String(), signer: signer}
	var wg sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.mu.Lock()
			server.connections = append(server.connections, conn)
			hostKey := server.signer
			server.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				options := &ssh.ServerConfig{NoClientAuth: allowed == nil}
				if allowed != nil {
					options.PublicKeyCallback = func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
						if bytes.Equal(key.Marshal(), allowed.Marshal()) {
							return nil, nil
						}
						return nil, errors.New("unexpected client key")
					}
				}
				options.AddHostKey(hostKey)
				sshConn, channels, requests, err := ssh.NewServerConn(conn, options)
				if err != nil {
					return
				}
				defer sshConn.Close()
				go ssh.DiscardRequests(requests)
				go func() {
					for channel := range channels {
						channel.Reject(ssh.Prohibited, "test only")
					}
				}()
				_ = sshConn.Wait()
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-acceptDone
		server.mu.Lock()
		for _, conn := range server.connections {
			conn.Close()
		}
		server.mu.Unlock()
		wg.Wait()
	})
	return server
}

func TestEncryptedKeyUnlockLockAndFileChange(t *testing.T) {
	private, signer := authTestKey(t)
	passphrase := []byte("test-only-passphrase")
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "fixture", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(block)
	path := filepath.Join(t.TempDir(), "encrypted.key")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewKeyStore()
	if encrypted, unlocked := store.KeyStatus(path); !encrypted || unlocked {
		t.Fatal("encrypted key reported as unlocked")
	}
	if _, err := store.Load(path); err == nil {
		t.Fatal("locked key was loaded")
	}
	if err := store.Unlock(path, []byte("wrong-secret")); err == nil || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatal("incorrect password was accepted or exposed")
	}
	if err := store.Unlock(path, passphrase); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(path)
	if err != nil || !bytes.Equal(loaded.PublicKey().Marshal(), signer.PublicKey().Marshal()) {
		t.Fatalf("unlocked key: %v", err)
	}
	if encrypted, unlocked := store.KeyStatus(path); !encrypted || !unlocked {
		t.Fatal("unlock state missing")
	}
	if current, _ := os.ReadFile(path); !bytes.Equal(current, data) {
		t.Fatal("unlock modified the key file")
	}
	store.Lock(path)
	if _, err := store.Load(path); err == nil {
		t.Fatal("lock did not clear signer")
	}
	if err := store.Unlock(path, passphrase); err != nil {
		t.Fatal(err)
	}
	other, _ := authTestKey(t)
	block, err = ssh.MarshalPrivateKeyWithPassphrase(other, "fixture", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(path); err == nil {
		t.Fatal("changed file reused old unlocked signer")
	}
	if err := store.Unlock(path, passphrase); err != nil {
		t.Fatal(err)
	}
	store.Clear()
	if _, err := store.Load(path); err == nil {
		t.Fatal("clear retained unlocked signer")
	}
}

func TestAgentAuthenticationAndSocketCleanup(t *testing.T) {
	private, signer := authTestKey(t)
	server := newAuthTestServer(t, signer, signer.PublicKey())
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	tun := newTestTunnel(t, nil)
	tun.config.SSHHost, tun.config.AuthMethod, tun.config.KeyFile = server.addr, "agent", ""
	tun.config.HostKeyCheck = config.HostKeyCheckInsecure
	left, right := net.Pipe()
	t.Cleanup(func() { left.Close(); right.Close() })
	agentDone := make(chan struct{})
	go func() { defer close(agentDone); _ = agent.ServeAgent(ring, right) }()
	tun.agentDial = func(context.Context, string) (net.Conn, error) { return left, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := tun.createSSHConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	awaitSignal(t, agentDone, "agent socket remained open after authentication")
}

func TestUnresponsiveAgentIsCancelled(t *testing.T) {
	_, signer := authTestKey(t)
	server := newAuthTestServer(t, signer, signer.PublicKey())
	tun := newTestTunnel(t, nil)
	tun.config.SSHHost, tun.config.AuthMethod = server.addr, "agent"
	tun.config.HostKeyCheck = config.HostKeyCheckInsecure
	tun.connectTimeout = 100 * time.Millisecond
	left, right := net.Pipe()
	t.Cleanup(func() { left.Close(); right.Close() })
	tun.agentDial = func(context.Context, string) (net.Conn, error) { return left, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := tun.createSSHConnection(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unresponsive agent: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("agent only stopped at outer test timeout")
	}
}

func TestHostKeyTrustRequiresConfirmationAndPreservesOtherHosts(t *testing.T) {
	for _, hashed := range []bool{false, true} {
		_, oldSigner := authTestKey(t)
		_, newSigner := authTestKey(t)
		server := newAuthTestServer(t, oldSigner, nil)
		cfg := config.ParsedTunnel{SSHHost: server.addr, SSHUser: "test", KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts")}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		info, err := InspectHostKey(ctx, cfg)
		if err != nil || info.Known || info.Changed {
			t.Fatalf("inspection: %+v %v", info, err)
		}
		if _, err := os.Stat(cfg.KnownHostsFile); !os.IsNotExist(err) {
			t.Fatal("inspection wrote trust data")
		}
		if _, err := TrustHostKey(ctx, cfg, "wrong-fingerprint", false); err == nil {
			t.Fatal("unconfirmed fingerprint was trusted")
		}
		if _, err := TrustHostKey(ctx, cfg, info.Fingerprint, false); err != nil {
			t.Fatal(err)
		}
		hosts := []string{server.addr, "other.example:22"}
		original := knownhosts.Line(hosts, oldSigner.PublicKey()) + "\n"
		if hashed {
			original = knownhosts.HashHostname(knownhosts.Normalize(server.addr)) + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(oldSigner.PublicKey()))) + "\n" + knownhosts.Line([]string{"other.example:22"}, oldSigner.PublicKey()) + "\n"
		}
		if err := os.WriteFile(cfg.KnownHostsFile, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		server.mu.Lock()
		server.signer = newSigner
		server.mu.Unlock()
		changed, err := InspectHostKey(ctx, cfg)
		if err != nil || !changed.Changed || changed.Known {
			t.Fatalf("rotation: %+v %v", changed, err)
		}
		if _, err := TrustHostKey(ctx, cfg, info.Fingerprint, true); err == nil {
			t.Fatal("stale fingerprint was accepted")
		}
		if _, err := TrustHostKey(ctx, cfg, changed.Fingerprint, false); err == nil {
			t.Fatal("changed host was silently replaced")
		}
		if data, _ := os.ReadFile(cfg.KnownHostsFile); string(data) != original {
			t.Fatal("rejected confirmation modified trust file")
		}
		if _, err := TrustHostKey(ctx, cfg, changed.Fingerprint, true); err != nil {
			t.Fatal(err)
		}
		check, err := knownhosts.New(cfg.KnownHostsFile)
		if err != nil {
			t.Fatal(err)
		}
		remote, err := net.ResolveTCPAddr("tcp", server.addr)
		if err != nil {
			t.Fatal(err)
		}
		if err := check(server.addr, remote, newSigner.PublicKey()); err != nil {
			t.Fatalf("new host rejected: %v", err)
		}
		if err := check(server.addr, remote, oldSigner.PublicKey()); err == nil {
			t.Fatal("replaced host key still trusted")
		}
		if err := check("other.example:22", &net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 22}, oldSigner.PublicKey()); err != nil {
			t.Fatalf("other host lost trust: %v", err)
		}
	}
}
