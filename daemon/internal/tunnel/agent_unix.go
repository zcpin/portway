//go:build !windows

package tunnel

import (
	"context"
	"errors"
	"net"
	"os"
)

func dialSSHAgent(ctx context.Context, address string) (net.Conn, error) {
	if address == "" {
		address = os.Getenv("SSH_AUTH_SOCK")
	}
	if address == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set; start ssh-agent or set agent_socket")
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", address)
}
