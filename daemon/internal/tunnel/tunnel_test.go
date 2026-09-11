package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/config"
)

// TestStartStopConcurrent 并发地 Start/Stop，验证不会出现
// cancel/forwarder/sshClient 的无锁读写（历史 bug：Stop 可能读到 nil cancel）。
func TestStartStopConcurrent(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("not a real key"), 0600); err != nil {
		t.Fatalf("写入占位私钥失败: %v", err)
	}

	tun, err := NewTunnel(config.ParsedTunnel{
		Name:                 "race-test",
		LocalPort:            15999,
		RemoteHost:           "127.0.0.1",
		RemotePort:           3306,
		SSHHost:              "127.0.0.1:1",
		SSHUser:              "nobody",
		KeyFile:              keyPath,
		ReconnectStrategy:    config.StrategyFixed,
		ReconnectInterval:    10 * time.Millisecond,
		MaxReconnectAttempts: 1,
	})
	if err != nil {
		t.Fatalf("NewTunnel() error = %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = tun.Start() // 已运行时返回错误，属预期
		}()
		go func() {
			defer wg.Done()
			tun.Stop()
		}()
	}
	wg.Wait()

	// 清理：最后一轮可能是 Start 胜出
	tun.Stop()
	if tun.IsRunning() {
		t.Fatal("Stop 之后 IsRunning() 仍为 true")
	}
}

// ---------- 掉线重连 ----------

// fakeSSHConn 是 sshConn 的测试替身：Close 后 Wait 返回，
// SendRequest 可按需失败以模拟 keepalive 探测不到对端。
type fakeSSHConn struct {
	closed    chan struct{}
	closeOnce sync.Once
	failReq   atomic.Bool
}

func newFakeSSHConn() *fakeSSHConn {
	return &fakeSSHConn{closed: make(chan struct{})}
}

func (c *fakeSSHConn) Dial(network, addr string) (net.Conn, error) {
	return nil, errors.New("fake: Dial not implemented")
}

func (c *fakeSSHConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeSSHConn) Wait() error {
	<-c.closed
	return errors.New("fake: connection closed")
}

func (c *fakeSSHConn) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	if c.failReq.Load() {
		return false, nil, errors.New("fake: keepalive failed")
	}
	return true, nil, nil
}

// newTestTunnel 返回一个拨号被替换为 fake 的隧道；
// 每次建立连接都会把连接发到 dialed。
func newTestTunnel(t *testing.T, dialed chan<- *fakeSSHConn) *Tunnel {
	t.Helper()
	tun, err := NewTunnel(config.ParsedTunnel{
		Name:                 "reconnect-test",
		LocalPort:            0, // 由系统分配本地端口，避免测试间端口冲突
		RemoteHost:           "127.0.0.1",
		RemotePort:           3306,
		SSHHost:              "127.0.0.1:1",
		SSHUser:              "nobody",
		KeyFile:              "unused-in-fake-dial",
		ReconnectStrategy:    config.StrategyFixed,
		ReconnectInterval:    10 * time.Millisecond,
		MaxReconnectAttempts: 0,
	})
	if err != nil {
		t.Fatalf("NewTunnel() error = %v", err)
	}
	tun.dial = func(ctx context.Context) (sshConn, error) {
		c := newFakeSSHConn()
		select {
		case dialed <- c:
		default:
		}
		return c, nil
	}
	return tun
}

func waitConn(t *testing.T, ch <-chan *fakeSSHConn) *fakeSSHConn {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("等待 SSH 连接超时")
		return nil
	}
}

// TestTunnelReconnectsAfterConnectionDrop 验证连接建立后掉线会触发重连
// （历史 bug：连接成功后永远阻塞在 ctx.Done，掉线不再重连）。
func TestTunnelReconnectsAfterConnectionDrop(t *testing.T) {
	dialed := make(chan *fakeSSHConn, 8)
	tun := newTestTunnel(t, dialed)
	t.Cleanup(tun.Stop)

	if err := tun.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	first := waitConn(t, dialed)

	// 模拟 SSH 连接断开
	first.Close()

	second := waitConn(t, dialed)
	if second == first {
		t.Fatal("重连后应使用新的连接")
	}
}

// TestTunnelKeepAliveFailureTriggersReconnect 验证 keepalive 失败会关闭
// 僵死连接并重连。
func TestTunnelKeepAliveFailureTriggersReconnect(t *testing.T) {
	dialed := make(chan *fakeSSHConn, 8)
	tun := newTestTunnel(t, dialed)
	tun.keepAliveInterval = 10 * time.Millisecond
	t.Cleanup(tun.Stop)

	if err := tun.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	first := waitConn(t, dialed)
	first.failReq.Store(true) // 下一次 keepalive 失败

	second := waitConn(t, dialed)
	if second == first {
		t.Fatal("keepalive 失败后应建立新连接")
	}
}

// TestStopInterruptsInFlightHandshake 验证 Stop 不会阻塞在进行中的 SSH 握手上。
//
// 用一个只接受连接、不发版本号的服务端模拟「握手卡住」，
// Stop 取消 ctx 时应关闭底层连接让握手立即失败返回。
func TestStopInterruptsInFlightHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case accepted <- struct{}{}:
		default:
		}
		// 不发送 SSH 版本号，也不关闭：让客户端握手一直等待
		_ = conn
	}()

	tun, err := NewTunnel(config.ParsedTunnel{
		Name:                 "handshake-test",
		LocalPort:            0,
		RemoteHost:           "127.0.0.1",
		RemotePort:           3306,
		SSHHost:              listener.Addr().String(),
		SSHUser:              "nobody",
		KeyFile:              writeTestKey(t),
		ReconnectStrategy:    config.StrategyFixed,
		ReconnectInterval:    time.Hour, // 失败后不要立刻重连，避免干扰
		MaxReconnectAttempts: 0,
	})
	if err != nil {
		t.Fatalf("NewTunnel() error = %v", err)
	}
	if err := tun.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(tun.Stop)

	select {
	case <-accepted: // 握手已开始
	case <-time.After(5 * time.Second):
		t.Fatal("等待连接建立超时")
	}

	done := make(chan struct{})
	go func() {
		tun.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 被进行中的 SSH 握手阻塞")
	}
}

// writeTestKey 生成一份可用于握手的 ed25519 私钥。
func writeTestKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("编码私钥失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}
	return path
}
