package tunnel

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

type finalReadConn struct {
	net.Conn
	err error
}

func (c finalReadConn) Read(p []byte) (int, error) {
	return copy(p, "final payload"), c.err
}

func (finalReadConn) SetReadDeadline(time.Time) error { return nil }

type bufferConn struct {
	net.Conn
	buffer bytes.Buffer
}

func (c *bufferConn) Write(p []byte) (int, error) { return c.buffer.Write(p) }

func TestCopyDataForwardsBytesReturnedWithError(t *testing.T) {
	for _, readErr := range []error{io.EOF, io.ErrUnexpectedEOF} {
		t.Run(readErr.Error(), func(t *testing.T) {
			forwarder := &Forwarder{stopChan: make(chan struct{})}
			dst := &bufferConn{}
			forwarder.copyData(finalReadConn{err: readErr}, dst, "test")
			if got := dst.buffer.String(); got != "final payload" {
				t.Fatalf("丢失了与读取错误一起返回的数据: %q", got)
			}
		})
	}
}

type pipeSSHConn struct {
	*fakeSSHConn
	remote net.Conn
}

func (c *pipeSSHConn) Dial(string, string) (net.Conn, error) { return c.remote, nil }

func TestForwarderTransfersDataAndStopsIdleConnections(t *testing.T) {
	remoteClient, remoteServer := net.Pipe()
	client := &pipeSSHConn{fakeSSHConn: newFakeSSHConn(), remote: remoteClient}
	f := NewForwarder("test", "127.0.0.1", "0", "127.0.0.1", "3306", client)
	t.Cleanup(func() { remoteClient.Close(); remoteServer.Close(); f.Stop() })
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	local, err := net.DialTimeout("tcp", f.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	_ = local.SetDeadline(time.Now().Add(2 * time.Second))
	_ = remoteServer.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := local.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(remoteServer, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("请求转发失败: %q, %v", buf, err)
	}
	if _, err := remoteServer.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(local, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("响应转发失败: %q, %v", buf, err)
	}
	done := make(chan struct{})
	go func() { f.Stop(); close(done) }()
	awaitSignal(t, done, "空闲连接阻止了转发器停止")
	awaitSignal(t, f.Done(), "停止后监听器未退出")
}
