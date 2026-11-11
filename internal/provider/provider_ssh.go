package provider

// Pure-Go SSH transport to a remote Docker daemon. The Docker client gets a
// DialContext that opens (or reuses) one SSH connection per distinct
// configuration and dials the remote daemon's unix socket through it with a
// "direct-streamlocal@openssh.com" channel. Nothing shells out to ssh.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	pschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	defaultSSHPort           = 22
	defaultSSHRemoteSocket   = "/var/run/docker.sock"
	defaultSSHConnectTimeout = 10 * time.Second
)

// dockerHomeDir is the home directory used for ~/.ssh and ~/.docker
// defaults. Tests replace it to stay hermetic.
var dockerHomeDir = os.UserHomeDir

// sshModel is the `ssh` block, shared by the provider block and by
// docker_swarm_node's per-node endpoint.
type sshModel struct {
	User                  types.String `tfsdk:"user"`
	Host                  types.String `tfsdk:"host"`
	Port                  types.Int64  `tfsdk:"port"`
	IdentityFile          types.String `tfsdk:"identity_file"`
	IdentityAgent         types.Bool   `tfsdk:"identity_agent"`
	Password              types.String `tfsdk:"password"`
	PasswordWO            types.String `tfsdk:"password_wo"`
	KnownHostsFile        types.String `tfsdk:"known_hosts_file"`
	StrictHostKeyChecking types.Bool   `tfsdk:"strict_host_key_checking"`
	ProxyJump             types.String `tfsdk:"proxy_jump"`
	ConnectTimeout        types.String `tfsdk:"connect_timeout"`
	RemoteSocket          types.String `tfsdk:"remote_socket"`
}

// sshAttrDocs holds one MarkdownDescription per ssh block attribute so the
// provider and resource variants of the block document the same thing.
var sshAttrDocs = map[string]string{
	"user": "SSH user. Default: the user in an `ssh://user@host` Docker host, else `USER`/`USERNAME` from the environment.",
	"host": "SSH server host name or IP. Default: the host of an `ssh://` Docker host (from `host`, the Docker context or `DOCKER_HOST`).",
	"port": "SSH server port. Default `22` (or the port in an `ssh://` Docker host).",
	"identity_file": "Private key used for public-key authentication: PEM/OpenSSH content or a path (`~` expands). Passphrase-protected keys " +
		"are not supported; load them into an agent and use `identity_agent`. When unset, `~/.ssh/id_ed25519`, `~/.ssh/id_ecdsa` and " +
		"`~/.ssh/id_rsa` are offered if they exist and are unencrypted.",
	"identity_agent": "Also offer the keys held by the running SSH agent: `SSH_AUTH_SOCK` on Linux/macOS, the Windows OpenSSH agent " +
		"named pipe `\\\\.\\pipe\\openssh-ssh-agent` on Windows (or `SSH_AUTH_SOCK` when it names a pipe). Default `false`.",
	"password":    "SSH password (password and keyboard-interactive authentication). Conflicts with `password_wo`.",
	"password_wo": "SSH password, write-only variant of `password`. Conflicts with `password`.",
	"known_hosts_file": "OpenSSH `known_hosts` file used to verify the server's host key (and every `proxy_jump` hop). " +
		"Default `~/.ssh/known_hosts`.",
	"strict_host_key_checking": "Reject servers whose host key is missing from, or does not match, `known_hosts_file`. Default `true`. " +
		"`false` accepts any host key (insecure; for throwaway hosts only).",
	"proxy_jump": "Jump host(s) to reach the server through, OpenSSH `ProxyJump` style: `user@host:port`, or several hops separated by " +
		"commas (`user@bastion:22,user@inner:2222`), dialled in order. User defaults to `user`, port to `22`. Each hop is authenticated " +
		"with the same credentials and checked against the same `known_hosts_file`.",
	"connect_timeout": "Timeout for the TCP connection plus SSH handshake of each hop, as a Go duration (`10s`, `1m`). Default `10s`.",
	"remote_socket":   "Path of the Docker daemon's unix socket on the remote host. Default `" + defaultSSHRemoteSocket + "`.",
}

const sshBlockDoc = "Reach the Docker daemon over SSH in pure Go (no `ssh` binary is run): the provider opens one SSH connection, " +
	"reuses it for every API call, and forwards each call to the remote daemon's unix socket. Applies when the Docker host is " +
	"`ssh://user@host[:port]` (from `host`, the Docker context or `DOCKER_HOST`), or when `host` is unset and this block names a `host`."

