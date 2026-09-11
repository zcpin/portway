package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/config"
	"github.com/byteporter/ssh-tunnel/internal/logger"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	// sshConnectTimeout 是 TCP 连接与 SSH 握手的超时时间
	sshConnectTimeout = 30 * time.Second
	// defaultKeepAliveInterval 是 keepalive 探测间隔，用于发现 NAT 超时、
	// 对端崩溃等不会主动通知本端的失效连接
	defaultKeepAliveInterval = 30 * time.Second
)

// sshConn 是隧道对 SSH 连接的最小依赖，便于测试中替换。
type sshConn interface {
	Dial(network, addr string) (net.Conn, error)
	Close() error
	Wait() error
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
}

// Tunnel represents a single SSH tunnel connection
type Tunnel struct {
	config    config.ParsedTunnel
	strategy  ReconnectStrategy
	sshClient sshConn
	forwarder *Forwarder
	ctx       context.Context
	cancel    context.CancelFunc
	// wg 指向本次运行的 WaitGroup：每次 Start 都新建一个，
	// 避免上一次 Stop 的 Wait 尚未返回时被新的 Start 复用（sync 会 panic）
	wg         *sync.WaitGroup
	mu         sync.Mutex
	isRunning  bool
	manualStop bool

	// dial 建立 SSH 连接，默认走 createSSHConnection，测试可替换
	dial func(ctx context.Context) (sshConn, error)
	// keepAliveInterval 为 keepalive 探测间隔
	keepAliveInterval time.Duration
}

// NewTunnel creates a new tunnel with the given configuration
func NewTunnel(cfg config.ParsedTunnel) (*Tunnel, error) {
	strategy, err := ParseStrategy(string(cfg.ReconnectStrategy), cfg.ReconnectInterval)
	if err != nil {
		return nil, fmt.Errorf("failed to parse reconnect strategy: %w", err)
	}

	t := &Tunnel{
		config:            cfg,
		strategy:          strategy,
		keepAliveInterval: defaultKeepAliveInterval,
	}
	t.dial = t.createSSHConnection
	return t, nil
}

// Start starts the tunnel
func (t *Tunnel) Start() error {
	t.mu.Lock()
	if t.isRunning {
		t.mu.Unlock()
		return fmt.Errorf("tunnel %s is already running", t.config.Name)
	}
	// ctx/cancel/wg 都必须在锁内完成：Stop 会在锁内捕获它们
	ctx, cancel := context.WithCancel(context.Background())
	t.ctx, t.cancel = ctx, cancel
	t.wg = &sync.WaitGroup{}
	t.wg.Add(1)
	wg := t.wg
	t.isRunning = true
	t.manualStop = false
	t.mu.Unlock()

	go t.run(ctx, wg)

	logger.Info("[%s] Tunnel started", t.config.Name)
	return nil
}

// Stop stops the tunnel
func (t *Tunnel) Stop() {
	t.mu.Lock()
	if !t.isRunning {
		t.mu.Unlock()
		return
	}
	t.manualStop = true
	t.isRunning = false
	cancel := t.cancel
	forwarder := t.forwarder
	sshClient := t.sshClient
	wg := t.wg
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// Stop forwarder if running
	if forwarder != nil {
		forwarder.Stop()
	}

	// Close SSH client
	if sshClient != nil {
		sshClient.Close()
	}

	// 只等待本次 Start 对应的 run，不干扰后续 Start
	if wg != nil {
		wg.Wait()
	}
	logger.Info("[%s] Tunnel stopped", t.config.Name)
}

// IsRunning returns whether the tunnel is currently running
func (t *Tunnel) IsRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.isRunning
}

