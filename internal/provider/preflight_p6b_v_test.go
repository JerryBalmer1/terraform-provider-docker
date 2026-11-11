package provider

// Pass 6b Lane V: after a Configure preflight warning (permission denied,
// TLS failure), the first operation fails with the same diagnostic text
// instead of the raw client error (daemonAnnotateDial / daemonTLSDial in
// client.go, daemonConnError in preflight.go).

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/client"
)

// preflightOpErr runs one operation (a network list, as a resource read
// would) on pd's client and returns its error.
func preflightOpErr(t *testing.T, c *client.Client) error {
	t.Helper()
	_, err := c.NetworkList(context.Background(), client.NetworkListOptions{})
	if err == nil {
		t.Fatal("operation succeeded; want a connection failure")
	}
	return err
}

func TestPreflightOpPermissionDeniedP6bV(t *testing.T) {
	errs := map[string]error{
		"EACCES": &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.EACCES)},
		"EPERM":  &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.EPERM)},
	}
	if isWindows {
		errs["ERROR_ACCESS_DENIED"] = &net.OpError{Op: "dial", Net: "pipe",
			Err: &os.PathError{Op: "open", Path: `\\.\pipe\docker_engine`, Err: syscall.Errno(errorAccessDenied)}}
	}
	for name, dialErr := range errs {
		t.Run(name, func(t *testing.T) {
			daemonBaseDialOverride = func(context.Context, string, string) (net.Conn, error) { return nil, dialErr }
			t.Cleanup(func() { daemonBaseDialOverride = nil })
			// Through Configure, as Terraform runs it: a warning, then the
			// provider data every resource gets.
			resp, pd := configureProvider(t, map[string]tftypes.Value{"host": tfStr("tcp://127.0.0.1:2375")})
			if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 || pd == nil {
				t.Fatalf("Configure: %v", resp.Diagnostics)
			}
			w := resp.Diagnostics.Warnings()[0]
			if w.Summary() != "Permission denied on the Docker daemon socket" {
				t.Fatalf("preflight summary = %q", w.Summary())
			}
			if strings.Count(w.Detail(), "On Linux, the user running Terraform") != 1 {
				t.Errorf("preflight detail repeats the hint: %s", w.Detail())
			}
			err := preflightOpErr(t, pd.Client)
			var ce *daemonConnError
			if !errors.As(err, &ce) {
				t.Fatalf("operation error is the raw client error: %v", err)
			}
			msg := err.Error()
			for _, want := range []string{"Permission denied on the Docker daemon socket", "tcp://127.0.0.1:2375",
				hostSourceArgument, "`docker` group", "`docker-users`"} {
				if !strings.Contains(msg, want) {
					t.Errorf("operation error lacks %q: %s", want, msg)
				}
			}
			// The operation's detail is the preflight's, without the
			// Configure-only "Planning continues" tail.
			if !strings.HasPrefix(w.Detail(), ce.Detail) || strings.Contains(ce.Detail, "Planning continues") {
				t.Errorf("operation detail differs from the preflight's:\nop: %s\npreflight: %s", ce.Detail, w.Detail())
			}
		})
	}
}

func TestPreflightOpTLSP6bV(t *testing.T) {
	srv := preflightTLSEngine(t, tls.NoClientCert)
	host := "tcp://" + srv.Listener.Addr().String()
	ep := endpointConfig{Host: host, APIVersion: "1.47", CA: preflightCA(t), HostSource: hostSourceArgument, TLSSource: tlsSourceBlock}
	c, err := newDockerClient(ep)
	if err != nil {
		t.Fatal(err)
	}
	sum, det := preflightWarning(t, c, ep)
	if sum != "TLS failure talking to the Docker daemon" || strings.Count(det, "not signed by the configured CA") != 1 {
		t.Fatalf("preflight: %s: %s", sum, det)
	}
	opErr := preflightOpErr(t, c)
	var ce *daemonConnError
	if !errors.As(opErr, &ce) {
		t.Fatalf("operation error is the raw client error: %v", opErr)
	}
	if ce.Summary != sum || !strings.HasPrefix(det, ce.Detail) || strings.Contains(ce.Detail, "Planning continues") {
		t.Errorf("operation diagnostic differs from the preflight's:\nop: %s: %s\npreflight: %s: %s", ce.Summary, ce.Detail, sum, det)
	}
	for _, want := range []string{host, "CA (inline PEM)", "not signed by the configured CA", "unknown authority"} {
		if !strings.Contains(opErr.Error(), want) {
			t.Errorf("operation error lacks %q: %v", want, opErr)
		}
	}

	// The right CA: the same client path works (the TLS dial wrapper does
	// a real handshake, not just error annotation).
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	good := endpointConfig{Host: host, APIVersion: "1.47", CA: caPEM, HostSource: hostSourceArgument, TLSSource: tlsSourceBlock}
	gc, err := newDockerClient(good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Ping(context.Background(), client.PingOptions{}); err != nil {
		t.Errorf("ping with the right CA: %v", err)
	}
}

// Errors that are neither permission nor TLS keep the client's own text.
func TestDaemonAnnotatePassThroughP6bV(t *testing.T) {
	ep := endpointConfig{Host: "tcp://127.0.0.1:1"}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	if got := daemonAnnotate(ep, refused); got != error(refused) {
		t.Errorf("connection refused annotated: %v", got)
	}
	if got := daemonAnnotate(ep, context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("context.Canceled annotated: %v", got)
	}
	if daemonAnnotate(ep, nil) != nil {
		t.Error("nil annotated")
	}
}
