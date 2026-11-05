package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	testSSHUser     = "alice"
	testSSHPassword = "s3cret"
)

// fakeEngine serves the two endpoints the SSH tests call.
func fakeEngine(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = io.WriteString(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/version"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Version":"ssh-fake-engine","ApiVersion":"1.47","Os":"linux"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testSSHServer is an in-process SSH server. direct-streamlocal channels go
// to engineAddr (whatever socket path is asked for); direct-tcpip channels
// are dialled for real, which makes it usable as a jump host.
type testSSHServer struct {
	Addr        string
	Port        int
	HostKey     ssh.Signer
	conns       atomic.Int32
	mu          sync.Mutex
	socketPaths []string
	tcpTargets  []string
}

func newHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newClientKey returns a client key as OpenSSH PEM plus its public key.
func newClientKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(blk)), sp
}

func startTestSSHServer(t *testing.T, engineAddr string, authorized ssh.PublicKey) *testSSHServer {
	t.Helper()
	s := &testSSHServer{HostKey: newHostKey(t)}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == testSSHUser && string(pw) == testSSHPassword {
				return nil, nil
			}
			return nil, fmt.Errorf("bad password for %s", c.User())
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if authorized != nil && c.User() == testSSHUser && string(k.Marshal()) == string(authorized.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	cfg.AddHostKey(s.HostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s.Addr = ln.Addr().String()
	s.Port = ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(nc, cfg, engineAddr)
		}
	}()
	return s
}

func (s *testSSHServer) serve(nc net.Conn, cfg *ssh.ServerConfig, engineAddr string) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	defer sc.Close()
	s.conns.Add(1)
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		var target string
		switch nch.ChannelType() {
		case "direct-streamlocal@openssh.com":
			var p struct {
				SocketPath string
				Reserved0  string
				Reserved1  uint32
			}
			if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
				_ = nch.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			s.mu.Lock()
			s.socketPaths = append(s.socketPaths, p.SocketPath)
			s.mu.Unlock()
			target = engineAddr
		case "direct-tcpip":
			var p struct {
				Host     string
				Port     uint32
				OrigHost string
				OrigPort uint32
			}
			if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
				_ = nch.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			target = net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
			s.mu.Lock()
			s.tcpTargets = append(s.tcpTargets, target)
			s.mu.Unlock()
		default:
			_ = nch.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		if target == "" {
			_ = nch.Reject(ssh.ConnectionFailed, "no engine")
			continue
		}
		up, err := net.Dial("tcp", target)
		if err != nil {
			_ = nch.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			_ = up.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() {
			_, _ = io.Copy(ch, up)
			_ = ch.CloseWrite()
			_ = ch.Close()
		}()
		go func() {
			_, _ = io.Copy(up, ch)
			if tc, ok := up.(*net.TCPConn); ok {
				_ = tc.CloseWrite()
			}
		}()
	}
}

