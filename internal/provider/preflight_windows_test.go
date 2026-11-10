//go:build windows

package provider

import (
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

// TestPreflightPipeAccessDenied: the Docker Desktop named pipe refusing a
// non-elevated process that is not in docker-users (ERROR_ACCESS_DENIED).
func TestPreflightPipeAccessDenied(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Net: "pipe", Err: &os.PathError{Op: "open", Path: `\.\pipe\docker_engine`, Err: syscall.ERROR_ACCESS_DENIED}}
	if got := classifyDaemonError(dialErr); got != daemonPermissionDenied {
		t.Fatalf("classifyDaemonError = %d", got)
	}
	ep := endpointConfig{Host: "npipe:////./pipe/docker_engine", HostSource: hostSourceArgument}
	sum, det := preflightWarning(t, preflightDialErrClient(t, "tcp://127.0.0.1:2375", dialErr), ep)
	if sum != "Permission denied on the Docker daemon socket" || !strings.Contains(det, "docker-users") || !strings.Contains(det, "npipe:") {
		t.Errorf("%s: %s", sum, det)
	}
}
