package provider

// Configure-time daemon preflight: classify a failed ping into one of a few
// causes and turn it into a diagnostic that says which host was tried, where
// that host came from, and (for TLS) which TLS settings were in effect.

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
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
)

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

// daemonCheckDiagnostic returns the summary and detail for a failed
// Configure-time ping of ep.
func daemonCheckDiagnostic(ep endpointConfig, err error) (string, string) {
	where := fmt.Sprintf("%s (from %s)", ep.Host, ep.hostSource())
	const tail = "\n\nPlanning continues; operations that need the daemon fail until this is fixed. " +
		"Set disable_docker_daemon_check = true to skip this check."
	switch classifyDaemonError(err) {
	case daemonPermissionDenied:
		return "Permission denied on the Docker daemon socket",
			fmt.Sprintf("Connecting to %s was refused by the operating system: %v\n\n"+
				"On Linux, the user running Terraform needs access to the socket (usually membership of the `docker` group, or a "+
				"rootless daemon's socket); on Windows, the named pipe needs an elevated process or membership of the `docker-users` "+
				"group (Docker Desktop).%s", where, err, tail)
	case daemonTLSFailure:
		return "TLS failure talking to the Docker daemon",
			fmt.Sprintf("The TLS connection to %s failed: %v\n\nTLS settings in effect: %s.\n\n%s%s",
				where, err, ep.tlsSummary(), tlsHint(err), tail)
	case daemonErrorResponse:
		return "Docker daemon check failed",
			fmt.Sprintf("Something answered at %s, but not with a Docker Engine /_ping: %v%s", where, err, tail)
	default:
		return "Docker daemon not reachable",
			fmt.Sprintf("No Docker daemon answered at %s: %v\n\n"+
				"Check that the daemon is running and that the address is right; the provider resolves it from `host`, the `ssh` "+
				"block, `context`, DOCKER_HOST, DOCKER_CONTEXT, `currentContext` in the docker config.json, then %s, in that order.%s",
				where, err, defaultDockerHost, tail)
	}
}

// tlsHint names the likely fix for a TLS failure.
func tlsHint(err error) string {
	msg := strings.ToLower(err.Error())
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	switch {
	case strings.Contains(msg, "client sent an http request to an https server"), strings.Contains(msg, "without tls"):
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
