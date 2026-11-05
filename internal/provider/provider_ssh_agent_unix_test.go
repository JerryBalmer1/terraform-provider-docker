//go:build !windows

package provider

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh/agent"
)

// listenTestAgent serves kr on a unix socket and returns its path for
// SSH_AUTH_SOCK.
func listenTestAgent(t *testing.T, kr agent.Agent) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tpdag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go serveTestAgent(ln, kr)
	return sock
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
