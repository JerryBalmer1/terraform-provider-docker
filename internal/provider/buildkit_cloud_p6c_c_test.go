package provider

// Pass 6c Lane C: Docker Build Cloud (buildkit_cloud.go,
// buildx_builder_cloud.go) against a fake auth server and a fake BuildKit
// proxy (plaintext gRPC on loopback).

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	controlapi "github.com/moby/buildkit/api/services/control"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	p6cCloudUser     = "p6c-hub-user-7f3a"
	p6cCloudPassword = "p6c-hub-secret-91c2e"
	p6cCloudToken    = "p6c-build-token-5d8b0"
)

// p6cCloudAuth is a fake auth.docker.io /token endpoint.
type p6cCloudAuth struct {
	srv      *httptest.Server
	mu       sync.Mutex
	queries  []string
	fails    int32 // answer 503 this many times first
	calls    atomic.Int32
	identity string // expected secret, when not p6cCloudPassword
}

func newP6cCloudAuth(t *testing.T) *p6cCloudAuth {
	t.Helper()
	a := &p6cCloudAuth{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := a.calls.Add(1)
		a.mu.Lock()
		a.queries = append(a.queries, r.URL.RawQuery)
		a.mu.Unlock()
		if n <= atomic.LoadInt32(&a.fails) {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		want := p6cCloudPassword
		if a.identity != "" {
			want = a.identity
		}
		u, p, ok := r.BasicAuth()
		if r.URL.Path != "/token" || !ok || u != p6cCloudUser || p != want {
			w.Header().Set("x-trace-id", "trace-p6c")
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q}`, p6cCloudToken)
	}))
	t.Cleanup(a.srv.Close)
	old := bkCloudAuthHost
	bkCloudAuthHost = a.srv.URL
	t.Cleanup(func() { bkCloudAuthHost = old })
	return a
}

// p6cCloudBK is a fake BuildKit proxy: the bkFake control API plus Solve,
// Status and Session, recording the metadata of every call.
type p6cCloudBK struct {
	bkFake
	mu        sync.Mutex
	md        []metadata.MD
	methods   []string
	platforms string
	exports   []string
}

func (f *p6cCloudBK) record(ctx context.Context, method string) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.mu.Lock()
	f.md = append(f.md, md)
	f.methods = append(f.methods, method)
	f.mu.Unlock()
}

func (f *p6cCloudBK) Solve(_ context.Context, req *controlapi.SolveRequest) (*controlapi.SolveResponse, error) {
	f.mu.Lock()
	f.platforms = req.FrontendAttrs["platform"]
	for _, e := range req.Exporters {
		f.exports = append(f.exports, e.Type)
	}
	f.mu.Unlock()
	return &controlapi.SolveResponse{ExporterResponse: map[string]string{
		"containerimage.digest": "sha256:" + strings.Repeat("a", 64),
	}}, nil
}

func (f *p6cCloudBK) Status(*controlapi.StatusRequest, grpc.ServerStreamingServer[controlapi.StatusResponse]) error {
	return nil
}

func (f *p6cCloudBK) Session(s grpc.BidiStreamingServer[controlapi.BytesMessage, controlapi.BytesMessage]) error {
	<-s.Context().Done()
	return nil
}

func newP6cCloudBK(t *testing.T) *p6cCloudBK {
	t.Helper()
	f := &p6cCloudBK{}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			f.record(ctx, info.FullMethod)
			return h(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			f.record(ss.Context(), info.FullMethod)
			return h(srv, ss)
		}))
	controlapi.RegisterControlServer(srv, f)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)
	oldA, oldP := bkCloudProxyAddress, bkCloudPlaintext
	bkCloudProxyAddress, bkCloudPlaintext = "tcp://"+l.Addr().String(), true
	t.Cleanup(func() { bkCloudProxyAddress, bkCloudPlaintext = oldA, oldP })
	return f
}

func p6cCloudPD() *providerData {
	return &providerData{RegistryAuth: []registryCredential{{Address: "docker.io", Username: p6cCloudUser, Password: p6cCloudPassword}}}
}

// p6cNoSecrets fails when any credential or token string appears in s.
func p6cNoSecrets(t *testing.T, where, s string) {
	t.Helper()
	basic := base64.StdEncoding.EncodeToString([]byte(p6cCloudUser + ":" + p6cCloudPassword))
	for _, secret := range []string{p6cCloudToken, p6cCloudPassword, p6cCloudUser, basic} {
		if strings.Contains(s, secret) {
			t.Errorf("%s contains a credential or token (%q)", where, secret[:6]+"...")
		}
	}
}

func TestBKCloudParseBuilder(t *testing.T) {
	for in, want := range map[string]string{"org/builder": "org/builder", "cloud://org/builder": "org/builder"} {
		if got, err := bkCloudBuilder(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"org", "/builder", "org/", "org/builder/extra", "./b", "../b", "org/.", "org/..", ""} {
		if _, err := bkCloudBuilder(in); err == nil || err.Error() != bkCloudFormatErrorText {
			t.Errorf("%q accepted: %v", in, err)
		}
	}
	n, err := bkCloudNode("", "cloud://Org/B")
	if err != nil || n.Builder != "cloud-org-b" || n.Endpoint != "cloud://Org/B" || n.Driver != bkDriverCloud {
		t.Errorf("node = %+v %v", n, err)
	}
	if r := imgSeamBuilder(&imageBuildModel{Builder: types.StringValue("org/b")}); r.Endpoint != "cloud://org/b" {
		t.Errorf("seam ref = %+v", r)
	}
	if r := imgSeamBuilder(&imageBuildModel{Builder: types.StringValue("cloud://org/b")}); r.Endpoint != "cloud://org/b" {
		t.Errorf("seam ref = %+v", r)
	}
}

func TestBKCloudUserContext(t *testing.T) {
	for want, env := range map[string][]string{
		"local": nil, "ci": {"CI=true"}, "github": {"CI=true", "GITHUB_ACTIONS=true"}, "gitlab": {"CI=true", "GITLAB_CI=true"},
		"jenkins": {"CI=true", "JENKINS_HOME=/var/jenkins"}, "buildkite": {"CI=true", "BUILDKITE=true"},
	} {
		if got := bkCloudUserContext(env); got != want {
			t.Errorf("%v: %s, want %s", env, got, want)
		}
	}
	h := bkCloudHeaders()
	if len(h) != 6 || h[0] != bkCloudHeaderVersion || h[1] != contractBuildxVersion {
		t.Errorf("headers = %v", h)
	}
}

func TestBKCloudLogin(t *testing.T) {
	ctx := context.Background()
	a := newP6cCloudAuth(t)
	tok, err := bkCloudLogin(ctx, p6cCloudPD(), "org/b")
	if err != nil || tok != p6cCloudToken {
		t.Fatalf("login: %q %v", tok, err)
	}
	if q := a.queries[0]; q != "service=build.docker.com&scope=builder:org/b:build" {
		t.Errorf("query = %s", q)
	}

	// Retries 503 (buildx's retry policy), then succeeds.
	a.calls.Store(0)
	atomic.StoreInt32(&a.fails, 2)
	if tok, err := bkCloudLogin(ctx, p6cCloudPD(), "org/b"); err != nil || tok != p6cCloudToken || a.calls.Load() != 3 {
		t.Errorf("retry: %q %v calls=%d", tok, err, a.calls.Load())
	}
	atomic.StoreInt32(&a.fails, 0)

	// An identity token replaces the password.
	a.identity = "p6c-identity-token"
	pd := &providerData{RegistryAuth: []registryCredential{{Address: "https://index.docker.io/v1/", Username: p6cCloudUser, Password: "x", IdentityToken: "p6c-identity-token"}}}
	if _, err := bkCloudLogin(ctx, pd, "org/b"); err != nil {
		t.Errorf("identity token: %v", err)
	}
	a.identity = ""

	// A config.json auths entry (base64 auth only) through the default docker config.
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{"auths":{"https://index.docker.io/v1/":{"auth":%q}}}`, base64.StdEncoding.EncodeToString([]byte(p6cCloudUser+":"+p6cCloudPassword)))
	if err := os.WriteFile(dir+"/config.json", []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	pd = &providerData{configAuth: newDockerConfigAuth(func(k string) string {
		if k == "DOCKER_CONFIG" {
			return dir
		}
		return ""
	}, false)}
	if tok, err := bkCloudLogin(ctx, pd, "org/b"); err != nil || tok != p6cCloudToken {
		t.Errorf("docker config auths: %q %v", tok, err)
	}

	// Wrong password: the error names the status and trace id, never the secret.
	bad := &providerData{RegistryAuth: []registryCredential{{Address: "docker.io", Username: p6cCloudUser, Password: "wrong-" + p6cCloudPassword}}}
	_, err = bkCloudLogin(ctx, bad, "org/b")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "trace-p6c") {
		t.Errorf("denied: %v", err)
	} else {
		p6cNoSecrets(t, "error", err.Error())
	}

	// No credential at all.
	if _, err := bkCloudLogin(ctx, &providerData{}, "org/b"); err == nil || !strings.Contains(err.Error(), "no Docker Hub credentials") {
		t.Errorf("anonymous: %v", err)
	}
}

func TestBKCloudTokenSource(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	old := bkCloudNow
	bkCloudNow = func() time.Time { return now }
	t.Cleanup(func() { bkCloudNow = old })
	n := 0
	src := bkCloudTokenSource(func(context.Context) (string, error) { n++; return fmt.Sprint("t", n), nil })
	a, _ := src(context.Background())
	now = now.Add(30 * time.Second)
	b, _ := src(context.Background())
	now = now.Add(61 * time.Second)
	c, _ := src(context.Background())
	if a != "t1" || b != "t1" || c != "t2" {
		t.Errorf("tokens %s %s %s", a, b, c)
	}
}

// TestBuildxBuilderCloud creates a cloud builder and builds two platforms
// through the seam against the fakes, then checks that every BuildKit call
// carried the bearer token and that no token or credential string reached
// the state, the diagnostics or the log (API-call entries included).
func TestBuildxBuilderCloud(t *testing.T) {
	var logBuf bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &logBuf)
	a := newP6cCloudAuth(t)
	bk := newP6cCloudBK(t)
	pd := p6cCloudPD()

	r := &buildxBuilderResource{pd: pd}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	m := bxbTestModel()
	m.Name = types.StringValue("cloudci")
	m.Driver = types.StringValue(bxbDriverCloud)
	m.Endpoint = types.StringValue("myorg/default")
	m.DockerContainer, m.TCPPort, m.BuildkitdConfigContent, m.BuildkitdFlags, m.Node = nil, types.Int64Null(), types.StringNull(), types.StringNull(), types.StringNull()
	m.Platform = types.ListNull(types.StringType)
	m.Bootstrap = types.BoolValue(true)
	m.Adopt = types.BoolNull()

	var vresp resource.ValidateConfigResponse
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: b53Config(t, sr, m)}, &vresp)
	if vresp.Diagnostics.HasError() {
		t.Fatalf("valid cloud builder rejected: %v", vresp.Diagnostics)
	}
	bad := m
	bad.Endpoint = types.StringValue("myorg")
	bad.TCPPort = types.Int64Value(1)
	bad.Remote = &bxbRemoteModel{CACert: types.StringNull(), Cert: types.StringNull(), Key: types.StringNull(), ServerName: types.StringNull()}
	vresp = resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: b53Config(t, sr, bad)}, &vresp)
	if vresp.Diagnostics.ErrorsCount() != 3 {
		t.Errorf("invalid cloud builder: %v", vresp.Diagnostics)
	}
	noEP := m
	noEP.Endpoint = types.StringNull()
	vresp = resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: b53Config(t, sr, noEP)}, &vresp)
	if !vresp.Diagnostics.HasError() {
		t.Error("cloud builder without endpoint accepted")
	}

	cresp := resource.CreateResponse{State: tfState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan(t, sr, m)}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	var st bxbModel
	cresp.State.Get(ctx, &st)
	if st.ID.ValueString() != "cloud://myorg/default" || st.Endpoint.ValueString() != "myorg/default" ||
		!st.Platforms.Equal(stringListValue([]string{"linux/amd64", "linux/arm64"}, false)) || st.BuildkitVersion.ValueString() != "v0.34.2" {
		t.Errorf("state = %+v", st)
	}
	if !slices.Contains(a.queries, "service=build.docker.com&scope=builder:myorg/default:build") {
		t.Errorf("auth queries = %v", a.queries)
	}

	// Read reports the builder again.
	rresp := resource.ReadResponse{State: cresp.State}
	r.Read(ctx, resource.ReadRequest{State: cresp.State}, &rresp)
	if rresp.Diagnostics.HasError() || rresp.Diagnostics.WarningsCount() > 0 {
		t.Errorf("read: %v", rresp.Diagnostics)
	}

	// A two-platform build on the cloud builder through the seam (push only:
	// nothing is loaded into a daemon).
	res, err := laneEBuildKit{}.Build(ctx, pd, seamBuildRequest{
		ContextDir: "https://github.com/docker/buildx.git#v0.38.2",
		Platforms:  []string{"linux/amd64", "linux/arm64"},
		Outputs:    seamBuildOutputs{Names: []string{"docker.io/myorg/app:1"}, Push: true},
		Builder:    imgSeamBuilder(&imageBuildModel{Builder: st.ID}),
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if bk.platforms != "linux/amd64,linux/arm64" || len(res.Platforms) != 2 || !strings.HasPrefix(res.Digest, "sha256:") {
		t.Errorf("build: platforms=%q res=%+v", bk.platforms, res)
	}

	// Every BuildKit call carried the bearer token and the client headers.
	bk.mu.Lock()
	if len(bk.md) < 3 {
		t.Errorf("BuildKit calls = %v", bk.methods)
	}
	for i, md := range bk.md {
		if got := md.Get(bkCloudHeaderAuth); len(got) != 1 || got[0] != "Bearer "+p6cCloudToken {
			t.Errorf("%s: authorization = %v", bk.methods[i], got)
		}
		if len(md.Get(bkCloudHeaderPlatform)) != 1 || len(md.Get(bkCloudHeaderContext)) != 1 || len(md.Get(bkCloudHeaderVersion)) != 1 {
			t.Errorf("%s: headers = %v", bk.methods[i], md)
		}
	}
	bk.mu.Unlock()

	var dresp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: cresp.State}, &dresp)
	if dresp.Diagnostics.HasError() {
		t.Errorf("delete: %v", dresp.Diagnostics)
	}

	// Nothing secret in state, diagnostics, build result or log.
	p6cNoSecrets(t, "state", cresp.State.Raw.String())
	p6cNoSecrets(t, "refreshed state", rresp.State.Raw.String())
	p6cNoSecrets(t, "diagnostics", fmt.Sprint(cresp.Diagnostics, rresp.Diagnostics, vresp.Diagnostics))
	p6cNoSecrets(t, "build result", fmt.Sprintf("%+v", *res))
	if logBuf.Len() == 0 {
		t.Error("no log entries captured")
	}
	p6cNoSecrets(t, "log", logBuf.String())
}

