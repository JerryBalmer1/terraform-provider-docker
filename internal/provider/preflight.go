package provider

// Configure-time daemon preflight: classify a failed ping into one of a few
// causes and turn it into a diagnostic that says which host was tried, where
// that host came from, and (for TLS) which TLS settings were in effect.

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"runtime"
	"strings"
	"syscall"

	"github.com/hashicorp/terraform-plugin-framework/attr"
)

// daemonFailure is the cause of a failed Configure-time ping.
type daemonFailure int

const (
	// daemonUnreachable: nothing answered (connection refused, no such
	// socket or pipe, DNS, timeout).
	daemonUnreachable daemonFailure = iota
	// daemonPermissionDenied: the unix socket or Windows named pipe exists
	// but this process may not open it (EACCES / EPERM /
	// ERROR_ACCESS_DENIED).
	daemonPermissionDenied
	// daemonTLSFailure: handshake, certificate verification, unknown
	// authority, rejected or missing client certificate, or TLS on one side
	// only.
	daemonTLSFailure
	// daemonErrorResponse: something answered, but not with a usable /_ping.
	daemonErrorResponse
	// daemonSSHUnreachable: the TCP connection to an SSH host (or one of its
	// jump hosts) failed (sshDialError, provider_ssh.go).
	daemonSSHUnreachable
)

// plainHTTPToTLSText is what a Go TLS server (dockerd --tlsverify) answers
// to a plain HTTP request (net/http server.go).
const plainHTTPToTLSText = "Client sent an HTTP request to an HTTPS server"

// errPlainHTTPToTLS is returned by the plain-HTTP dial wrapper
// (daemonPlainConn) when the daemon answered with plainHTTPToTLSText.
var errPlainHTTPToTLS = errors.New("client sent an HTTP request to an HTTPS server: the daemon expects TLS on this address")

// Host and TLS sources recorded by resolveEndpoint (endpointConfig.HostSource
// / TLSSource) and shown in the preflight diagnostics.
const (
	hostSourceArgument = "the provider's `host` argument"
	hostSourceSSHBlock = "the provider's `ssh` block (`ssh.host`)"
	hostSourceEnv      = "the DOCKER_HOST environment variable"
	hostSourceDefault  = "the built-in default (no `host`, `ssh` block, `context`, DOCKER_HOST, DOCKER_CONTEXT or `currentContext` in the docker config.json)"
	tlsSourceBlock     = "the provider's `tls` block"
)

// errorAccessDenied is Windows' ERROR_ACCESS_DENIED. syscall.Errno.Is maps it
// to fs.ErrPermission on Windows; the number is matched here too because the
// named-pipe dialer is not guaranteed to return a syscall.Errno chain.
const errorAccessDenied = 5

const isWindows = runtime.GOOS == "windows"

// classifyDaemonError says why a ping failed. It matches typed errors first
// and falls back to the texts the moby client and crypto/tls produce, because
// the moby client flattens some causes (permission denied) into plain text.
func classifyDaemonError(err error) daemonFailure {
	if err == nil {
		return daemonUnreachable
	}
	var sshErr *sshDialError
	if errors.As(err, &sshErr) {
		return daemonSSHUnreachable
	}
	msg := strings.ToLower(err.Error())
	var (
		unknownCA   x509.UnknownAuthorityError
		hostErr     x509.HostnameError
		invalidCert x509.CertificateInvalidError
		verifyErr   *tls.CertificateVerificationError
		recordErr   tls.RecordHeaderError
		alertErr    tls.AlertError
	)
	switch {
	case errors.As(err, &unknownCA), errors.As(err, &hostErr), errors.As(err, &invalidCert),
		errors.As(err, &verifyErr), errors.As(err, &recordErr), errors.As(err, &alertErr):
		return daemonTLSFailure
	case strings.Contains(msg, "x509:"), strings.Contains(msg, "tls:"),
		strings.Contains(msg, "client sent an http request to an https server"),
		strings.Contains(msg, "trying to connect to a tls-enabled daemon without tls"),
		strings.Contains(msg, "check your tls client certification settings"):
		return daemonTLSFailure
	}
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || isWinAccessDenied(err) ||
		strings.Contains(msg, "permission denied") || strings.Contains(msg, "access is denied") {
		return daemonPermissionDenied
	}
	if strings.Contains(msg, "error response from daemon") || strings.Contains(msg, "status code") {
		return daemonErrorResponse
	}
	return daemonUnreachable
}

// isWinAccessDenied reports a Windows ERROR_ACCESS_DENIED errno in err's
// chain. It compiles everywhere and only matches on Windows, where errno 5
// means access denied (on unix it is EIO).
func isWinAccessDenied(err error) bool {
	var errno syscall.Errno
	return isWindows && errors.As(err, &errno) && errno == errorAccessDenied
}

