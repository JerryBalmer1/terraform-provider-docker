package provider

// Shared Docker helpers for every lane: client construction, registry auth,
// wait-for-condition, name-or-id resolution and labels. Lane files call these;
// they do not redefine them.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/docker/go-connections/sockets"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
)

// ---------------------------------------------------------------------------
// Client construction

// endpointConfig describes one Docker daemon endpoint. CA, Cert and Key are
// PEM content or file paths; all empty means no TLS. SSH, when set (or when
// Host is ssh://), routes every connection through the pure-Go SSH dialer.
type endpointConfig struct {
	Host          string
	APIVersion    string
	CA            string
	Cert          string
	Key           string
	SkipTLSVerify bool
	UserAgent     string
	SSH           *sshConfig
	// Context is the Docker CLI context the endpoint came from ("" if none).
	Context string
	// HostSource and TLSSource say where Host and the TLS material came
	// from (resolveEndpoint; "" elsewhere), for the Configure preflight
	// diagnostics (preflight.go).
	HostSource string
	TLSSource  string
	// MinAPIVersion is the min_api_version floor ("" = none). When set,
	// finishDockerClient installs apiFloorHook, so every versioned request
	// below the floor fails even on paths that never call pd.APIVersion.
	MinAPIVersion string
}

// newDockerClient builds a client for one endpoint. It makes no network call
// (an SSH endpoint connects on the first API call).
// docker_swarm_node uses it to reach the joining node's own daemon.
func newDockerClient(ep endpointConfig) (*client.Client, error) {
	host := ep.Host
	if host == "" {
		host = defaultDockerHost
	}
	opts := []client.Opt{}
	if ep.SSH != nil || isSSHHost(host) {
		sc := ep.SSH
		if sc == nil {
			var err error
			if sc, err = sshConfigFrom(nil, host, os.Getenv); err != nil {
				return nil, err
			}
		}
		// An unreachable SSH host (or jump host) fails every operation with
		// the preflight's SSH diagnostic (sshDialError, daemonAnnotate).
		dial := daemonAnnotateDial(ep, sc.dialContext)
		// The unix host gives the HTTP layer its dummy host name and no
		// proxy; WithDialContext then swaps the unix dialer for SSH.
		opts = append(opts,
			client.WithHTTPClient(&http.Client{Transport: &http.Transport{DialContext: dial}}),
			client.WithHost("unix://"+sc.RemoteSocket),
			client.WithDialContext(dial),
		)
		return finishDockerClient(ep, opts)
	}
	tlsCfg, err := loadTLSConfig(ep)
	if err != nil {
		return nil, err
	}
	hostURL, err := client.ParseHostURL(host)
	if err != nil {
		return nil, err
	}
	// The dialer WithHost would install, wrapped so a permission-denied or
	// TLS failure carries the same diagnostic as the Configure preflight
	// (daemonAnnotateDial, preflight.go) on every later operation too.
	base, err := daemonBaseDial(hostURL.Scheme, hostURL.Host)
	if err != nil {
		return nil, err
	}
	if tlsCfg != nil {
		// WithHTTPClient must come before WithHost so the host's dialer is
		// configured on this transport. A TLS transport makes the scheme https.
		opts = append(opts, client.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment,
				DialTLSContext: daemonTLSDial(ep, base, tlsCfg)},
		}))
	}
	plainDial := daemonAnnotateDial(ep, base)
	if tlsCfg == nil && hostURL.Scheme == "tcp" {
		// Plain HTTP to a TLS port: the TLS diagnostic on every operation.
		annotated := plainDial
		plainDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := annotated(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &daemonPlainConn{Conn: conn, ep: ep}, nil
		}
	}
	opts = append(opts, client.WithHost(host), client.WithDialContext(plainDial))
	return finishDockerClient(ep, opts)
}

// dialFunc is an http.Transport DialContext.
type dialFunc = func(ctx context.Context, network, addr string) (net.Conn, error)

// daemonBaseDialOverride, when set (tests), replaces the socket dialer of
// every client newDockerClient builds for a non-SSH host.
var daemonBaseDialOverride dialFunc

