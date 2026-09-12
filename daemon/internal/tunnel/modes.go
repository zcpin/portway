package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

func (f *Forwarder) dialSSH(ctx context.Context, target string) (net.Conn, error) {
	if client, ok := f.sshClient.(interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	}); ok {
		return client.DialContext(ctx, "tcp", target)
	}
	return f.sshClient.Dial("tcp", target)
}

func (f *Forwarder) connectTarget(incoming net.Conn) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	if f.mode == "remote" {
		return (&net.Dialer{}).DialContext(ctx, "tcp", f.localAddr)
	}
	if f.mode != "dynamic" {
		return f.dialSSH(ctx, f.remoteAddr)
	}
	if err := incoming.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}
	target, err := socksTarget(incoming)
	if err != nil {
		return nil, err
	}
	conn, err := f.dialSSH(ctx, target)
	if err != nil {
		_ = socksReply(incoming, 5)
		return nil, err
	}
	if err := socksReply(incoming, 0); err != nil {
		conn.Close()
		return nil, err
	}
	if err := incoming.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func socksReply(writer io.Writer, code byte) error {
	_, err := writer.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

func socksTarget(conn io.ReadWriter) (string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", err
	}
	if header[0] != 5 || header[1] == 0 {
		return "", errors.New("invalid SOCKS5 greeting")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", err
	}
	allowed := false
	for _, method := range methods {
		if method == 0 {
			allowed = true
		}
	}
	if !allowed {
		_, _ = conn.Write([]byte{5, 255})
		return "", errors.New("SOCKS5 requires no-auth method")
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(conn, request); err != nil {
		return "", err
	}
	if request[0] != 5 || request[2] != 0 {
		return "", errors.New("invalid SOCKS5 request")
	}
	if request[1] != 1 {
		_ = socksReply(conn, 7)
		return "", errors.New("SOCKS5 supports CONNECT only")
	}
	var host string
	switch request[3] {
	case 1, 4:
		size := 4
		if request[3] == 4 {
			size = 16
		}
		address := make([]byte, size)
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", err
		}
		host = net.IP(address).String()
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", err
		}
		if length[0] == 0 {
			_ = socksReply(conn, 8)
			return "", errors.New("empty SOCKS5 hostname")
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", err
		}
		host = string(address)
		if strings.ContainsAny(host, "\x00\r\n") {
			_ = socksReply(conn, 8)
			return "", errors.New("invalid SOCKS5 hostname")
		}
	default:
		_ = socksReply(conn, 8)
		return "", fmt.Errorf("unsupported SOCKS5 address type %d", request[3])
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBytes)
	if port == 0 {
		_ = socksReply(conn, 8)
		return "", errors.New("SOCKS5 target port is zero")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}