// writeKnownHosts writes a known_hosts file listing each server's key.
func writeKnownHosts(t *testing.T, entries map[string]ssh.PublicKey) string {
	t.Helper()
	var b strings.Builder
	for addr, k := range entries {
		b.WriteString(knownhosts.Line([]string{knownhosts.Normalize(addr)}, k) + "\n")
	}
	f := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(f, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// hermeticSSH points ~ at an empty temp dir and drops pooled connections.
func hermeticSSH(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	old := dockerHomeDir
	dockerHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() {
		dockerHomeDir = old
		sshPool.closeAll()
	})
	return home
}

func sshPingVersion(t *testing.T, sc *sshConfig) (string, error) {
	t.Helper()
	c, err := newDockerClient(endpointConfig{SSH: sc, APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := c.Ping(ctx); err != nil {
		return "", err
	}
	v, err := c.ServerVersion(ctx)
	return v.Version, err
}

func TestSSHPasswordAuth(t *testing.T) {
	hermeticSSH(t)
	eng := fakeEngine(t)
	srv := startTestSSHServer(t, eng.Listener.Addr().String(), nil)
	kh := writeKnownHosts(t, map[string]ssh.PublicKey{srv.Addr: srv.HostKey.PublicKey()})
	sc := &sshConfig{User: testSSHUser, Host: "127.0.0.1", Port: srv.Port, Password: testSSHPassword, KnownHostsFile: kh,
		Strict: true, ConnectTimeout: 5 * time.Second, RemoteSocket: "/run/custom.sock"}
	v, err := sshPingVersion(t, sc)
	if err != nil || v != "ssh-fake-engine" {
		t.Fatalf("got %q, %v", v, err)
	}
	// Second client, same config: the pooled SSH connection is reused.
	if _, err := sshPingVersion(t, sc); err != nil {
		t.Fatal(err)
	}
	if n := srv.conns.Load(); n != 1 {
		t.Errorf("want 1 SSH connection (reused), got %d", n)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.socketPaths) == 0 || srv.socketPaths[0] != "/run/custom.sock" {
		t.Errorf("remote socket paths: %v", srv.socketPaths)
	}
}

func TestSSHWrongPassword(t *testing.T) {
	hermeticSSH(t)
	eng := fakeEngine(t)
	srv := startTestSSHServer(t, eng.Listener.Addr().String(), nil)
	sc := &sshConfig{User: testSSHUser, Host: "127.0.0.1", Port: srv.Port, Password: "nope",
		ConnectTimeout: 5 * time.Second, RemoteSocket: defaultSSHRemoteSocket}
	if _, err := sshPingVersion(t, sc); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("want auth failure, got %v", err)
	}
}

func TestSSHKeyAuth(t *testing.T) {
	hermeticSSH(t)
	keyPEM, pub := newClientKey(t)
	eng := fakeEngine(t)
	srv := startTestSSHServer(t, eng.Listener.Addr().String(), pub)
	// Key as PEM content, host key checking off.
	sc := &sshConfig{User: testSSHUser, Host: "127.0.0.1", Port: srv.Port, IdentityFile: keyPEM,
		ConnectTimeout: 5 * time.Second, RemoteSocket: defaultSSHRemoteSocket}
	if v, err := sshPingVersion(t, sc); err != nil || v != "ssh-fake-engine" {
		t.Fatalf("pem key: %q, %v", v, err)
	}
	// Key as a path.
	f := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(f, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	sc2 := *sc
	sc2.IdentityFile = f
	if _, err := sshPingVersion(t, &sc2); err != nil {
		t.Fatalf("key file: %v", err)
	}
}

func TestSSHDefaultIdentityFile(t *testing.T) {
	home := hermeticSSH(t)
	keyPEM, pub := newClientKey(t)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := fakeEngine(t)
	srv := startTestSSHServer(t, eng.Listener.Addr().String(), pub)
	// known_hosts defaults to ~/.ssh/known_hosts too.
	kh := writeKnownHosts(t, map[string]ssh.PublicKey{srv.Addr: srv.HostKey.PublicKey()})
	b, _ := os.ReadFile(kh)
	if err := os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	sc := &sshConfig{User: testSSHUser, Host: "127.0.0.1", Port: srv.Port, Strict: true,
		ConnectTimeout: 5 * time.Second, RemoteSocket: defaultSSHRemoteSocket}
	if _, err := sshPingVersion(t, sc); err != nil {
		t.Fatal(err)
	}
}

func TestSSHKnownHostsMismatchRejected(t *testing.T) {
	hermeticSSH(t)
	eng := fakeEngine(t)
	srv := startTestSSHServer(t, eng.Listener.Addr().String(), nil)
	other := newHostKey(t)
	kh := writeKnownHosts(t, map[string]ssh.PublicKey{srv.Addr: other.PublicKey()})
	sc := &sshConfig{User: testSSHUser, Host: "127.0.0.1", Port: srv.Port, Password: testSSHPassword, KnownHostsFile: kh,
		Strict: true, ConnectTimeout: 5 * time.Second, RemoteSocket: defaultSSHRemoteSocket}
	_, err := sshPingVersion(t, sc)
	if err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("want host key mismatch, got %v", err)
	}
	// Unknown host (empty known_hosts) is rejected too when strict.
	sc.KnownHostsFile = writeKnownHosts(t, nil)
	if _, err := sshPingVersion(t, sc); err == nil || !strings.Contains(err.Error(), "is not in") {
		t.Fatalf("want unknown host, got %v", err)
	}
	// Missing known_hosts file is an error when strict.
	sc.KnownHostsFile = filepath.Join(t.TempDir(), "absent")
	if _, err := sshPingVersion(t, sc); err == nil || !strings.Contains(err.Error(), "known_hosts") {
		t.Fatalf("want missing known_hosts error, got %v", err)
	}
	// Not strict: accepted.
	sc.Strict = false
	if _, err := sshPingVersion(t, sc); err != nil {
		t.Fatalf("non-strict: %v", err)
	}
}

func TestSSHProxyJump(t *testing.T) {
	hermeticSSH(t)
	eng := fakeEngine(t)
	target := startTestSSHServer(t, eng.Listener.Addr().String(), nil)
	jump := startTestSSHServer(t, "", nil)
	kh := writeKnownHosts(t, map[string]ssh.PublicKey{
		target.Addr: target.HostKey.PublicKey(),
		jump.Addr:   jump.HostKey.PublicKey(),
	})
	sc := &sshConfig{User: testSSHUser, Host: "127.0.0.1", Port: target.Port, Password: testSSHPassword, KnownHostsFile: kh,
		Strict: true, ProxyJump: testSSHUser + "@" + jump.Addr, ConnectTimeout: 5 * time.Second, RemoteSocket: defaultSSHRemoteSocket}
	if v, err := sshPingVersion(t, sc); err != nil || v != "ssh-fake-engine" {
		t.Fatalf("got %q, %v", v, err)
	}
	jump.mu.Lock()
	targets := append([]string(nil), jump.tcpTargets...)
	jump.mu.Unlock()
	if len(targets) != 1 || targets[0] != target.Addr {
		t.Errorf("jump host forwarded to %v, want [%s]", targets, target.Addr)
	}
	if target.conns.Load() != 1 || jump.conns.Load() != 1 {
		t.Errorf("connections: target %d jump %d", target.conns.Load(), jump.conns.Load())
	}

	// A jump host with an unexpected key is rejected even though the target is fine.
	sshPool.closeAll()
	kh2 := writeKnownHosts(t, map[string]ssh.PublicKey{
		target.Addr: target.HostKey.PublicKey(),
		jump.Addr:   newHostKey(t).PublicKey(),
	})
	sc2 := *sc
	sc2.KnownHostsFile = kh2
	if _, err := sshPingVersion(t, &sc2); err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("want jump host key mismatch, got %v", err)
	}
}

func TestSSHAgentAuth(t *testing.T) {
	hermeticSSH(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kr := agent.NewKeyring()
	if err := kr.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	signers, _ := kr.Signers()
	sock := listenTestAgent(t, kr)
	t.Setenv("SSH_AUTH_SOCK", sock)
	eng := fakeEngine(t)
	srv := startTestSSHServer(t, eng.Listener.Addr().String(), signers[0].PublicKey())
	sc := &sshConfig{User: testSSHUser, Host: "127.0.0.1", Port: srv.Port, IdentityAgent: true,
		ConnectTimeout: 5 * time.Second, RemoteSocket: defaultSSHRemoteSocket}
	if _, err := sshPingVersion(t, sc); err != nil {
		t.Fatal(err)
	}
}

func TestSSHConfigFrom(t *testing.T) {
	getenv := func(k string) string { return map[string]string{"USER": "envuser"}[k] }
	c, err := sshConfigFrom(nil, "ssh://bob@10.0.0.5:2222", getenv)
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "bob" || c.Host != "10.0.0.5" || c.Port != 2222 || !c.Strict || c.RemoteSocket != defaultSSHRemoteSocket ||
		c.ConnectTimeout != defaultSSHConnectTimeout {
		t.Errorf("from host: %+v", c)
	}
	if got := c.dockerHost(); got != "ssh://bob@10.0.0.5:2222" {
		t.Errorf("dockerHost %q", got)
	}
	m := &sshModel{
		User: types.StringNull(), Host: types.StringValue("h2"), Port: types.Int64Null(),
		IdentityFile: types.StringNull(), IdentityAgent: types.BoolValue(true), Password: types.StringNull(),
		PasswordWO: types.StringValue("wo"), KnownHostsFile: types.StringNull(), StrictHostKeyChecking: types.BoolValue(false),
		ProxyJump: types.StringValue("j1, root@j2:2200"), ConnectTimeout: types.StringValue("3s"), RemoteSocket: types.StringValue("/s"),
	}
	c, err = sshConfigFrom(m, "", getenv)
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "envuser" || c.Host != "h2" || c.Port != 22 || c.Password != "wo" || c.Strict || !c.IdentityAgent ||
		c.ConnectTimeout != 3*time.Second || c.RemoteSocket != "/s" {
		t.Errorf("from block: %+v", c)
	}
	hops, err := c.hops()
	if err != nil || len(hops) != 3 || hops[0] != (sshHop{"envuser", "j1:22"}) || hops[1] != (sshHop{"root", "j2:2200"}) || hops[2].Addr != "h2:22" {
		t.Errorf("hops %+v %v", hops, err)
	}
	for _, bad := range []struct {
		m    *sshModel
		host string
	}{
		{nil, ""}, // no host
		{&sshModel{Host: types.StringUnknown()}, ""},
		{&sshModel{Host: types.StringValue("h"), ConnectTimeout: types.StringValue("soon")}, ""},
		{&sshModel{Host: types.StringValue("h"), ProxyJump: types.StringValue("u@:22")}, ""},
		{nil, "ssh://u:pw@h"},
	} {
		if _, err := sshConfigFrom(bad.m, bad.host, getenv); err == nil {
			t.Errorf("want error for %+v %q", bad.m, bad.host)
		}
	}
	if _, err := sshConfigFrom(nil, "ssh://h", func(string) string { return "" }); err == nil {
		t.Error("want error without any user")
	}
}

func TestResolveEndpointSSH(t *testing.T) {
	env := map[string]string{"DOCKER_CONFIG": t.TempDir(), "USER": "u"}
	getenv := func(k string) string { return env[k] }
	var diags diag.Diagnostics
	// ssh:// host with an ssh block adding credentials.
	m := providerModel{Host: types.StringValue("ssh://deploy@node1:2022"), SSH: &sshModel{Password: types.StringValue("pw")}}
	ep := resolveEndpoint(m, getenv, &diags)
	if diags.HasError() || ep.SSH == nil || ep.SSH.User != "deploy" || ep.SSH.Port != 2022 || ep.SSH.Password != "pw" {
		t.Fatalf("ssh host: %+v %v", ep.SSH, diags)
	}
	// ssh block alone, beating DOCKER_HOST.
	env["DOCKER_HOST"] = "unix:///var/run/docker.sock"
	ep = resolveEndpoint(providerModel{SSH: &sshModel{Host: types.StringValue("node2")}}, getenv, &diags)
	if diags.HasError() || ep.SSH == nil || ep.Host != "ssh://u@node2:22" {
		t.Fatalf("ssh block: %+v %v", ep, diags)
	}
	// ssh block without a host and a non-ssh host is an error.
	resolveEndpoint(providerModel{Host: types.StringValue("tcp://x:2376"), SSH: &sshModel{}}, getenv, &diags)
	if !diags.HasError() {
		t.Fatal("want error for ssh block with tcp host")
	}
}
