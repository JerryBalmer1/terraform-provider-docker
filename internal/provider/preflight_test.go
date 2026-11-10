package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/client"
)

func TestClassifyDaemonError(t *testing.T) {
	cases := map[string]struct {
		err  error
		want daemonFailure
	}{
		"refused":            {&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, daemonUnreachable},
		"no socket":          {fmt.Errorf("failed to connect to the docker API at unix:///x.sock: %w", fs.ErrNotExist), daemonUnreachable},
		"wrapped EACCES":     {&net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.EACCES)}, daemonPermissionDenied},
		"wrapped EPERM":      {fmt.Errorf("dial: %w", &os.PathError{Op: "open", Path: "/var/run/docker.sock", Err: syscall.EPERM}), daemonPermissionDenied},
		"fs.ErrPermission":   {fmt.Errorf("x: %w", fs.ErrPermission), daemonPermissionDenied},
		"moby flattened":     {errors.New("permission denied while trying to connect to the docker API at unix:///var/run/docker.sock"), daemonPermissionDenied},
		"pipe access denied": {errors.New("open //./pipe/docker_engine: Access is denied."), daemonPermissionDenied},
		"unknown authority":  {fmt.Errorf("error during connect: %w", x509.UnknownAuthorityError{}), daemonTLSFailure},
		"hostname":           {fmt.Errorf("x: %w", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "h"}), daemonTLSFailure},
		"record header":      {fmt.Errorf("x: %w", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}), daemonTLSFailure},
		"remote alert":       {errors.New("remote error: tls: certificate required"), daemonTLSFailure},
		"plain to tls":       {errors.New("Error response from daemon: Client sent an HTTP request to an HTTPS server."), daemonTLSFailure},
		"daemon error":       {errors.New("Error response from daemon: page not found"), daemonErrorResponse},
	}
	for name, tc := range cases {
		if got := classifyDaemonError(tc.err); got != tc.want {
			t.Errorf("%s: classifyDaemonError(%v) = %d, want %d", name, tc.err, got, tc.want)
		}
	}
}

// TestResolveEndpointHostSource: every precedence step records where the
// host came from, for the preflight diagnostics.
func TestResolveEndpointHostSource(t *testing.T) {
	dir := t.TempDir()
	env := func(kv map[string]string) func(string) string {
		return func(k string) string {
			if k == "DOCKER_CONFIG" {
				return dir
			}
			return kv[k]
		}
	}
	var diags diag.Diagnostics
	if ep := resolveEndpoint(providerModel{Host: types.StringValue("tcp://h:2375")}, env(nil), &diags); ep.HostSource != hostSourceArgument {
		t.Errorf("host argument: %q", ep.HostSource)
	}
	if ep := resolveEndpoint(providerModel{}, env(map[string]string{"DOCKER_HOST": "tcp://e:2375"}), &diags); ep.HostSource != hostSourceEnv {
		t.Errorf("DOCKER_HOST: %q", ep.HostSource)
	}
	ep := resolveEndpoint(providerModel{}, env(nil), &diags)
	if ep.HostSource != hostSourceDefault || ep.Host != defaultDockerHost {
		t.Errorf("default: %q %q", ep.Host, ep.HostSource)
	}
	if ep.tlsSummary() == "" || !strings.HasPrefix(ep.tlsSummary(), "none") {
		t.Errorf("no TLS: %q", ep.tlsSummary())
	}
	ep = resolveEndpoint(providerModel{Host: types.StringValue("tcp://h:2376"), CertPath: types.StringValue("certs")}, env(nil), &diags)
	if !strings.Contains(ep.tlsSummary(), "`cert_path` certs") || !strings.Contains(ep.tlsSummary(), "client certificate certs") {
		t.Errorf("cert_path TLS summary: %q", ep.tlsSummary())
	}
	if diags.HasError() {
		t.Fatal(diags)
	}
}

