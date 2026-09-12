package tunnel

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

type socksTestClient struct {
	*fakeSSHConn
	targets chan string
}

func (c *socksTestClient) DialContext(ctx context.Context, _, address string) (net.Conn, error) {
	c.targets <- address
	left, right := net.Pipe()
	go func() { defer right.Close(); _, _ = io.Copy(right, right) }()
	return left, nil
}

func TestSOCKS5ForwardsIPv4DomainAndIPv6(t *testing.T) {
	for _, target := range []struct {
		address []byte
		want    string
	}{
		{[]byte{1, 127, 0, 0, 1}, "127.0.0.1:5432"},
		{append([]byte{3, 10}, []byte("db.example")...), "db.example:5432"},
		{append([]byte{4}, net.ParseIP("::1").To16()...), "[::1]:5432"},
	} {
		client := &socksTestClient{fakeSSHConn: newFakeSSHConn(), targets: make(chan string, 1)}
		forwarder := NewForwarder("socks", "127.0.0.1", "0", "", "0", client, "dynamic")
		if err := forwarder.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(forwarder.Stop)
		conn, err := net.Dial("tcp", forwarder.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		port := make([]byte, 2)
		binary.BigEndian.PutUint16(port, 5432)
		request := append([]byte{5, 1, 0, 5, 1, 0}, target.address...)
		request = append(request, port...)
		if _, err := conn.Write(request); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 12)
		if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 || reply[3] != 0 {
			t.Fatalf("SOCKS5 reply: %v %v", reply, err)
		}
		if got := <-client.targets; got != target.want {
			t.Fatalf("target=%q want=%q", got, target.want)
		}
		if _, err := conn.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, 5)
		if _, err := io.ReadFull(conn, data); err != nil || string(data) != "hello" {
			t.Fatalf("SOCKS5 transfer: %q %v", data, err)
		}
		forwarder.Stop()
	}
}

func TestSOCKS5RejectsUnsupportedCommandsAndStopsPartialHandshakes(t *testing.T) {
	for _, command := range []byte{2, 3} {
		client := &socksTestClient{fakeSSHConn: newFakeSSHConn(), targets: make(chan string, 1)}
		forwarder := NewForwarder("socks", "127.0.0.1", "0", "", "0", client, "dynamic")
		if err := forwarder.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(forwarder.Stop)
		conn, err := net.Dial("tcp", forwarder.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = conn.Write([]byte{5, 1, 0, 5, command, 0, 1})
		reply := make([]byte, 12)
		if _, err := io.ReadFull(conn, reply); err != nil || reply[3] != 7 {
			t.Fatalf("unsupported command: %v %v", reply, err)
		}
		conn.Close()
		select {
		case <-client.targets:
			t.Fatal("unsupported command opened a destination")
		default:
		}
	}
	forwarder := NewForwarder("partial", "127.0.0.1", "0", "", "0", newFakeSSHConn(), "dynamic")
	if err := forwarder.Start(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", forwarder.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte{5})
	done := make(chan struct{})
	go func() { forwarder.Stop(); close(done) }()
	awaitSignal(t, done, "partial SOCKS5 handshake prevented stop")
}
