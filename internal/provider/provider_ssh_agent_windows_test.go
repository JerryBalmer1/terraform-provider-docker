//go:build windows

package provider

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/crypto/ssh/agent"
)

// listenTestAgent serves kr on a private named pipe and returns its name
// for SSH_AUTH_SOCK (sshAgentConn honors a pipe there).
func listenTestAgent(t *testing.T, kr agent.Agent) string {
	t.Helper()
	name := fmt.Sprintf(`\.\pipe\tpd-test-agent-%d`, time.Now().UnixNano())
	ln, err := winio.ListenPipe(name, nil)
	if err != nil {
		// Some sandboxes forbid creating named pipes.
		t.Skipf("cannot create named pipe %s for a test agent: %v", name, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go serveTestAgent(ln, kr)
	return name
}

func serveTestAgent(ln net.Listener, kr agent.Agent) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() { _ = agent.ServeAgent(kr, c); _ = c.Close() }()
	}
}