func sshPasswordConflict(other string) []validator.String {
	return []validator.String{stringvalidator.ConflictsWith(path.MatchRelative().AtParent().AtName(other))}
}

// sshProviderBlockSchema is the provider's ssh block.
func sshProviderBlockSchema() pschema.SingleNestedBlock {
	str := func(name string, sensitive bool, v []validator.String) pschema.StringAttribute {
		return pschema.StringAttribute{Optional: true, Sensitive: sensitive, Validators: v, MarkdownDescription: sshAttrDocs[name]}
	}
	return pschema.SingleNestedBlock{
		MarkdownDescription: sshBlockDoc + " Provider configuration is never written to state, so `password` and `password_wo` " +
			"behave the same here; `password_wo` exists for symmetry with resources.",
		Attributes: map[string]pschema.Attribute{
			"user":                     str("user", false, nil),
			"host":                     str("host", false, nil),
			"port":                     pschema.Int64Attribute{Optional: true, MarkdownDescription: sshAttrDocs["port"]},
			"identity_file":            str("identity_file", true, nil),
			"identity_agent":           pschema.BoolAttribute{Optional: true, MarkdownDescription: sshAttrDocs["identity_agent"]},
			"password":                 str("password", true, sshPasswordConflict("password_wo")),
			"password_wo":              str("password_wo", true, sshPasswordConflict("password")),
			"known_hosts_file":         str("known_hosts_file", false, nil),
			"strict_host_key_checking": pschema.BoolAttribute{Optional: true, MarkdownDescription: sshAttrDocs["strict_host_key_checking"]},
			"proxy_jump":               str("proxy_jump", false, nil),
			"connect_timeout":          str("connect_timeout", false, nil),
			"remote_socket":            str("remote_socket", false, nil),
		},
	}
}

// sshResourceBlockSchema is the same block for a resource (docker_swarm_node).
// password_wo is a real write-only attribute there (Terraform >= 1.11).
func sshResourceBlockSchema(blockDoc string) rschema.SingleNestedBlock {
	str := func(name string, sensitive bool, v []validator.String) rschema.StringAttribute {
		return rschema.StringAttribute{Optional: true, Sensitive: sensitive, Validators: v, MarkdownDescription: sshAttrDocs[name]}
	}
	wo := str("password_wo", true, sshPasswordConflict("password"))
	wo.WriteOnly = true
	wo.MarkdownDescription += " Write-only: never stored in plan or state (needs Terraform >= 1.11). Because it is not in state, " +
		"destroy cannot use it; give destroy another way in (`identity_file`, `identity_agent` or `password`)."
	return rschema.SingleNestedBlock{
		MarkdownDescription: blockDoc,
		Attributes: map[string]rschema.Attribute{
			"user":                     str("user", false, nil),
			"host":                     str("host", false, nil),
			"port":                     rschema.Int64Attribute{Optional: true, MarkdownDescription: sshAttrDocs["port"]},
			"identity_file":            str("identity_file", true, nil),
			"identity_agent":           rschema.BoolAttribute{Optional: true, MarkdownDescription: sshAttrDocs["identity_agent"]},
			"password":                 str("password", true, sshPasswordConflict("password_wo")),
			"password_wo":              wo,
			"known_hosts_file":         str("known_hosts_file", false, nil),
			"strict_host_key_checking": rschema.BoolAttribute{Optional: true, MarkdownDescription: sshAttrDocs["strict_host_key_checking"]},
			"proxy_jump":               str("proxy_jump", false, nil),
			"connect_timeout":          str("connect_timeout", false, nil),
			"remote_socket":            str("remote_socket", false, nil),
		},
	}
}

// sshConfig is a resolved SSH endpoint. It is comparable and used as the key
// of the connection pool.
type sshConfig struct {
	User           string
	Host           string
	Port           int
	IdentityFile   string
	IdentityAgent  bool
	Password       string
	KnownHostsFile string
	Strict         bool
	ProxyJump      string
	ConnectTimeout time.Duration
	RemoteSocket   string
}

// address is host:port for dialling.
func (c sshConfig) address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// dockerHost is the ssh:// URL shown in messages.
func (c sshConfig) dockerHost() string {
	u := url.URL{Scheme: "ssh", User: url.User(c.User), Host: c.address()}
	return u.String()
}

func isSSHHost(h string) bool { return strings.HasPrefix(strings.ToLower(h), "ssh://") }