// preflightWarning runs checkDockerDaemon and returns its one warning.
func preflightWarning(t *testing.T, c *client.Client, ep endpointConfig) (string, string) {
	t.Helper()
	var diags diag.Diagnostics
	checkDockerDaemon(context.Background(), c, ep, DefaultMinAPIVersion, &diags)
	if diags.HasError() || diags.WarningsCount() != 1 {
		t.Fatalf("want exactly one warning and no error, got %v", diags)
	}
	w := diags.Warnings()[0]
	return w.Summary(), w.Detail()
}

func TestPreflightUnreachable(t *testing.T) {
	addr := closedTCPAddr(t)
	ep := endpointConfig{Host: "tcp://" + addr, APIVersion: "1.47", HostSource: hostSourceEnv}
	c, err := newDockerClient(ep)
	if err != nil {
		t.Fatal(err)
	}
	sum, det := preflightWarning(t, c, ep)
	if sum != "Docker daemon not reachable" || !strings.Contains(det, addr) || !strings.Contains(det, "DOCKER_HOST environment variable") {
		t.Errorf("%s: %s", sum, det)
	}
	// Through Configure: the host argument is named as the source.
	resp, _ := configureProvider(t, map[string]tftypes.Value{"host": tfStr("tcp://" + addr)})
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 ||
		!strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), hostSourceArgument) {
		t.Errorf("Configure: %v", resp.Diagnostics)
	}
}

// preflightDialErrClient is a client whose every connection fails with err,
// the way a unix socket or named pipe without access does.
func preflightDialErrClient(t *testing.T, host string, err error) *client.Client {
	t.Helper()
	c, cerr := client.NewClientWithOpts(client.WithHost(host), client.WithVersion("1.47"),
		client.WithDialContext(func(context.Context, string, string) (net.Conn, error) { return nil, err }))
	if cerr != nil {
		t.Fatal(cerr)
	}
	return c
}

func TestPreflightPermissionDenied(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EACCES, syscall.EPERM} {
		dialErr := &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", errno)}
		ep := endpointConfig{Host: "unix:///var/run/docker.sock", HostSource: hostSourceDefault}
		sum, det := preflightWarning(t, preflightDialErrClient(t, "tcp://127.0.0.1:2375", dialErr), ep)
		if sum != "Permission denied on the Docker daemon socket" || !strings.Contains(det, "/var/run/docker.sock") ||
			!strings.Contains(det, "built-in default") || !strings.Contains(det, "`docker` group") {
			t.Errorf("%v: %s: %s", errno, sum, det)
		}
	}
}

// preflightCA returns a fresh self-signed CA certificate as PEM; nothing
// trusts it, so a server certificate checked against it is from an
// unknown authority.
func preflightCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tpd preflight test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// preflightTLSEngine is a fake Engine behind TLS (httptest's own
// certificate) with the given client-auth policy.
func preflightTLSEngine(t *testing.T, auth tls.ClientAuthType) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		_, _ = io.WriteString(w, "OK")
	}))
	srv.TLS = &tls.Config{ClientAuth: auth}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestPreflightTLS(t *testing.T) {
	srv := preflightTLSEngine(t, tls.NoClientCert)
	host := "tcp://" + srv.Listener.Addr().String()

	// Untrusted server certificate.
	ep := endpointConfig{Host: host, APIVersion: "1.47", CA: preflightCA(t), HostSource: hostSourceArgument, TLSSource: tlsSourceBlock}
	c, err := newDockerClient(ep)
	if err != nil {
		t.Fatal(err)
	}
	sum, det := preflightWarning(t, c, ep)
	if sum != "TLS failure talking to the Docker daemon" || !strings.Contains(det, "from the provider's `tls` block; CA (inline PEM); client certificate none") ||
		!strings.Contains(det, "not signed by the configured CA") {
		t.Errorf("unknown authority: %s: %s", sum, det)
	}

	// Plain HTTP against the TLS daemon.
	plain := endpointConfig{Host: host, APIVersion: "1.47", HostSource: hostSourceEnv}
	c, err = newDockerClient(plain)
	if err != nil {
		t.Fatal(err)
	}
	sum, det = preflightWarning(t, c, plain)
	if sum != "TLS failure talking to the Docker daemon" || !strings.Contains(det, "TLS settings in effect: none") ||
		!strings.Contains(det, "expects TLS") {
		t.Errorf("plain to TLS: %s: %s", sum, det)
	}

	// The daemon requires a client certificate and none is configured.
	mtls := preflightTLSEngine(t, tls.RequireAnyClientCert)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mtls.Certificate().Raw}))
	noCert := endpointConfig{Host: "tcp://" + mtls.Listener.Addr().String(), APIVersion: "1.47", CA: caPEM,
		HostSource: hostSourceArgument, TLSSource: tlsSourceBlock}
	c, err = newDockerClient(noCert)
	if err != nil {
		t.Fatal(err)
	}
	sum, det = preflightWarning(t, c, noCert)
	if sum != "TLS failure talking to the Docker daemon" || !strings.Contains(det, "client certificate") {
		t.Errorf("client cert required: %s: %s", sum, det)
	}
}