// daemonConnError is a connection failure of the Docker client that the
// preflight classifies as permission denied or TLS (daemonAnnotate). Its
// text is the preflight diagnostic's summary and detail, so the first
// resource, data source or action call after a Configure warning fails with
// the same explanation instead of the raw client error. It deliberately does
// not unwrap to Cause: the moby client would replace a wrapped
// fs.ErrPermission by its own one-line message (client request.go
// doRequest).
type daemonConnError struct {
	Summary, Detail string
	Cause           error
}

func (e *daemonConnError) Error() string { return e.Summary + ": " + e.Detail }

// daemonAnnotate turns a permission-denied or TLS failure reaching ep into
// a daemonConnError; any other error (unreachable, context cancelled, ...)
// is returned unchanged.
func daemonAnnotate(ep endpointConfig, err error) error {
	if err == nil {
		return nil
	}
	switch classifyDaemonError(err) {
	case daemonPermissionDenied, daemonTLSFailure, daemonSSHUnreachable:
		summary, detail := daemonFailureText(ep, err)
		return &daemonConnError{Summary: summary, Detail: detail, Cause: err}
	}
	return err
}

// permissionCause is the text of a permission-denied failure without the
// dial wrapper of a *net.OpError ("dial unix <path>: "), whose host the
// diagnostic already names. The moby client turns any connection error
// whose text contains "dial unix" into its generic "Cannot connect to the
// Docker daemon" (client request.go doRequest), which would drop the
// daemonConnError text.
func permissionCause(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

// daemonCheckDiagnostic returns the summary and detail for a failed
// Configure-time ping of ep.
func daemonCheckDiagnostic(ep endpointConfig, err error) (string, string) {
	const tail = "\n\nPlanning continues; operations that need the daemon fail until this is fixed. " +
		"Set disable_docker_daemon_check = true to skip this check."
	var ce *daemonConnError
	if errors.As(err, &ce) {
		err = ce.Cause
	}
	summary, detail := daemonFailureText(ep, err)
	return summary, detail + tail
}

// daemonFailureText is the summary and detail (without the Configure
// tail) for a failure reaching ep.
func daemonFailureText(ep endpointConfig, err error) (string, string) {
	where := fmt.Sprintf("%s (from %s)", ep.Host, ep.hostSource())
	switch classifyDaemonError(err) {
	case daemonPermissionDenied:
		return "Permission denied on the Docker daemon socket",
			fmt.Sprintf("Connecting to %s was refused by the operating system: %s\n\n"+
				"On Linux, the user running Terraform needs access to the socket (usually membership of the `docker` group, or a "+
				"rootless daemon's socket); on Windows, the named pipe needs an elevated process or membership of the `docker-users` "+
				"group (Docker Desktop).", where, permissionCause(err))
	case daemonTLSFailure:
		return "TLS failure talking to the Docker daemon",
			fmt.Sprintf("The TLS connection to %s failed: %v\n\nTLS settings in effect: %s.\n\n%s",
				where, err, ep.tlsSummary(), tlsHint(err))
	case daemonSSHUnreachable:
		// "connection refused" in the text would make the moby client
		// replace the whole error by its generic "Cannot connect" message
		// (client request.go doRequest), so it is reworded.
		cause := strings.ReplaceAll(err.Error(), "connection refused", "connection was refused")
		return "SSH host not reachable",
			fmt.Sprintf("The SSH connection for %s could not be opened: %s\n\n"+
				"The provider dials SSH itself (no ssh binary). Check the host name and port (`ssh.host` / `ssh.port`, or "+
				"ssh://user@host:port in `host` or DOCKER_HOST), that sshd runs there and a firewall lets the port through, "+
				"every `ssh.proxy_jump` hop, and `ssh.connect_timeout` for a slow link.", where, cause)
	case daemonErrorResponse:
		return "Docker daemon check failed",
			fmt.Sprintf("Something answered at %s, but not with a Docker Engine /_ping: %v", where, err)
	default:
		return "Docker daemon not reachable",
			fmt.Sprintf("No Docker daemon answered at %s: %v\n\n"+
				"Check that the daemon is running and that the address is right; the provider resolves it from `host`, the `ssh` "+
				"block, `context`, DOCKER_HOST, DOCKER_CONTEXT, `currentContext` in the docker config.json, then %s, in that order.",
				where, err, defaultDockerHost)
	}
}

// tlsHint names the likely fix for a TLS failure.
func tlsHint(err error) string {
	msg := strings.ToLower(err.Error())
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	switch {
	case strings.Contains(msg, "client sent an http request to an https server"), strings.Contains(msg, "without tls"),
		strings.Contains(msg, "malformed http response"):
		return "The daemon expects TLS on this address but the provider spoke plain HTTP: set the `tls` block or `cert_path` " +
			"(or DOCKER_TLS_VERIFY and DOCKER_CERT_PATH), or use the daemon's plain-HTTP address."
	case strings.Contains(msg, "first record does not look like a tls handshake"):
		return "The provider spoke TLS but the daemon answers plain HTTP on this address: remove the TLS settings, or use the daemon's TLS port."
	case errors.As(err, &unknownCA), strings.Contains(msg, "unknown authority"):
		return "The daemon's server certificate is not signed by the configured CA (`tls.ca`, `ca.pem`): point the CA at the one that signed the daemon's certificate."
	case errors.As(err, &hostErr), strings.Contains(msg, "certificate is valid for"), strings.Contains(msg, "doesn't contain any ip sans"):
		return "The daemon's server certificate does not name this host: use a host name or address listed in the certificate's SANs, or reissue the certificate."
	case strings.Contains(msg, "expired"), strings.Contains(msg, "not yet valid"):
		return "A certificate is outside its validity period: renew it (and check the clock)."
	case strings.Contains(msg, "certificate required"), strings.Contains(msg, "bad certificate"),
		strings.Contains(msg, "handshake failure"), strings.Contains(msg, "unknown certificate authority"),
		strings.Contains(msg, "client certification"):
		return "The daemon rejected the client certificate, or requires one (dockerd --tlsverify): set `tls.cert` and `tls.key` " +
			"(or `cert.pem` / `key.pem` under `cert_path`) to a client certificate signed by the CA the daemon trusts."
	}
	return "Check the CA, the client certificate and key, and that the daemon listens with TLS on this address."
}

// hostSource names where ep.Host came from ("an unknown source" for
// endpoints not built by resolveEndpoint).
func (ep endpointConfig) hostSource() string {
	if ep.HostSource == "" {
		return "an unknown source"
	}
	return ep.HostSource
}

// tlsSummary describes the TLS settings in effect for ep, without secrets.
func (ep endpointConfig) tlsSummary() string {
	if ep.CA == "" && ep.Cert == "" && ep.Key == "" && !ep.SkipTLSVerify {
		return "none (plain HTTP, no `tls` block, `cert_path`, context TLS material or DOCKER_TLS_VERIFY + DOCKER_CERT_PATH)"
	}
	src := ep.TLSSource
	if src == "" {
		src = "an unknown source"
	}
	ca := "system roots"
	if ep.CA != "" {
		ca = describePEMOrPath(ep.CA)
	}
	cert := "none"
	if ep.Cert != "" {
		cert = describePEMOrPath(ep.Cert)
	}
	return fmt.Sprintf("from %s; CA %s; client certificate %s; skip verification %t", src, ca, cert, ep.SkipTLSVerify)
}

// describePEMOrPath shows a file path as is and PEM content as "(inline PEM)".
func describePEMOrPath(v string) string {
	if strings.Contains(v, "-----BEGIN") {
		return "(inline PEM)"
	}
	return v
}

// endpointHasUnknown reports whether a provider argument that shapes the
// endpoint (beyond the ones resolveEndpoint already rejects when unknown)
// is unknown: the TLS block, the SSH block, or disable_docker_daemon_check.
// Configure then skips the ping, since it would test a different endpoint.
func endpointHasUnknown(m providerModel) bool {
	vals := []attr.Value{m.DisableDockerDaemonCheck}
	if m.TLS != nil {
		vals = append(vals, m.TLS.CA, m.TLS.Cert, m.TLS.Key)
	}
	if s := m.SSH; s != nil {
		vals = append(vals, s.User, s.Host, s.Port, s.IdentityFile, s.IdentityAgent, s.Password, s.PasswordWO,
			s.KnownHostsFile, s.StrictHostKeyChecking, s.ProxyJump, s.ConnectTimeout, s.RemoteSocket)
	}
	for _, v := range vals {
		if v.IsUnknown() {
			return true
		}
	}
	return false
}

// daemonPlainConn wraps a plain-HTTP connection to a tcp:// host. When the
// first bytes the daemon sends are a Go TLS server's answer to plain HTTP
// (plainHTTPToTLSText), or a TLS record (an alert or handshake, 0x15 /
// 0x16), the read fails with a daemonConnError carrying the preflight's TLS
// diagnostic, so every operation names the fix instead of the raw
// "Error response from daemon" or "malformed HTTP response".
type daemonPlainConn struct {
	net.Conn
	ep      endpointConfig
	checked bool
}

// CloseWrite half-closes the wrapped connection, so a hijacked exec stream
// (docker_exec stdin) still signals end of file (cop_exec.go needs a
// client.CloseWriter). Pass 6c Lane F: found by TestAccExecStdinP6bV.
func (c *daemonPlainConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return fmt.Errorf("%T cannot half-close", c.Conn)
}

func (c *daemonPlainConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if c.checked || n == 0 {
		return n, err
	}
	c.checked = true
	head := b[:n]
	if head[0] == 0x15 || head[0] == 0x16 || (bytes.HasPrefix(head, []byte("HTTP/1.0 400")) && bytes.Contains(head, []byte(plainHTTPToTLSText))) {
		return 0, daemonAnnotate(c.ep, errPlainHTTPToTLS)
	}
	return n, err
}