// parseSSHTarget parses `ssh://user@host:port` or `user@host:port` (any part
// but host optional).
func parseSSHTarget(s string) (user, host string, port int, err error) {
	raw := s
	if !isSSHHost(raw) {
		raw = "ssh://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, fmt.Errorf("parsing ssh target %q: %w", s, err)
	}
	if u.User != nil {
		user = u.User.Username()
		if _, has := u.User.Password(); has {
			return "", "", 0, fmt.Errorf("ssh target %q: passwords in the URL are not supported; use the ssh block's password", s)
		}
	}
	host = u.Hostname()
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port <= 0 || port > 65535 {
			return "", "", 0, fmt.Errorf("ssh target %q: bad port %q", s, p)
		}
	}
	if host == "" {
		return "", "", 0, fmt.Errorf("ssh target %q has no host", s)
	}
	return user, host, port, nil
}

// sshConfigFrom resolves the ssh block (m may be nil) on top of an ssh://
// Docker host (dockerHost may be "" or non-ssh). Unknown values are errors:
// the caller only builds endpoints at Configure or apply time.
func sshConfigFrom(m *sshModel, dockerHost string, getenv func(string) string) (*sshConfig, error) {
	c := &sshConfig{Strict: true, ConnectTimeout: defaultSSHConnectTimeout, RemoteSocket: defaultSSHRemoteSocket}
	if isSSHHost(dockerHost) {
		u, h, p, err := parseSSHTarget(dockerHost)
		if err != nil {
			return nil, err
		}
		c.User, c.Host, c.Port = u, h, p
	}
	if m != nil {
		var unknown []string
		str := func(name string, v types.String, dst *string) {
			if v.IsUnknown() {
				unknown = append(unknown, name)
				return
			}
			if s := v.ValueString(); s != "" {
				*dst = s
			}
		}
		str("user", m.User, &c.User)
		str("host", m.Host, &c.Host)
		str("identity_file", m.IdentityFile, &c.IdentityFile)
		str("password", m.Password, &c.Password)
		str("password_wo", m.PasswordWO, &c.Password)
		str("known_hosts_file", m.KnownHostsFile, &c.KnownHostsFile)
		str("proxy_jump", m.ProxyJump, &c.ProxyJump)
		str("remote_socket", m.RemoteSocket, &c.RemoteSocket)
		var timeout string
		str("connect_timeout", m.ConnectTimeout, &timeout)
		if m.Port.IsUnknown() {
			unknown = append(unknown, "port")
		} else if !m.Port.IsNull() {
			c.Port = int(m.Port.ValueInt64())
		}
		if m.IdentityAgent.IsUnknown() {
			unknown = append(unknown, "identity_agent")
		} else if !m.IdentityAgent.IsNull() {
			c.IdentityAgent = m.IdentityAgent.ValueBool()
		}
		if m.StrictHostKeyChecking.IsUnknown() {
			unknown = append(unknown, "strict_host_key_checking")
		} else if !m.StrictHostKeyChecking.IsNull() {
			c.Strict = m.StrictHostKeyChecking.ValueBool()
		}
		if len(unknown) > 0 {
			return nil, fmt.Errorf("ssh.%s not known yet; set literal values", strings.Join(unknown, ", ssh."))
		}
		if timeout != "" {
			d, err := time.ParseDuration(timeout)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("ssh.connect_timeout %q is not a positive Go duration", timeout)
			}
			c.ConnectTimeout = d
		}
	}
	if c.Host == "" {
		return nil, errors.New("no SSH host: set ssh.host or use an ssh://user@host Docker host")
	}
	if c.Port == 0 {
		c.Port = defaultSSHPort
	}
	if c.Port < 0 || c.Port > 65535 {
		return nil, fmt.Errorf("ssh.port %d is out of range", c.Port)
	}
	if c.User == "" {
		c.User = getenv("USER")
	}
	if c.User == "" {
		c.User = getenv("USERNAME")
	}
	if c.User == "" {
		return nil, errors.New("no SSH user: set ssh.user or use an ssh://user@host Docker host")
	}
	if c.ProxyJump != "" {
		if _, err := c.hops(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// sshHop is one connection in the chain (jump hosts, then the target).
type sshHop struct {
	User string
	Addr string
}

func (c sshConfig) hops() ([]sshHop, error) {
	var out []sshHop
	if c.ProxyJump != "" {
		for _, part := range strings.Split(c.ProxyJump, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			u, h, p, err := parseSSHTarget(part)
			if err != nil {
				return nil, fmt.Errorf("ssh.proxy_jump: %w", err)
			}
			if u == "" {
				u = c.User
			}
			if p == 0 {
				p = defaultSSHPort
			}
			out = append(out, sshHop{User: u, Addr: net.JoinHostPort(h, strconv.Itoa(p))})
		}
	}
	return append(out, sshHop{User: c.User, Addr: c.address()}), nil
}

// ---------------------------------------------------------------------------
// Authentication and host keys

func homePath(parts ...string) string {
	home, err := dockerHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(append([]string{home}, parts...)...)
}

func parseSigner(b []byte, what string) (ssh.Signer, error) {
	s, err := ssh.ParsePrivateKey(b)
	var pm *ssh.PassphraseMissingError
	if errors.As(err, &pm) {
		return nil, fmt.Errorf("%s is passphrase-protected; load it into an SSH agent and set identity_agent = true", what)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", what, err)
	}
	return s, nil
}

// authMethods builds the auth methods. The returned closer releases the
// agent connection and must be called once the handshakes are done.
func (c sshConfig) authMethods(ctx context.Context) ([]ssh.AuthMethod, func(), error) {
	closer := func() {}
	var signers []ssh.Signer
	if c.IdentityFile != "" {
		var b []byte
		var err error
		what := "ssh.identity_file"
		if strings.Contains(c.IdentityFile, "-----BEGIN") {
			b = []byte(c.IdentityFile)
		} else {
			what = "ssh.identity_file " + c.IdentityFile
			b, err = os.ReadFile(expandHome(c.IdentityFile))
			if err != nil {
				return nil, closer, fmt.Errorf("reading %s: %w", what, err)
			}
		}
		s, err := parseSigner(b, what)
		if err != nil {
			return nil, closer, err
		}
		signers = append(signers, s)
	} else {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			p := homePath(".ssh", name)
			if p == "" {
				break
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if s, err := ssh.ParsePrivateKey(b); err == nil {
				signers = append(signers, s)
			}
		}
	}
	if c.IdentityAgent {
		conn, err := sshAgentConn(ctx)
		if err != nil {
			return nil, closer, fmt.Errorf("ssh.identity_agent: %w", err)
		}
		closer = func() { _ = conn.Close() }
		as, err := agent.NewClient(conn).Signers()
		if err != nil {
			closer()
			return nil, func() {}, fmt.Errorf("ssh.identity_agent: listing agent keys: %w", err)
		}
		signers = append(signers, as...)
	}
	var methods []ssh.AuthMethod
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if c.Password != "" {
		pw := c.Password
		methods = append(methods, ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				ans := make([]string, len(questions))
				for i := range ans {
					ans[i] = pw
				}
				return ans, nil
			}))
	}
	if len(methods) == 0 {
		closer()
		return nil, func() {}, errors.New("no SSH credentials: set ssh.identity_file, ssh.identity_agent or ssh.password")
	}
	return methods, closer, nil
}