// TestBuildxBuilderCloudDenied: a refused login fails Create on endpoint
// without leaking the secret.
func TestBuildxBuilderCloudDenied(t *testing.T) {
	ctx := context.Background()
	newP6cCloudAuth(t)
	newP6cCloudBK(t)
	pd := &providerData{RegistryAuth: []registryCredential{{Address: "docker.io", Username: p6cCloudUser, Password: "bad-" + p6cCloudPassword}}}
	r := &buildxBuilderResource{pd: pd}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	m := bxbTestModel()
	m.Driver, m.Endpoint = types.StringValue(bxbDriverCloud), types.StringValue("myorg/default")
	m.DockerContainer, m.TCPPort, m.BuildkitdConfigContent, m.Node = nil, types.Int64Null(), types.StringNull(), types.StringNull()
	m.Platform, m.Adopt = types.ListNull(types.StringType), types.BoolNull()
	cresp := resource.CreateResponse{State: tfState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan(t, sr, m)}, &cresp)
	if !cresp.Diagnostics.HasError() || !strings.Contains(fmt.Sprint(cresp.Diagnostics), "Docker Build Cloud") {
		t.Fatalf("denied create: %v", cresp.Diagnostics)
	}
	p6cNoSecrets(t, "diagnostics", fmt.Sprint(cresp.Diagnostics))
}