func TestPreflightAPIFloor(t *testing.T) {
	eng := fakeEngineAPI(t, "1.41")
	resp, _ := configureProvider(t, map[string]tftypes.Value{"host": tfStr("tcp://" + eng.Listener.Addr().String())})
	if !hasAttrError(resp.Diagnostics, "min_api_version") {
		t.Fatalf("want an error on min_api_version, got %v", resp.Diagnostics)
	}
	d := resp.Diagnostics.Errors()[0]
	if d.Summary() != "Docker daemon too old" || !strings.Contains(d.Detail(), "1.41") || !strings.Contains(d.Detail(), DefaultMinAPIVersion) ||
		!strings.Contains(d.Detail(), hostSourceArgument) {
		t.Errorf("floor: %s: %s", d.Summary(), d.Detail())
	}
	// Below what the client can negotiate at all: still "too old", with
	// both versions, not "not reachable".
	old := fakeEngineAPI(t, "1.30")
	resp, _ = configureProvider(t, map[string]tftypes.Value{"host": tfStr("tcp://" + old.Listener.Addr().String())})
	if !hasAttrError(resp.Diagnostics, "min_api_version") || resp.Diagnostics.WarningsCount() != 0 {
		t.Fatalf("ancient daemon: want an error on min_api_version, got %v", resp.Diagnostics)
	}
	d = resp.Diagnostics.Errors()[0]
	if !strings.Contains(d.Detail(), "1.30") || !strings.Contains(d.Detail(), DefaultMinAPIVersion) || !strings.Contains(d.Detail(), client.MinAPIVersion) {
		t.Errorf("ancient daemon: %s", d.Detail())
	}
}

// TestPreflightSkipsUnknown: with an unknown tls value (or an unknown
// disable_docker_daemon_check) Configure does not ping, so a closed port
// gives no warning.
func TestPreflightSkipsUnknown(t *testing.T) {
	addr := closedTCPAddr(t)
	resp, pd := configureProvider(t, map[string]tftypes.Value{
		"host":                        tfStr("tcp://" + addr),
		"disable_docker_daemon_check": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
	})
	if len(resp.Diagnostics) != 0 || pd == nil {
		t.Fatalf("unknown disable flag: %v", resp.Diagnostics)
	}
	tlsType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"ca": tftypes.String, "cert": tftypes.String, "key": tftypes.String}}
	resp, pd = configureProvider(t, map[string]tftypes.Value{
		"host": tfStr("tcp://" + addr),
		"tls": tftypes.NewValue(tlsType, map[string]tftypes.Value{
			"ca":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"cert": tftypes.NewValue(tftypes.String, nil),
			"key":  tftypes.NewValue(tftypes.String, nil),
		}),
	})
	if len(resp.Diagnostics) != 0 || pd == nil {
		t.Fatalf("unknown tls.ca: %v", resp.Diagnostics)
	}
}