func (c sshConfig) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if !c.Strict {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // explicit strict_host_key_checking = false
	}
	file := c.KnownHostsFile
	if file == "" {
		file = homePath(".ssh", "known_hosts")
	} else {
		file = expandHome(file)
	}
	if file == "" {
		return nil, errors.New("cannot locate ~/.ssh/known_hosts; set ssh.known_hosts_file or strict_host_key_checking = false")
	}
	cb, err := knownhosts.New(file)
	if err != nil {
		return nil, fmt.Errorf("loading known_hosts %s (set ssh.known_hosts_file, or strict_host_key_checking = false): %w", file, err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) > 0 {
				return fmt.Errorf("host key mismatch for %s: the %s key offered does not match %s line %d (possible man-in-the-middle)",
					hostname, key.Type(), ke.Want[0].Filename, ke.Want[0].Line)
			}
			return fmt.Errorf("host %s is not in %s (add it with ssh-keyscan, or set strict_host_key_checking = false)", hostname, file)
		}
		return err
	}, nil
}

// ---------------------------------------------------------------------------
// Connection chain and pool

// sshChain is an open connection to the target plus the jump connections
// it rides on, innermost last.
type sshChain struct {
	clients []*ssh.Client
}

func (ch *sshChain) target() *ssh.Client { return ch.clients[len(ch.clients)-1] }

func (ch *sshChain) Close() {
	for i := len(ch.clients) - 1; i >= 0; i-- {
		_ = ch.clients[i].Close()
	}
}

