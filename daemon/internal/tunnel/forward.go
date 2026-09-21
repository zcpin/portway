package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/byteporter/portway/internal/logger"
)

type Forwarder struct {
	tunnelName string
	localAddr  string
	remoteAddr string
	sshClient  sshConn
	mode       string
	ctx        context.Context
	cancel     context.CancelFunc
	stopChan   chan struct{}
	// done 在 accept 循环退出时关闭，供调用方感知 listener 意外终止
	done     chan struct{}
	doneOnce sync.Once
	wg       sync.WaitGroup
	stopOnce sync.Once

	mu       sync.Mutex
	listener net.Listener
	conns    []net.Conn
}

func NewForwarder(tunnelName, localHost, localPort, remoteHost, remotePort string, sshClient sshConn, modes ...string) *Forwarder {
	mode := "local"
	if len(modes) != 0 && modes[0] != "" {
		mode = modes[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Forwarder{
		mode: mode, ctx: ctx, cancel: cancel,
		tunnelName: tunnelName,
		localAddr:  net.JoinHostPort(localHost, localPort),
		remoteAddr: net.JoinHostPort(remoteHost, remotePort),
		sshClient:  sshClient,
		stopChan:   make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Done 返回的 channel 在 accept 循环退出（停止或出错）时关闭。
func (f *Forwarder) Done() <-chan struct{} {
	return f.done
}

func (f *Forwarder) Start() error {
	var listener net.Listener
	var err error
	switch f.mode {
	case "local", "dynamic":
		listener, err = net.Listen("tcp", f.localAddr)
	case "remote":
		remote, ok := f.sshClient.(interface {
			Listen(string, string) (net.Listener, error)
		})
		if !ok {
			return fmt.Errorf("SSH transport does not support remote forwarding")
		}
		listener, err = remote.Listen("tcp", f.remoteAddr)
	default:
		return fmt.Errorf("unsupported forwarding mode %q", f.mode)
	}
	if err != nil {
		return fmt.Errorf("failed to start %s listener: %w", f.mode, err)
	}

	logger.Info("[%s] %s listener ready on %s", f.tunnelName, f.mode, listener.Addr())

	// 与 Stop 互斥：若 Stop 先执行，就不能再启动 accept 循环；
	// 否则 wg.Add 会与 Stop 里的 wg.Wait 并发，属于 WaitGroup 误用
	f.mu.Lock()
	select {
	case <-f.stopChan:
		f.mu.Unlock()
		listener.Close()
		return nil
	default:
	}
	f.listener = listener
	f.wg.Add(1)
	f.mu.Unlock()

	go f.acceptConnections(listener)

	return nil
}

func (f *Forwarder) acceptConnections(listener net.Listener) {
	// defer 顺序：先关 listener 释放端口，再通知 Done，最后 wg.Done
	defer f.wg.Done()
	defer f.doneOnce.Do(func() { close(f.done) })
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-f.stopChan:
			default:
				logger.Debug("[%s] Accept error: %v", f.tunnelName, err)
			}
			return
		}
		f.wg.Add(1)
		go f.handleConnection(conn)
	}
}

func (f *Forwarder) trackConn(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.stopChan:
		return false
	default:
	}
	f.conns = append(f.conns, conn)
	return true
}

func (f *Forwarder) untrackConn(conn net.Conn) {
	f.mu.Lock()
	for i, c := range f.conns {
		if c == conn {
			f.conns = append(f.conns[:i], f.conns[i+1:]...)
			break
		}
	}
	f.mu.Unlock()
}

func (f *Forwarder) handleConnection(localConn net.Conn) {
	defer f.wg.Done()
	defer localConn.Close()

	if !f.trackConn(localConn) {
		return
	}
	defer f.untrackConn(localConn)

	remoteConn, err := f.connectTarget(localConn)
	if err != nil {
		logger.Error("[%s] %s forwarding failed: %v", f.tunnelName, f.mode, err)
		return
	}
	defer remoteConn.Close()

	if !f.trackConn(remoteConn) {
		return
	}
	defer f.untrackConn(remoteConn)

	logger.Debug("[%s] New connection from %s", f.tunnelName, localConn.RemoteAddr())

	done := make(chan error, 2)
	go func() {
		done <- f.copyData(localConn, remoteConn, "local->remote")
	}()
	go func() {
		done <- f.copyData(remoteConn, localConn, "remote->local")
	}()
	// 正常 EOF 只关闭对端的写方向，另一方向仍可继续传输。
	// 读写失败时关闭两端，确保另一条复制协程也能退出。
	if err := <-done; err != nil {
		localConn.Close()
		remoteConn.Close()
	}
	<-done
}

func (f *Forwarder) copyData(src, dst net.Conn, direction string) error {
	// io.Copy 先转发 n > 0 的数据，再处理同次 Read 返回的 EOF/错误。
	// Stop 通过关闭已登记的连接解除阻塞，无需轮询读超时。
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	if conn, ok := dst.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return nil
}

func (f *Forwarder) Stop() {
	f.stopOnce.Do(func() {
		f.mu.Lock()
		close(f.stopChan)
		if f.cancel != nil {
			f.cancel()
		}
		listener := f.listener
		conns := f.conns
		f.conns = nil
		f.mu.Unlock()

		if listener != nil {
			listener.Close()
		}
		for _, c := range conns {
			c.Close()
		}
	})

	f.wg.Wait()
	f.doneOnce.Do(func() { close(f.done) })
	logger.Debug("[%s] Forwarder stopped", f.tunnelName)
}
