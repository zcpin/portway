package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/byteporter/portway/internal/config"
	"golang.org/x/crypto/ssh"
)

func (t *Tunnel) peerConfig(ctx context.Context, peer config.SSHHop) (*ssh.ClientConfig, func(), error) {
	hop := &Tunnel{keys: t.keys, agentDial: t.agentDial, config: config.ParsedTunnel{
		SSHHost: peer.Host, SSHUser: peer.User, KeyFile: peer.KeyFile, AuthMethod: peer.AuthMethod,
		AgentSocket: peer.AgentSocket, HostKeyCheck: peer.HostKeyCheck, KnownHostsFile: peer.KnownHostsFile}}
	if hop.agentDial == nil {
		hop.agentDial = dialSSHAgent
	}
	auth, cleanup, err := hop.authentication(ctx)
	if err != nil {
		return nil, nil, err
	}
	callback, err := hop.hostKeyCallback()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return &ssh.ClientConfig{User: peer.User, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: callback}, cleanup, nil
}

func dialThrough(ctx context.Context, previous *ssh.Client, host string) (net.Conn, error) {
	if previous != nil {
		return previous.DialContext(ctx, "tcp", host)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", host)
}

func handshake(ctx context.Context, conn net.Conn, host string, options *ssh.ClientConfig) (*ssh.Client, error) {
	stopClose := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopClose()
	transport, channels, requests, err := ssh.NewClientConn(conn, host, options)
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("SSH handshake %s: %w", host, err)
	}
	if !stopClose() || ctx.Err() != nil {
		transport.Close()
		return nil, fmt.Errorf("SSH handshake cancelled: %w", ctx.Err())
	}
	return ssh.NewClient(transport, channels, requests), nil
}

func closeHops(hops []*ssh.Client) error {
	var failures []error
	for i := len(hops) - 1; i >= 0; i-- {
		failures = append(failures, hops[i].Close())
	}
	return errors.Join(failures...)
}

func (t *Tunnel) dialTarget(ctx context.Context) (net.Conn, []*ssh.Client, error) {
	hops := make([]*ssh.Client, 0, len(t.config.Jumps))
	var previous *ssh.Client
	for _, peer := range t.config.Jumps {
		options, cleanup, err := t.peerConfig(ctx, peer)
		if err != nil {
			_ = closeHops(hops)
			return nil, nil, fmt.Errorf("jump %s: %w", peer.Name, err)
		}
		conn, err := dialThrough(ctx, previous, peer.Host)
		if err == nil {
			previous, err = handshake(ctx, conn, peer.Host, options)
		}
		cleanup()
		if err != nil {
			_ = closeHops(hops)
			return nil, nil, fmt.Errorf("jump %s: %w", peer.Name, err)
		}
		hops = append(hops, previous)
	}
	conn, err := dialThrough(ctx, previous, t.config.SSHHost)
	if err != nil {
		_ = closeHops(hops)
		return nil, nil, err
	}
	return conn, hops, nil
}

type chainedClient struct {
	*ssh.Client
	hops []*ssh.Client
	once sync.Once
	err  error
}

func (c *chainedClient) Close() error {
	c.once.Do(func() { c.err = errors.Join(c.Client.Close(), closeHops(c.hops)) })
	return c.err
}

func (t *Tunnel) connectChain(ctx context.Context) (sshConn, error) {
	timeout := t.connectTimeout
	if timeout <= 0 {
		timeout = sshConnectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	options, cleanup, err := t.peerConfig(ctx, t.config.TargetHop())
	if err != nil {
		return nil, err
	}
	defer cleanup()
	conn, hops, err := t.dialTarget(ctx)
	if err != nil {
		return nil, err
	}
	client, err := handshake(ctx, conn, t.config.SSHHost, options)
	if err != nil {
		_ = closeHops(hops)
		return nil, err
	}
	if len(hops) == 0 {
		return client, nil
	}
	return &chainedClient{Client: client, hops: hops}, nil
}
