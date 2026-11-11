package provider

// Pass 6c Lane F (open finding 4): preflight diagnostics for plain HTTP sent
// to a TLS port and for an SSH host that cannot be reached, on the Configure
// ping and on every later operation (daemonPlainConn, sshDialError).

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestPreflightPlainHTTPToTLSP6cF(t *testing.T) {
	srv := preflightTLSEngine(t, tls.NoClientCert)
	host := "tcp://" + srv.Listener.Addr().String()
	ep := endpointConfig{Host: host, APIVersion: "1.47", HostSource: hostSourceArgument}
	c, err := newDockerClient(ep)
	if err != nil {
		t.Fatal(err)
	}
	sum, det := preflightWarning(t, c, ep)
	if sum != "TLS failure talking to the Docker daemon" || !strings.Contains(det, "the provider spoke plain HTTP") {
		t.Fatalf("preflight: %s: %s", sum, det)
	}
	opErr := preflightOpErr(t, c)
	var ce *daemonConnError
	if !errors.As(opErr, &ce) {
		t.Fatalf("operation error is the raw client error: %v", opErr)
	}
	for _, want := range []string{host, "TLS failure talking to the Docker daemon", "the provider spoke plain HTTP", "none (plain HTTP"} {
		if !strings.Contains(opErr.Error(), want) {
			t.Errorf("operation error lacks %q: %v", want, opErr)
		}
	}
}

// The plain-HTTP wrapper keeps half-close, which docker_exec stdin needs on a
// hijacked stream (TestAccExecStdinP6bV failed without it).
func TestDaemonPlainConnCloseWriteP6cF(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		s, err := ln.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer s.Close()
		b, _ := io.ReadAll(s) // returns at the client's half-close
		got <- string(b)
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var conn net.Conn = &daemonPlainConn{Conn: raw}
	cw, ok := conn.(client.CloseWriter)
	if !ok {
		t.Fatal("daemonPlainConn is not a client.CloseWriter")
	}
	_, _ = conn.Write([]byte("stdin"))
	if err := cw.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != "stdin" {
			t.Errorf("server read %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never saw end of file")
	}
}

func TestPreflightSSHUnreachableP6cF(t *testing.T) {
	addr := closedTCPAddr(t)
	hostPart, portPart, _ := strings.Cut(addr, ":")
	var port int
	for _, r := range portPart {
		port = port*10 + int(r-'0')
	}
	sc := &sshConfig{User: "nobody", Host: hostPart, Port: port, Password: "x", Strict: false,
		ConnectTimeout: 3 * time.Second, RemoteSocket: "/var/run/docker.sock"}
	ep := endpointConfig{Host: sc.dockerHost(), SSH: sc, APIVersion: "1.47", HostSource: hostSourceArgument}
	c, err := newDockerClient(ep)
	if err != nil {
		t.Fatal(err)
	}
	sum, det := preflightWarning(t, c, ep)
	if sum != "SSH host not reachable" || !strings.Contains(det, addr) || !strings.Contains(det, "`ssh.proxy_jump`") {
		t.Fatalf("preflight: %s: %s", sum, det)
	}
	opErr := preflightOpErr(t, c)
	var ce *daemonConnError
	if !errors.As(opErr, &ce) || ce.Summary != "SSH host not reachable" {
		t.Fatalf("operation error is the raw client error: %v", opErr)
	}
	if strings.Contains(ce.Detail, "Planning continues") || !strings.Contains(opErr.Error(), addr) {
		t.Errorf("operation detail: %s", ce.Detail)
	}

	// Classification: a wrapped sshDialError is SSH, a handshake failure is not.
	if got := classifyDaemonError(&sshDialError{Addr: addr, Err: errors.New("i/o timeout")}); got != daemonSSHUnreachable {
		t.Errorf("sshDialError classified %v", got)
	}
	if got := classifyDaemonError(errors.New("ssh: handshake failed: EOF")); got == daemonSSHUnreachable {
		t.Error("handshake failure classified as unreachable")
	}
	_ = context.Background
}
