//go:build windows

package tunnel

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"

	"github.com/Microsoft/go-winio"
)

func dialSSHAgent(ctx context.Context, address string) (net.Conn, error) {
	if address == "" {
		address = os.Getenv("SSH_AUTH_SOCK")
	}
	if address == "" {
		address = `\\.\pipe\openssh-ssh-agent`
	}
	if !strings.HasPrefix(strings.ToLower(address), `\\.\pipe\`) {
		return nil, errors.New("Windows agent_socket must be a local named pipe")
	}
	return winio.DialPipeContext(ctx, address)
}
