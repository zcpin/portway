package tunnel

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func awaitSignal(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func tunnelListenAddr(t *testing.T, tun *Tunnel) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		tun.mu.Lock()
		forwarder := tun.forwarder
		tun.mu.Unlock()
		if forwarder != nil {
			forwarder.mu.Lock()
			listener := forwarder.listener
			forwarder.mu.Unlock()
			if listener != nil {
				return listener.Addr().String()
			}
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("隧道未开始监听")
		}
	}
}

func TestRetryExhaustionClearsRunningState(t *testing.T) {
	tun := newTestTunnel(t, nil)
	tun.config.MaxReconnectAttempts = 1
	tun.dial = func(context.Context) (sshConn, error) {
		return nil, errors.New("connection refused")
	}
	t.Cleanup(tun.Stop)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	tun.mu.Lock()
	wg := tun.wg
	tun.mu.Unlock()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	awaitSignal(t, done, "重试耗尽后运行循环未退出")
	if tun.IsRunning() {
		t.Fatal("重试耗尽后仍报告运行中")
	}
	if err := tun.Start(); err != nil {
		t.Fatalf("重试耗尽后应允许再次启动: %v", err)
	}
}

type pendingDialSSHConn struct {
	*fakeSSHConn
	started chan struct{}
	once    sync.Once
}

func (c *pendingDialSSHConn) Dial(string, string) (net.Conn, error) {
	c.once.Do(func() { close(c.started) })
	<-c.closed
	return nil, errors.New("transport closed")
}

func TestStopInterruptsPendingChannelDial(t *testing.T) {
	client := &pendingDialSSHConn{fakeSSHConn: newFakeSSHConn(), started: make(chan struct{})}
	tun := newTestTunnel(t, nil)
	tun.dial = func(context.Context) (sshConn, error) { return client, nil }
	t.Cleanup(func() { client.Close(); tun.Stop() })
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	local, err := net.DialTimeout("tcp", tunnelListenAddr(t, tun), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	awaitSignal(t, client.started, "没有尝试打开 SSH 通道")
	done := make(chan struct{})
	go func() { tun.Stop(); close(done) }()
	awaitSignal(t, done, "Stop 被尚未完成的 SSH 通道拨号阻塞")
}

func TestLateDialDoesNotReplaceRestartedTunnel(t *testing.T) {
	tun := newTestTunnel(t, nil)
	oldClient, newClient := newFakeSSHConn(), newFakeSSHConn()
	firstStarted, firstCancelled := make(chan struct{}), make(chan struct{})
	releaseOld := make(chan struct{})
	var releaseOnce sync.Once
	var calls atomic.Int32
	tun.dial = func(ctx context.Context) (sshConn, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-ctx.Done()
			close(firstCancelled)
			<-releaseOld // 模拟拨号恰好在取消之后才返回成功。
			return oldClient, nil
		}
		return newClient, nil
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseOld) })
		oldClient.Close()
		newClient.Close()
		tun.Stop()
	})
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, firstStarted, "首次拨号未开始")
	stopped := make(chan struct{})
	go func() { tun.Stop(); close(stopped) }()
	awaitSignal(t, firstCancelled, "首次运行未取消")
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	_ = tunnelListenAddr(t, tun)
	releaseOnce.Do(func() { close(releaseOld) })
	awaitSignal(t, stopped, "旧运行未退出")
	awaitSignal(t, oldClient.closed, "取消后返回的旧连接未关闭")
	if !tun.IsRunning() {
		t.Fatal("旧运行退出覆盖了新运行的状态")
	}
	tun.mu.Lock()
	current := tun.sshClient
	tun.mu.Unlock()
	if current != newClient {
		t.Fatal("旧拨号覆盖了新运行的 SSH 连接")
	}
	select {
	case <-newClient.closed:
		t.Fatal("旧运行退出关闭了新连接")
	default:
	}
}

func TestSSHHandshakeTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	t.Cleanup(func() { close(serverDone); listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-serverDone // 只接受 TCP，不发送 SSH 版本或握手响应。
	}()
	tun := newTestTunnel(t, nil)
	tun.config.SSHHost = listener.Addr().String()
	tun.config.KeyFile = writeTestKey(t)
	tun.connectTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := tun.createSSHConnection(ctx)
	if client != nil {
		client.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("握手应按自身时限失败，得到 %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("SSH 握手没有自己的超时，只被外层保护超时中断")
	}
}

func TestHandshakeTimeoutDoesNotCloseEstablishedConnection(t *testing.T) {
	keyPath := writeTestKey(t)
	key, err := os.ReadFile(keyPath)
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
	serverDone := make(chan struct{})
	t.Cleanup(func() { close(serverDone); listener.Close() })
	go func() {
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
				_ = channel.Reject(ssh.Prohibited, "no channels in this test")
			}
		}()
		<-serverDone
	}()
	tun := newTestTunnel(t, nil)
	tun.config.SSHHost = listener.Addr().String()
	tun.config.KeyFile = keyPath
	tun.connectTimeout = 500 * time.Millisecond
	client, err := tun.createSSHConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	<-time.After(tun.connectTimeout + 50*time.Millisecond)
	result := make(chan error, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("握手超时影响了已建立的会话: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("已建立的会话无法收发请求")
	}
}

type silentKeepAliveSSHConn struct {
	*fakeSSHConn
	started chan struct{}
	done    chan struct{}
}

func (c *silentKeepAliveSSHConn) SendRequest(string, bool, []byte) (bool, []byte, error) {
	close(c.started)
	defer close(c.done)
	<-c.closed
	return false, nil, errors.New("transport closed")
}

func TestKeepAliveInterruptsUnansweredRequest(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		name := "timeout"
		if cancelRequest {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			client := &silentKeepAliveSSHConn{
				fakeSSHConn: newFakeSSHConn(), started: make(chan struct{}), done: make(chan struct{}),
			}
			t.Cleanup(func() { client.Close() })
			tun := newTestTunnel(t, nil)
			tun.keepAliveInterval = time.Millisecond
			tun.keepAliveTimeout = 50 * time.Millisecond
			if cancelRequest {
				tun.keepAliveTimeout = time.Hour
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { tun.keepAlive(ctx, client); close(done) }()
			awaitSignal(t, client.started, "keepalive 请求未发出")
			if cancelRequest {
				cancel()
			}
			awaitSignal(t, client.closed, "无响应的 keepalive 没有关闭 SSH 连接")
			awaitSignal(t, client.done, "发送请求的协程未退出")
			awaitSignal(t, done, "keepalive 协程未退出")
		})
	}
}