// connect opens the chain: TCP to the first hop, then each later hop through
// a direct-tcpip channel on the previous one.
func (c sshConfig) connect(ctx context.Context) (*sshChain, error) {
	hops, err := c.hops()
	if err != nil {
		return nil, err
	}
	auth, closeAgent, err := c.authMethods(ctx)
	if err != nil {
		return nil, err
	}
	defer closeAgent()
	hk, err := c.hostKeyCallback()
	if err != nil {
		return nil, err
	}
	chain := &sshChain{}
	for i, hop := range hops {
		var conn net.Conn
		if i == 0 {
			d := net.Dialer{Timeout: c.ConnectTimeout}
			conn, err = d.DialContext(ctx, "tcp", hop.Addr)
		} else {
			conn, err = chain.target().Dial("tcp", hop.Addr)
		}
		if err != nil {
			chain.Close()
			return nil, &sshDialError{Addr: hop.Addr, Err: err}
		}
		cl, err := sshHandshake(ctx, conn, hop, auth, hk, c.ConnectTimeout)
		if err != nil {
			_ = conn.Close()
			chain.Close()
			return nil, fmt.Errorf("ssh: %s@%s: %w", hop.User, hop.Addr, err)
		}
		chain.clients = append(chain.clients, cl)
	}
	return chain, nil
}

// sshDialError is a failed TCP connection to an SSH host or jump host. The
// preflight (preflight.go) classifies it as daemonSSHUnreachable.
type sshDialError struct {
	Addr string
	Err  error
}

func (e *sshDialError) Error() string { return fmt.Sprintf("ssh: connecting to %s: %v", e.Addr, e.Err) }
func (e *sshDialError) Unwrap() error { return e.Err }

func sshHandshake(ctx context.Context, conn net.Conn, hop sshHop, auth []ssh.AuthMethod, hk ssh.HostKeyCallback, timeout time.Duration) (*ssh.Client, error) {
	// Channel connections (jump hops) do not support deadlines; a timer that
	// closes the connection bounds the handshake for both kinds.
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-done:
		case <-ctx.Done():
			_ = conn.Close()
		case <-t.C:
			_ = conn.Close()
		}
	}()
	cc, chans, reqs, err := ssh.NewClientConn(conn, hop.Addr, &ssh.ClientConfig{
		User: hop.User, Auth: auth, HostKeyCallback: hk, Timeout: timeout,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

// sshPool keeps one open chain per sshConfig for the life of the process.
type sshConnPool struct {
	mu    sync.Mutex
	conns map[sshConfig]*sshChain
}

var sshPool = &sshConnPool{conns: map[sshConfig]*sshChain{}}

func (p *sshConnPool) get(ctx context.Context, c sshConfig) (*sshChain, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.conns[c]; ok {
		return ch, nil
	}
	ch, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	p.conns[c] = ch
	go func() {
		_ = ch.target().Wait()
		p.drop(c, ch)
	}()
	return ch, nil
}

func (p *sshConnPool) drop(c sshConfig, ch *sshChain) {
	p.mu.Lock()
	if cur, ok := p.conns[c]; ok && cur == ch {
		delete(p.conns, c)
	}
	p.mu.Unlock()
	ch.Close()
}

// closeAll closes every pooled connection (tests).
func (p *sshConnPool) closeAll() {
	p.mu.Lock()
	all := p.conns
	p.conns = map[sshConfig]*sshChain{}
	p.mu.Unlock()
	for _, ch := range all {
		ch.Close()
	}
}

// dialContext is the Docker client's DialContext: network and addr are the
// dummy HTTP host and ignored; every call opens a channel to RemoteSocket.
func (c sshConfig) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	ch, err := sshPool.get(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.dockerHost(), err)
	}
	conn, err := ch.target().Dial("unix", c.RemoteSocket)
	if err == nil {
		return conn, nil
	}
	// The pooled connection may have died; reconnect once.
	sshPool.drop(c, ch)
	ch, err2 := sshPool.get(ctx, c)
	if err2 != nil {
		return nil, fmt.Errorf("%s: dialling %s: %v; reconnecting: %w", c.dockerHost(), c.RemoteSocket, err, err2)
	}
	conn, err = ch.target().Dial("unix", c.RemoteSocket)
	if err != nil {
		return nil, fmt.Errorf("%s: dialling remote socket %s: %w", c.dockerHost(), c.RemoteSocket, err)
	}
	return conn, nil
}
