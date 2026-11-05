//go:build windows

package provider

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

// windowsSSHAgentPipe is the Windows OpenSSH agent's named pipe.
const windowsSSHAgentPipe = `\.\pipe\openssh-ssh-agent`

// sshAgentConn connects to the Windows OpenSSH agent (or to the pipe named
// by SSH_AUTH_SOCK, as set by some agents such as 1Password or gpg4win).
func sshAgentConn(ctx context.Context) (net.Conn, error) {
	p := windowsSSHAgentPipe
	if s := os.Getenv("SSH_AUTH_SOCK"); strings.HasPrefix(s, `\.\pipe\`) || strings.HasPrefix(s, `//./pipe/`) {
		p = strings.ReplaceAll(s, "/", `\`)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("connecting to SSH agent pipe %s (is the ssh-agent service running?): %w", p, err)
	}
	return c, nil
}