// daemonBaseDial returns the dialer the moby client's WithHost installs for
// proto / addr (go-connections sockets.ConfigureTransport: unix socket,
// named pipe, or TCP with a timeout).
func daemonBaseDial(proto, addr string) (dialFunc, error) {
	if daemonBaseDialOverride != nil {
		return daemonBaseDialOverride, nil
	}
	tr := &http.Transport{}
	if err := sockets.ConfigureTransport(tr, proto, addr); err != nil {
		return nil, err
	}
	return tr.DialContext, nil
}

// daemonAnnotateDial wraps base so a permission-denied failure is returned
// as a daemonConnError (preflight.go) naming the host, its source and the
// fix, the way the Configure preflight words it.
func daemonAnnotateDial(ep endpointConfig, base dialFunc) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := base(ctx, network, addr)
		if err != nil {
			return nil, daemonAnnotate(ep, err)
		}
		return conn, nil
	}
}

// daemonTLSDial dials with base and runs the TLS handshake itself (what
// http.Transport would do with TLSClientConfig), so a handshake or
// certificate failure is returned as a daemonConnError with the preflight's
// TLS diagnostic. Proxied requests do not use it (http.Transport runs the
// handshake after CONNECT itself), and neither do hijacked streams, which
// the moby client dials with tls.Dial.
func daemonTLSDial(ep endpointConfig, base dialFunc, cfg *tls.Config) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := base(ctx, network, addr)
		if err != nil {
			return nil, daemonAnnotate(ep, err)
		}
		c := cfg.Clone()
		if c.ServerName == "" {
			if h, _, err := net.SplitHostPort(addr); err == nil {
				c.ServerName = h
			} else {
				c.ServerName = addr
			}
		}
		tc := tls.Client(raw, c)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, daemonAnnotate(ep, err)
		}
		return tc, nil
	}
}

func finishDockerClient(ep endpointConfig, opts []client.Opt) (*client.Client, error) {
	if ep.APIVersion != "" {
		opts = append(opts, client.WithVersion(ep.APIVersion))
	} else {
		opts = append(opts, client.WithAPIVersionNegotiation())
	}
	if ep.UserAgent != "" {
		opts = append(opts, client.WithUserAgent(ep.UserAgent))
	}
	if ep.MinAPIVersion != "" {
		host := ep.Host
		if host == "" {
			host = defaultDockerHost
		}
		opts = append(opts, client.WithHTTPRequestHook(apiFloorHook(host, ep.MinAPIVersion)))
	}
	// One tflog entry per Engine API call (api_log.go).
	opts = append(opts, apiLogOpts()...)
	return client.NewClientWithOpts(opts...)
}

// pemOrFile returns v when it is PEM content, otherwise the contents of the
// file it names.
func pemOrFile(v string) ([]byte, error) {
	if strings.Contains(v, "-----BEGIN") {
		return []byte(v), nil
	}
	return os.ReadFile(expandHome(v))
}

