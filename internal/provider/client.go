package provider

// Shared Docker helpers for every lane: client construction, registry auth,
// wait-for-condition, name-or-id resolution and labels. Lane files call these;
// they do not redefine them.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

// ---------------------------------------------------------------------------
// Client construction

// endpointConfig describes one Docker daemon endpoint. CA, Cert and Key are
// PEM content or file paths; all empty means no TLS.
type endpointConfig struct {
	Host       string
	APIVersion string
	CA         string
	Cert       string
	Key        string
	UserAgent  string
}

// newDockerClient builds a client for one endpoint. It makes no network call.
// docker_swarm_node uses it to reach the joining node's own daemon.
func newDockerClient(ep endpointConfig) (*client.Client, error) {
	host := ep.Host
	if host == "" {
		host = defaultDockerHost
	}
	opts := []client.Opt{}
	tlsCfg, err := loadTLSConfig(ep)
	if err != nil {
		return nil, err
	}
	if tlsCfg != nil {
		// WithHTTPClient must come before WithHost so the host's dialer is
		// configured on this transport. A TLS transport makes the scheme https.
		opts = append(opts, client.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment},
		}))
	}
	opts = append(opts, client.WithHost(host))
	if ep.APIVersion != "" {
		opts = append(opts, client.WithVersion(ep.APIVersion))
	} else {
		opts = append(opts, client.WithAPIVersionNegotiation())
	}
	if ep.UserAgent != "" {
		opts = append(opts, client.WithUserAgent(ep.UserAgent))
	}
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
	if ep.CA == "" && ep.Cert == "" && ep.Key == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
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
}

func resolveRegistryAuth(in []registryAuthModel, diags *diag.Diagnostics) []registryCredential {
	var out []registryCredential
	for i, m := range in {
		c := registryCredential{
			Address:  m.Address.ValueString(),
			Username: m.Username.ValueString(),
			Password: m.Password.ValueString(),
		}
		if cf := m.ConfigFile.ValueString(); cf != "" && c.Username == "" {
			u, p, err := credentialFromConfigFile(expandHome(cf), c.Address)
			if err != nil {
				diags.AddAttributeError(path.Root("registry_auth").AtListIndex(i).AtName("config_file"),
					"Cannot read registry credentials", err.Error())
				continue
			}
			c.Username, c.Password = u, p
		}
		if c.Username == "" {
			diags.AddAttributeError(path.Root("registry_auth").AtListIndex(i),
				"Incomplete registry_auth", fmt.Sprintf("registry_auth for %q needs username/password or a config_file with an auths entry.", c.Address))
			continue
		}
		out = append(out, c)
	}
	return out
}

// credentialFromConfigFile reads the auths entry for address from a docker
// config.json. Credential helpers (credsStore/credHelpers) are not supported.
func credentialFromConfigFile(file, address string) (string, string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", "", err
	}
	var cfg struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", "", fmt.Errorf("parsing %s: %w", file, err)
	}
	for k, v := range cfg.Auths {
		if normalizeRegistry(k) != normalizeRegistry(address) {
			continue
		}
		if v.Username != "" {
			return v.Username, v.Password, nil
		}
		raw, err := base64.StdEncoding.DecodeString(v.Auth)
		if err != nil {
			return "", "", fmt.Errorf("auths[%q].auth is not base64: %w", k, err)
		}
		u, p, ok := strings.Cut(string(raw), ":")
		if !ok {
			return "", "", fmt.Errorf("auths[%q].auth is not user:password", k)
		}
		return u, p, nil
	}
	return "", "", fmt.Errorf("%s has no auths entry for %q (credential helpers are not supported)", file, address)
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
	return registry.EncodeAuthConfig(cfg)
}

// registryAuthConfigFor returns the matching AuthConfig (for ImageBuild's
// AuthConfigs map and similar).
func (pd *providerData) registryAuthConfigFor(imageRef string) (registry.AuthConfig, bool, error) {
	host, err := registryHost(imageRef)
	if err != nil {
		return registry.AuthConfig{}, false, fmt.Errorf("parsing image reference %q: %w", imageRef, err)
	}
	for _, c := range pd.RegistryAuth {
		if normalizeRegistry(c.Address) == normalizeRegistry(host) {
			return registry.AuthConfig{Username: c.Username, Password: c.Password, ServerAddress: c.Address}, true, nil
		}
	}
	return registry.AuthConfig{}, false, nil
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
	list, err := c.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return "", err
	}
	objs := make([]namedObject, 0, len(list))
	for _, n := range list {
		objs = append(objs, namedObject{ID: n.ID, Name: n.Name})
	}
	return resolveNameOrID("network", ref, objs)
}

func resolveVolumeName(ctx context.Context, c *client.Client, ref string) (string, error) {
	v, err := c.VolumeInspect(ctx, ref)
	if err != nil {
		return "", err
	}
	return v.Name, nil
}

func resolveSecretID(ctx context.Context, c *client.Client, ref string) (string, error) {
	list, err := c.SecretList(ctx, swarm.SecretListOptions{})
	if err != nil {
		return "", err
	}
	objs := make([]namedObject, 0, len(list))
	for _, s := range list {
		objs = append(objs, namedObject{ID: s.ID, Name: s.Spec.Name})
	}
	return resolveNameOrID("secret", ref, objs)
}

func resolveConfigID(ctx context.Context, c *client.Client, ref string) (string, error) {
	list, err := c.ConfigList(ctx, swarm.ConfigListOptions{})
	if err != nil {
		return "", err
	}
	objs := make([]namedObject, 0, len(list))
	for _, s := range list {
		objs = append(objs, namedObject{ID: s.ID, Name: s.Spec.Name})
	}
	return resolveNameOrID("config", ref, objs)
}

func resolveServiceID(ctx context.Context, c *client.Client, ref string) (string, error) {
	list, err := c.ServiceList(ctx, swarm.ServiceListOptions{})
	if err != nil {
		return "", err
	}
	objs := make([]namedObject, 0, len(list))
	for _, s := range list {
		objs = append(objs, namedObject{ID: s.ID, Name: s.Spec.Name})
	}
	return resolveNameOrID("service", ref, objs)
}

// resolveNodeID matches a node ID, hostname or node name.
func resolveNodeID(ctx context.Context, c *client.Client, ref string) (string, error) {
	list, err := c.NodeList(ctx, swarm.NodeListOptions{})
	if err != nil {
		return "", err
	}
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
func labelFilter(labels map[string]string) filters.Args {
	args := filters.NewArgs()
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
	_, err := c.VolumeInspect(ctx, name)
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