// run is the main tunnel loop：连接 — 维持 — 掉线后按策略重连。
// attempt 是本次运行的局部状态，不与后续 Start 的 run 共享。
func (t *Tunnel) run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	attempt := 0
	for {
		if ctx.Err() != nil {
			logger.Debug("[%s] Tunnel run loop cancelled", t.config.Name)
			return
		}
		if !t.strategy.ShouldContinue(attempt, t.config.MaxReconnectAttempts) {
			logger.Error("[%s] Max reconnect attempts (%d) reached, giving up", t.config.Name, t.config.MaxReconnectAttempts)
			return
		}

		established, err := t.connectAndForward(ctx)
		if ctx.Err() != nil {
			logger.Debug("[%s] Tunnel run loop cancelled", t.config.Name)
			return
		}

		if established {
			// 连接曾成功建立，计数从头开始
			attempt = 0
			t.strategy.Reset()
		}
		if err == nil {
			err = errors.New("connection closed")
		}

		logger.Warn("[%s] Connection lost (attempt %d): %v", t.config.Name, attempt+1, err)

		attempt++
		interval := t.strategy.GetInterval(attempt)
		logger.Info("[%s] Reconnecting in %v...", t.config.Name, interval)

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// connectAndForward establishes the SSH connection and forwards until it drops.
//
// 返回 (established, err)：
//   - established 为 true 表示连接成功建立过（用于重置重连计数）
//   - err 非 nil 表示连接建立失败或建立后断开，需要重连
//   - ctx 取消时立刻返回，由调用方判断退出
func (t *Tunnel) connectAndForward(ctx context.Context) (bool, error) {
	logger.Info("[%s] Connecting to %s as %s...", t.config.Name, t.config.SSHHost, t.config.SSHUser)

	client, err := t.dial(ctx)
	if err != nil {
		return false, err
	}

	t.mu.Lock()
	t.sshClient = client
	t.mu.Unlock()

	logger.Info("[%s] SSH connection established", t.config.Name)

	// Create and start forwarder
	forwarder := NewForwarder(
		t.config.Name,
		"127.0.0.1", // Always bind to localhost
		fmt.Sprintf("%d", t.config.LocalPort),
		t.config.RemoteHost,
		fmt.Sprintf("%d", t.config.RemotePort),
		client,
	)

	// 先登记再启动：否则并发的 Stop 可能看不到这个 forwarder，导致监听泄漏
	t.mu.Lock()
	t.forwarder = forwarder
	t.mu.Unlock()

	if err := forwarder.Start(); err != nil {
		client.Close()
		return false, fmt.Errorf("failed to start forwarder: %w", err)
	}

	established := true
	runErr := error(nil)

	defer func() {
		forwarder.Stop()
		client.Close()
	}()

	// keepalive 探测：失败即关闭连接，让下面的 select 立刻醒过来触发重连
	keepAliveCtx, cancelKeepAlive := context.WithCancel(ctx)
	defer cancelKeepAlive()
	go t.keepAlive(keepAliveCtx, client)

	// SSH 连接断开（对端关闭、网络中断等）时 client.Wait 返回
	clientDone := make(chan struct{}, 1)
	go func() {
		_ = client.Wait()
		clientDone <- struct{}{}
	}()

	select {
	case <-ctx.Done():
		// 手动停止或重连被取消
	case <-clientDone:
		if ctx.Err() == nil {
			runErr = errors.New("SSH connection closed unexpectedly")
		}
	case <-forwarder.Done():
		if ctx.Err() == nil {
			runErr = errors.New("forwarder stopped unexpectedly")
		}
	}

	return established, runErr
}

// keepAlive 周期性发送 keepalive 请求，连接失效时主动关闭以触发重连。
func (t *Tunnel) keepAlive(ctx context.Context, client sshConn) {
	interval := t.keepAliveInterval
	if interval <= 0 {
		interval = defaultKeepAliveInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				logger.Debug("[%s] SSH keepalive failed, closing connection: %v", t.config.Name, err)
				client.Close()
				return
			}
		}
	}
}

// createSSHConnection creates a new SSH connection.
//
// 不用 ssh.Dial 而是自己管理底层连接：这样 ctx 取消（用户点停止）能中断
// 正在进行的拨号与握手，而不是让 Stop 一直等到 30 秒超时。
func (t *Tunnel) createSSHConnection(ctx context.Context) (sshConn, error) {
	// Read the private key
	key, err := os.ReadFile(t.config.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("unable to read private key %s: %w", t.config.KeyFile, err)
	}

	// Parse the private key
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}

	hostKeyCallback, err := t.hostKeyCallback()
	if err != nil {
		return nil, err
	}

	// Create SSH client config
	sshConfig := &ssh.ClientConfig{
		User: t.config.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         sshConnectTimeout,
	}

	// Connect to SSH server
	dialer := &net.Dialer{Timeout: sshConnectTimeout}
	netConn, err := dialer.DialContext(ctx, "tcp", t.config.SSHHost)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", t.config.SSHHost, err)
	}

	// ctx 取消时关闭底层连接以中断握手；握手结束后 goroutine 立即退出
	handshakeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			netConn.Close()
		case <-handshakeDone:
		}
	}()

	conn, chans, reqs, err := ssh.NewClientConn(netConn, t.config.SSHHost, sshConfig)
	close(handshakeDone)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("failed to establish SSH connection to %s: %w", t.config.SSHHost, err)
	}

	return ssh.NewClient(conn, chans, reqs), nil
}

// hostKeyCallback 按配置返回主机密钥校验策略。
func (t *Tunnel) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if t.config.HostKeyCheck == config.HostKeyCheckKnownHosts {
		callback, err := knownhosts.New(t.config.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("load known_hosts %s: %w", t.config.KnownHostsFile, err)
		}
		return callback, nil
	}
	return ssh.InsecureIgnoreHostKey(), nil
}

// GetName returns the tunnel name
func (t *Tunnel) GetName() string {
	return t.config.Name
}

// GetConfig returns the tunnel configuration
func (t *Tunnel) GetConfig() config.ParsedTunnel {
	return t.config
}
