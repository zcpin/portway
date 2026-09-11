package tunnel

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/logger"
)

type Forwarder struct {
	tunnelName string
	localAddr  string
	remoteAddr string
	sshClient  sshConn
	stopChan   chan struct{}
	// done 在 accept 循环退出时关闭，供调用方感知 listener 意外终止
	done     chan struct{}
	doneOnce sync.Once
	wg       sync.WaitGroup
	stopOnce sync.Once

	mu    sync.Mutex
	conns []net.Conn
}

func NewForwarder(tunnelName, localHost, localPort, remoteHost, remotePort string, sshClient sshConn) *Forwarder {
	return &Forwarder{
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
	listener, err := net.Listen("tcp", f.localAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", f.localAddr, err)
	}

	logger.Info("[%s] Listening on %s, forwarding to %s", f.tunnelName, f.localAddr, f.remoteAddr)

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
		select {
		case <-f.stopChan:
			logger.Debug("[%s] Stopping listener", f.tunnelName)
			return
		default:
			connCh := make(chan net.Conn, 1)
			errCh := make(chan error, 1)

			go func() {
				conn, err := listener.Accept()
				if err != nil {
					errCh <- err
				} else {
					connCh <- conn
				}
			}()

			select {
			case <-f.stopChan:
				return
			case conn := <-connCh:
				f.wg.Add(1)
				go f.handleConnection(conn)
			case err := <-errCh:
				if err != io.EOF {
					logger.Debug("[%s] Accept error: %v", f.tunnelName, err)
				}
				return
			}
		}
	}
}

func (f *Forwarder) trackConn(conn net.Conn) {
	f.mu.Lock()
	f.conns = append(f.conns, conn)
	f.mu.Unlock()
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

	f.trackConn(localConn)
	defer f.untrackConn(localConn)

	remoteConn, err := f.sshClient.Dial("tcp", f.remoteAddr)
	if err != nil {
		logger.Error("[%s] Failed to dial remote %s: %v", f.tunnelName, f.remoteAddr, err)
		return
	}
	defer remoteConn.Close()

	f.trackConn(remoteConn)
	defer f.untrackConn(remoteConn)

	logger.Debug("[%s] New connection from %s", f.tunnelName, localConn.RemoteAddr())

	done := make(chan struct{})
	f.wg.Add(2)
	go func() {
		defer f.wg.Done()
		f.copyData(localConn, remoteConn, "local->remote")
	}()
	go func() {
		defer f.wg.Done()
		f.copyData(remoteConn, localConn, "remote->local")
		close(done)
	}()
	<-done
}

func (f *Forwarder) copyData(src, dst net.Conn, direction string) {
	buf := make([]byte, 32*1024)

	for {
		select {
		case <-f.stopChan:
			return
		default:
			src.SetReadDeadline(time.Now().Add(1 * time.Second))

			n, err := src.Read(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return
			}

			if n > 0 {
				if _, err := dst.Write(buf[:n]); err != nil {
					return
				}
			}
		}
	}
}

func (f *Forwarder) Stop() {
	f.stopOnce.Do(func() {
		close(f.stopChan)
	})

	f.mu.Lock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns = nil
	f.mu.Unlock()

	f.wg.Wait()
	logger.Debug("[%s] Forwarder stopped", f.tunnelName)
}
