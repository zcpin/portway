package tunnel

import (
	"context"
	"fmt"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func (t *Tunnel) authentication(ctx context.Context) (ssh.AuthMethod, func(), error) {
	if t.config.AuthMethod != "agent" {
		signer, err := t.keys.Load(t.config.KeyFile)
		if err != nil {
			return nil, nil, err
		}
		return ssh.PublicKeys(signer), func() {}, nil
	}
	conn, err := t.agentDial(ctx, t.config.AgentSocket)
	if err != nil {
		return nil, nil, fmt.Errorf("SSH agent unavailable: %w", err)
	}
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cleanup := func() { stopClose(); _ = conn.Close() }
	return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), cleanup, nil
}
