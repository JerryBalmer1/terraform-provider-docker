//go:build !windows

package provider

import (
	"context"
	"errors"
	"net"
	"os"
)

// sshAgentConn connects to the agent named by SSH_AUTH_SOCK.
func sshAgentConn(ctx context.Context) (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set; start an ssh-agent or set identity_agent = false")
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", sock)
}