func loadTLSConfig(ep endpointConfig) (*tls.Config, error) {
	if ep.CA == "" && ep.Cert == "" && ep.Key == "" && !ep.SkipTLSVerify {
		return nil, nil
	}
	// SkipTLSVerify only comes from a docker context that asked for it.
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: ep.SkipTLSVerify} //nolint:gosec
	if ep.CA != "" {
		ca, err := pemOrFile(ep.CA)
		if err != nil {
			return nil, fmt.Errorf("reading tls.ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, errors.New("tls.ca holds no PEM certificate")
		}
		cfg.RootCAs = pool
	}
	if (ep.Cert == "") != (ep.Key == "") {
		return nil, errors.New("tls.cert and tls.key must be set together")
	}
	if ep.Cert != "" {
		cert, err := pemOrFile(ep.Cert)
		if err != nil {
			return nil, fmt.Errorf("reading tls.cert: %w", err)
		}
		key, err := pemOrFile(ep.Key)
		if err != nil {
			return nil, fmt.Errorf("reading tls.key: %w", err)
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, fmt.Errorf("loading tls.cert/tls.key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// ---------------------------------------------------------------------------
// Errors

// isNotFound reports whether err is a Docker "no such object" error.
func isNotFound(err error) bool {
	return err != nil && cerrdefs.IsNotFound(err)
}

// ---------------------------------------------------------------------------
// Registry auth

// registryCredential is a resolved registry_auth entry.
type registryCredential struct {
	Address  string
	Username string
	Password string
	// Auth is the base64 "user:pass" from a config.json auths entry, used
	// when Username/Password are empty.
	Auth string
	// IdentityToken is an OAuth refresh token from a config.json entry or
	// a credential helper, sent instead of a password when set.
	IdentityToken string
}

// normalizeRegistry maps the many spellings of Docker Hub to docker.io and
// strips scheme and path from everything else.
func normalizeRegistry(addr string) string {
	a := strings.TrimPrefix(strings.TrimPrefix(addr, "https://"), "http://")
	a, _, _ = strings.Cut(a, "/")
	switch a {
	case "docker.io", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	}
	return a
}

// registryHost returns the registry domain of an image reference
// ("nginx" -> "docker.io", "ghcr.io/x/y:1" -> "ghcr.io").
func registryHost(imageRef string) (string, error) {
	named, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		return "", err
	}
	return reference.Domain(named), nil
}

// registryAuthFor returns the encoded X-Registry-Auth header value for
// imageRef, or "" when no registry_auth entry matches.
func (pd *providerData) registryAuthFor(imageRef string) (string, error) {
	cfg, ok, err := pd.registryAuthConfigFor(imageRef)
	if err != nil || !ok {
		return "", err
	}
	return authconfig.Encode(cfg)
}

// registryAuthConfigFor returns the matching AuthConfig (for ImageBuild's
// AuthConfigs map and similar).
func (pd *providerData) registryAuthConfigFor(imageRef string) (registry.AuthConfig, bool, error) {
	host, err := registryHost(imageRef)
	if err != nil {
		return registry.AuthConfig{}, false, fmt.Errorf("parsing image reference %q: %w", imageRef, err)
	}
	c, ok := pd.registryCredentialFor(context.Background(), host)
	if !ok {
		return registry.AuthConfig{}, false, nil
	}
	return registry.AuthConfig{Username: c.Username, Password: c.Password, IdentityToken: c.IdentityToken, ServerAddress: c.Address}, true, nil
}

// registryCredentialFor returns the credential for a registry host: the
// matching registry_auth entry, else the default docker config's (when the
// provider was configured), else none (anonymous).
func (pd *providerData) registryCredentialFor(ctx context.Context, host string) (registryCredential, bool) {
	if pd == nil {
		return registryCredential{}, false
	}
	for _, c := range pd.RegistryAuth {
		if normalizeRegistry(c.Address) == normalizeRegistry(host) {
			return c, true
		}
	}
	return pd.configAuth.lookup(ctx, host)
}

// ---------------------------------------------------------------------------
// Waiting

// waitFor polls cond every interval until it reports done, returns an error,
// ctx ends or timeout elapses. A zero interval means 2s.
func waitFor(ctx context.Context, timeout, interval time.Duration, cond func(context.Context) (bool, error)) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		done, err := cond(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s: %w", timeout, ctx.Err())
		case <-t.C:
		}
	}
}

// parseDurationOr parses a Go duration string ("30s", "5m"), returning def
// for "".
func parseDurationOr(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}

// ---------------------------------------------------------------------------
// Name-or-ID resolution

// namedObject is the minimum resolveNameOrID needs.
type namedObject struct {
	ID   string
	Name string
}

// resolveNameOrID picks the object that ref names: exact ID, then exact name,
// then a unique ID prefix. kind is used in error messages.
func resolveNameOrID(kind, ref string, objs []namedObject) (string, error) {
	for _, o := range objs {
		if o.ID == ref {
			return o.ID, nil
		}
	}
	for _, o := range objs {
		if o.Name == ref {
			return o.ID, nil
		}
	}
	var hits []string
	for _, o := range objs {
		if strings.HasPrefix(o.ID, ref) {
			hits = append(hits, o.ID)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("no %s named or with ID %q: %w", kind, ref, cerrdefs.ErrNotFound)
	default:
		return "", fmt.Errorf("%s ID prefix %q is ambiguous: %s", kind, ref, strings.Join(hits, ", "))
	}
}

func resolveNetworkID(ctx context.Context, c *client.Client, ref string) (string, error) {
	res, err := c.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return "", err
	}
	list := res.Items
	objs := make([]namedObject, 0, len(list))
	for _, n := range list {
		objs = append(objs, namedObject{ID: n.ID, Name: n.Name})
	}
	return resolveNameOrID("network", ref, objs)
}

func resolveVolumeName(ctx context.Context, c *client.Client, ref string) (string, error) {
	res, err := c.VolumeInspect(ctx, ref, client.VolumeInspectOptions{})
	if err != nil {
		return "", err
	}
	return res.Volume.Name, nil
}

func resolveSecretID(ctx context.Context, c *client.Client, ref string) (string, error) {
	res, err := c.SecretList(ctx, client.SecretListOptions{})
	if err != nil {
		return "", err
	}
	list := res.Items
	objs := make([]namedObject, 0, len(list))
	for _, s := range list {
		objs = append(objs, namedObject{ID: s.ID, Name: s.Spec.Name})
	}
	return resolveNameOrID("secret", ref, objs)
}

func resolveConfigID(ctx context.Context, c *client.Client, ref string) (string, error) {
	res, err := c.ConfigList(ctx, client.ConfigListOptions{})
	if err != nil {
		return "", err
	}
	list := res.Items
	objs := make([]namedObject, 0, len(list))
	for _, s := range list {
		objs = append(objs, namedObject{ID: s.ID, Name: s.Spec.Name})
	}
	return resolveNameOrID("config", ref, objs)
}

func resolveServiceID(ctx context.Context, c *client.Client, ref string) (string, error) {
	res, err := c.ServiceList(ctx, client.ServiceListOptions{})
	if err != nil {
		return "", err
	}
	list := res.Items
	objs := make([]namedObject, 0, len(list))
	for _, s := range list {
		objs = append(objs, namedObject{ID: s.ID, Name: s.Spec.Name})
	}
	return resolveNameOrID("service", ref, objs)
}

// resolveNodeID matches a node ID, hostname or node name.
func resolveNodeID(ctx context.Context, c *client.Client, ref string) (string, error) {
	res, err := c.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return "", err
	}
	list := res.Items
	objs := make([]namedObject, 0, 2*len(list))
	for _, n := range list {
		objs = append(objs, namedObject{ID: n.ID, Name: n.Description.Hostname})
		if n.Spec.Name != "" {
			objs = append(objs, namedObject{ID: n.ID, Name: n.Spec.Name})
		}
	}
	return resolveNameOrID("node", ref, objs)
}

// ---------------------------------------------------------------------------
// Labels

const (
	// labelStackNamespace is the label docker stack uses; docker_stack sets
	// it on every object it creates so the CLI recognises the stack.
	labelStackNamespace = "com.docker.stack.namespace"
	// labelManagedBy marks objects this provider created.
	labelManagedBy      = "io.terraform.provider"
	labelManagedByValue = "JerryBalmer1/docker"
)

// mergeLabels returns a new map with every key of each map, later maps
// winning. nil maps are skipped; the result is never nil.
func mergeLabels(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// userLabels drops labels the provider adds itself (managed-by, and the
// stack namespace when stripStack), so Read reports what the user wrote.
func userLabels(in map[string]string, stripStack bool) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if k == labelManagedBy || (stripStack && k == labelStackNamespace) {
			continue
		}
		out[k] = v
	}
	return out
}

// labelFilter builds list filters matching every key=value in labels.
func labelFilter(labels map[string]string) client.Filters {
	args := make(client.Filters)
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args.Add("label", k+"="+labels[k])
	}
	return args
}

// volumeExists is a small convenience for lanes that create volumes.
func volumeExists(ctx context.Context, c *client.Client, name string) (bool, error) {
	_, err := c.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
