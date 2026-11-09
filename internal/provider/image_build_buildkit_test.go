package provider

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/moby/api/types/build"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

// imgBkTestTraceAux returns the aux JSON of a moby.buildkit.trace message.
func imgBkTestTraceAux(t *testing.T, sr *controlapi.StatusResponse) json.RawMessage {
	t.Helper()
	dt, err := proto.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(dt) // []byte -> base64 JSON string, like the daemon's AuxFormatter
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func imgBkTestMsg(t *testing.T, w io.Writer, m jsonstream.Message) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(m); err != nil {
		t.Fatal(err)
	}
}

func imgBkRaw(s string) *json.RawMessage { r := json.RawMessage(s); return &r }

// imgBkFakeDaemon serves /session (h2c upgrade, then acts as the gRPC client
// of the provider's session, like dockerd) and /build.
type imgBkFakeDaemon struct {
	t           *testing.T
	mu          sync.Mutex
	sessHeaders http.Header
	sessMethod  string
	sessPath    string
	buildQuery  map[string][]string
	tarFiles    []string
	secret      string
	secretErr   error
	cred        *auth.CredentialsResponse
	sessConn    chan net.Conn
}

func (f *imgBkFakeDaemon) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/session":
		f.mu.Lock()
		f.sessHeaders, f.sessMethod, f.sessPath = r.Header.Clone(), r.Method, r.URL.Path
		f.mu.Unlock()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			f.t.Error(err)
			return
		}
		fmt.Fprint(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n")
		f.sessConn <- conn
	case strings.HasSuffix(r.URL.Path, "/build"):
		var files []string
		tr := tar.NewReader(r.Body)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			files = append(files, h.Name)
		}
		var conn net.Conn
		select {
		case conn = <-f.sessConn:
		case <-time.After(10 * time.Second):
			http.Error(w, "session never connected", http.StatusInternalServerError)
			return
		}
		defer conn.Close()
		cc, err := grpc.NewClient("passthrough:///session",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return conn, nil }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			f.t.Error(err)
			return
		}
		defer cc.Close()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		sec, secErr := secrets.NewSecretsClient(cc).GetSecret(ctx, &secrets.GetSecretRequest{ID: "token"})
		cred, credErr := auth.NewAuthClient(cc).Credentials(ctx, &auth.CredentialsRequest{Host: "registry-1.docker.io"})
		if credErr != nil {
			f.t.Errorf("credentials: %v", credErr)
		}
		f.mu.Lock()
		f.buildQuery, f.tarFiles, f.cred, f.secretErr = r.URL.Query(), files, cred, secErr
		if sec != nil {
			f.secret = string(sec.Data)
		}
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		imgBkTestMsg(f.t, w, jsonstream.Message{ID: imgAuxBuildKitTrace, Aux: imgBkPtr(imgBkTestTraceAux(f.t, &controlapi.StatusResponse{
			Vertexes: []*controlapi.Vertex{{Digest: "sha256:v1", Name: "[1/2] FROM scratch"}},
			Logs:     []*controlapi.VertexLog{{Vertex: "sha256:v1", Msg: []byte("step output\n")}},
		}))})
		imgBkTestMsg(f.t, w, jsonstream.Message{ID: imgAuxImageID, Aux: imgBkRaw(`{"ID":"sha256:bk1"}`)})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
	}
}

func imgBkPtr(r json.RawMessage) *json.RawMessage { return &r }

func TestImageBuildKitBuildFakeDaemon(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"Dockerfile": "# syntax=docker/dockerfile:1\nFROM scratch\n", "a.txt": "a"})
	secFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(secFile, []byte("s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := &imgBkFakeDaemon{t: t, sessConn: make(chan net.Conn, 1)}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}

	plan := &imageResourceModel{
		Name: types.StringValue("app:bk"),
		Build: &imageBuildModel{
			Context:   types.StringValue(dir),
			Args:      stringMapValue(map[string]string{"A": "1"}, true),
			Builder:   types.StringValue(imgBuilderBuildKit),
			CacheFrom: stringListValue([]string{"app:cache", "ghcr.io/x/app:cache"}, true),
			Platforms: stringListValue([]string{"linux/arm64"}, true),
			Secrets:   []imageBuildSecretModel{{ID: types.StringValue("token"), Src: types.StringValue(secFile), Env: types.StringNull()}},
		},
	}
	bc, err := imgCollectContext(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var diags diag.Diagnostics
	opts := imgBuildOptions(ctx, plan, bc.Dockerfile, &diags)
	creds := []registryCredential{{Address: "docker.io", Username: "hub", Password: "pw"}}
	id, bkDiags := imgBuildKitBuild(ctx, c, creds, plan.Build, bc, opts)
	diags.Append(bkDiags...)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if id != "sha256:bk1" {
		t.Errorf("image id: %q", id)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// (2) session handshake.
	if f.sessMethod != http.MethodPost || f.sessPath != "/session" {
		t.Errorf("session request: %s %s", f.sessMethod, f.sessPath)
	}
	h := f.sessHeaders
	if h.Get("Upgrade") != "h2c" || h.Get("Connection") != "Upgrade" {
		t.Errorf("upgrade headers: %v", h)
	}
	sessID := h.Get("X-Docker-Expose-Session-Uuid")
	if sessID == "" || h.Get("X-Docker-Expose-Session-Sharedkey") != imgBuildKitSharedKey(dir) {
		t.Errorf("session id/key headers: %v", h)
	}
	methods := strings.Join(h.Values("X-Docker-Expose-Session-Grpc-Method"), " ")
	for _, m := range []string{"/moby.buildkit.secrets.v1.Secrets/GetSecret", "/moby.filesync.v1.Auth/Credentials", "/grpc.health.v1.Health/Check"} {
		if !strings.Contains(methods, m) {
			t.Errorf("session methods missing %s: %s", m, methods)
		}
	}
	// (1) build options as seen by /build.
	q := f.buildQuery
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if get("version") != "2" || get("session") != sessID {
		t.Errorf("version/session: %q %q (want session %q)", get("version"), get("session"), sessID)
	}
	if get("cachefrom") != `["app:cache","ghcr.io/x/app:cache"]` {
		t.Errorf("cachefrom: %q", get("cachefrom"))
	}
	if get("platform") != "linux/arm64" || get("t") != "app:bk" || get("dockerfile") != "Dockerfile" {
		t.Errorf("platform/t/dockerfile: %v", q)
	}
	if get("buildargs") != `{"A":"1"}` {
		t.Errorf("buildargs: %q", get("buildargs"))
	}
	if !strings.Contains(strings.Join(f.tarFiles, ","), "Dockerfile") || !strings.Contains(strings.Join(f.tarFiles, ","), "a.txt") {
		t.Errorf("tar context: %v", f.tarFiles)
	}
	// Session services reached over the hijacked connection.
	if f.secretErr != nil || f.secret != "s3cr3t" {
		t.Errorf("secret over session: %q %v", f.secret, f.secretErr)
	}
	if f.cred == nil || f.cred.Username != "hub" || f.cred.Secret != "pw" {
		t.Errorf("credentials over session: %+v", f.cred)
	}
}

func TestImageBuildKitOptions(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	b := &imageBuildModel{
		Builder:   types.StringValue(imgBuilderBuildKit),
		CacheFrom: stringListValue([]string{"a:1"}, true),
		Platforms: stringListValue([]string{"linux/amd64"}, true),
	}
	opts := client.ImageBuildOptions{Version: build.BuilderV1, Platforms: platformsFromString("linux/arm64")}
	imgApplyBuildKitOptions(ctx, b, &opts, &diags)
	if diags.HasError() || opts.Version != build.BuilderBuildKit || len(opts.CacheFrom) != 1 || platformsString(opts.Platforms) != "linux/amd64" {
		t.Errorf("opts: %+v %v", opts, diags)
	}
	// No platforms: keep the classic platform resolution.
	opts = client.ImageBuildOptions{Platforms: platformsFromString("linux/arm64")}
	imgApplyBuildKitOptions(ctx, &imageBuildModel{Builder: types.StringValue(imgBuilderBuildKit)}, &opts, &diags)
	if platformsString(opts.Platforms) != "linux/arm64" || opts.CacheFrom != nil {
		t.Errorf("defaults: %+v", opts)
	}
}

func TestImageBuildKitSecretSources(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "s.txt")
	if err := os.WriteFile(file, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMG_BK_TEST_SECRET", "from-env")
	in := []imageBuildSecretModel{
		{ID: types.StringValue("f"), Src: types.StringValue(file), Env: types.StringNull()},
		{ID: types.StringValue("e"), Src: types.StringNull(), Env: types.StringValue("IMG_BK_TEST_SECRET")},
	}
	srcs, err := imgBuildKitSecretSources(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 2 || srcs[0].FilePath != file || srcs[0].Env != "" || srcs[1].Env != "IMG_BK_TEST_SECRET" || srcs[1].FilePath != "" {
		t.Errorf("sources: %+v", srcs)
	}
	store, err := secretsprovider.NewStore(srcs)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"f": "from-file", "e": "from-env"} {
		if got, err := store.GetSecret(ctx, id); err != nil || string(got) != want {
			t.Errorf("secret %s: %q %v", id, got, err)
		}
	}
	if _, err := store.GetSecret(ctx, "missing"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("missing secret: %v", err)
	}
	atts, err := imgBuildKitAttachables(in, nil)
	if err != nil || len(atts) != 2 {
		t.Errorf("attachables: %d %v", len(atts), err)
	}
	if atts, err := imgBuildKitAttachables(nil, nil); err != nil || len(atts) != 1 {
		t.Errorf("auth-only attachables: %d %v", len(atts), err)
	}

	for name, bad := range map[string]imageBuildSecretModel{
		"both":       {ID: types.StringValue("x"), Src: types.StringValue(file), Env: types.StringValue("IMG_BK_TEST_SECRET")},
		"neither":    {ID: types.StringValue("x"), Src: types.StringNull(), Env: types.StringNull()},
		"unset env":  {ID: types.StringValue("x"), Src: types.StringNull(), Env: types.StringValue("IMG_BK_TEST_DEFINITELY_UNSET")},
		"missing id": {ID: types.StringValue(""), Src: types.StringValue(file), Env: types.StringNull()},
	} {
		if _, err := imgBuildKitSecretSources([]imageBuildSecretModel{bad}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	missing := []imageBuildSecretModel{{ID: types.StringValue("x"), Src: types.StringValue(filepath.Join(t.TempDir(), "nope")), Env: types.StringNull()}}
	if _, err := imgBuildKitAttachables(missing, nil); err == nil {
		t.Error("missing secret file: expected error")
	}
}

func TestImageBuildKitAuthCredentials(t *testing.T) {
	ctx := context.Background()
	a := &imgBuildKitAuth{creds: []registryCredential{
		{Address: "https://index.docker.io/v1/", Username: "hub", Password: "p"},
		{Address: "ghcr.io", Auth: base64.StdEncoding.EncodeToString([]byte("gh:tok"))},
		{Address: "myacr.azurecr.io", Username: "00000000-0000-0000-0000-000000000000", IdentityToken: "refresh-tok"},
	}}
	for host, want := range map[string][2]string{
		"registry-1.docker.io": {"hub", "p"},
		"docker.io":            {"hub", "p"},
		"ghcr.io":              {"gh", "tok"},
		"myacr.azurecr.io":     {"", "refresh-tok"}, // identity token wins, no username
		"quay.io":              {"", ""},
	} {
		r, err := a.Credentials(ctx, &auth.CredentialsRequest{Host: host})
		if err != nil || r.Username != want[0] || r.Secret != want[1] {
			t.Errorf("%s: %+v %v", host, r, err)
		}
	}
}

func TestImageBuildKitValidate(t *testing.T) {
	ctx := context.Background()
	sec := []imageBuildSecretModel{{ID: types.StringValue("s"), Src: types.StringValue("f"), Env: types.StringNull()}}
	list := func(v ...string) types.List { return stringListValue(v, true) }
	nullList := types.ListNull(types.StringType)
	cases := map[string]struct {
		b    imageBuildModel
		errs []string // expected attribute path substrings
	}{
		"classic plain": {b: imageBuildModel{Builder: types.StringValue(imgBuilderClassic), CacheFrom: nullList, Platforms: nullList}},
		"classic with buildkit fields": {
			b:    imageBuildModel{Builder: types.StringValue(imgBuilderClassic), Secrets: sec, CacheFrom: list("a"), Platforms: list("linux/amd64")},
			errs: []string{"build.secrets", "build.cache_from", "build.platforms"},
		},
		"default builder is classic": {
			b:    imageBuildModel{Builder: types.StringNull(), CacheFrom: list("a"), Platforms: nullList},
			errs: []string{"build.cache_from"},
		},
		"unknown builder skipped": {b: imageBuildModel{Builder: types.StringUnknown(), Secrets: sec, CacheFrom: list("a"), Platforms: nullList}},
		"buildkit ok":             {b: imageBuildModel{Builder: types.StringValue(imgBuilderBuildKit), Secrets: sec, CacheFrom: list("a"), Platforms: list("linux/amd64")}},
		"secret both and neither": {
			b: imageBuildModel{Builder: types.StringValue(imgBuilderBuildKit), CacheFrom: nullList, Platforms: nullList, Secrets: []imageBuildSecretModel{
				{ID: types.StringValue("a"), Src: types.StringValue("f"), Env: types.StringValue("E")},
				{ID: types.StringValue("b"), Src: types.StringNull(), Env: types.StringNull()},
			}},
			errs: []string{"build.secrets[0]", "build.secrets[1]"},
		},
		"duplicate secret id": {
			b: imageBuildModel{Builder: types.StringValue(imgBuilderBuildKit), CacheFrom: nullList, Platforms: nullList, Secrets: []imageBuildSecretModel{
				{ID: types.StringValue("a"), Src: types.StringValue("f"), Env: types.StringNull()},
				{ID: types.StringValue("a"), Src: types.StringNull(), Env: types.StringValue("E")},
			}},
			errs: []string{"build.secrets[1].id"},
		},
		"multi platform": {
			b:    imageBuildModel{Builder: types.StringValue(imgBuilderBuildKit), CacheFrom: nullList, Platforms: list("linux/amd64", "linux/arm64")},
			errs: []string{"build.platforms"},
		},
		"platform and platforms": {
			b:    imageBuildModel{Builder: types.StringValue(imgBuilderBuildKit), Platform: types.StringValue("linux/amd64"), CacheFrom: nullList, Platforms: list("linux/amd64")},
			errs: []string{"build.platforms"},
		},
		"unknown platforms skipped": {
			b: imageBuildModel{Builder: types.StringValue(imgBuilderBuildKit), CacheFrom: nullList, Platforms: types.ListUnknown(types.StringType)},
		},
	}
	for name, tc := range cases {
		d := imgValidateBuildKit(ctx, &tc.b)
		var got []string
		for _, e := range d.Errors() {
			if pe, ok := e.(diag.DiagnosticWithPath); ok {
				got = append(got, pe.Path().String())
			}
		}
		if len(got) != len(tc.errs) {
			t.Errorf("%s: got errors %v, want %v (%v)", name, got, tc.errs, d)
			continue
		}
		for i, want := range tc.errs {
			if !strings.HasPrefix(got[i], want) {
				t.Errorf("%s: error %d path %q, want prefix %q", name, i, got[i], want)
			}
		}
	}
	if d := imgValidateBuildKit(ctx, nil); d.HasError() {
		t.Error("nil build should validate")
	}
}

func TestImageBuildKitDecodeStream(t *testing.T) {
	// moby.image.id aux parsing.
	if id := imgParseImageIDAux(json.RawMessage(`{"ID":"sha256:abc"}`)); id != "sha256:abc" {
		t.Errorf("image id aux: %q", id)
	}
	if id := imgParseImageIDAux(json.RawMessage(`"not an object"`)); id != "" {
		t.Errorf("bad aux: %q", id)
	}

	var ok strings.Builder
	trace := imgBkTestTraceAux(t, &controlapi.StatusResponse{
		Vertexes: []*controlapi.Vertex{{Digest: "d1", Name: "[2/2] RUN make"}},
		Logs: []*controlapi.VertexLog{
			{Vertex: "d1", Msg: []byte("compiling ")},
			{Vertex: "d1", Msg: []byte("main.go\nlinking\n")},
		},
	})
	imgBkTestMsg(t, &ok, jsonstream.Message{ID: imgAuxBuildKitTrace, Aux: &trace})
	imgBkTestMsg(t, &ok, jsonstream.Message{ID: imgAuxImageID, Aux: imgBkRaw(`{"ID":"sha256:built"}`)})
	var lines []string
	id, err := imgDecodeBuildKitStream(strings.NewReader(ok.String()), nil, func(s string) { lines = append(lines, s) })
	if err != nil || id != "sha256:built" {
		t.Fatalf("decode: %q %v", id, err)
	}
	if strings.Join(lines, "|") != "compiling main.go|linking" {
		t.Errorf("log lines: %q", lines)
	}

	// Fallback to a classic {"ID"} aux without an ID field on the message.
	id, err = imgDecodeBuildKitStream(strings.NewReader(`{"aux":{"ID":"sha256:classic"}}`+"\n"), nil, nil)
	if err != nil || id != "sha256:classic" {
		t.Errorf("fallback id: %q %v", id, err)
	}

	// Failure: vertex error and step output are attached to errorDetail.
	var bad strings.Builder
	failTrace := imgBkTestTraceAux(t, &controlapi.StatusResponse{
		Vertexes: []*controlapi.Vertex{{Digest: "d2", Name: "[2/2] RUN false", Error: "process \"/bin/sh -c false\" did not complete successfully: exit code: 1"}},
		Logs:     []*controlapi.VertexLog{{Vertex: "d2", Msg: []byte("about to fail\n")}},
	})
	imgBkTestMsg(t, &bad, jsonstream.Message{ID: imgAuxBuildKitTrace, Aux: &failTrace})
	imgBkTestMsg(t, &bad, jsonstream.Message{Error: &jsonstream.Error{Message: "failed to solve: exit code: 1"}})
	_, err = imgDecodeBuildKitStream(strings.NewReader(bad.String()), nil, nil)
	var se *imgStreamError
	if !errors.As(err, &se) {
		t.Fatalf("want imgStreamError, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"failed to solve", "[2/2] RUN false", "exit code: 1", "about to fail"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}

	// Garbage trace payloads are ignored.
	if _, err := imgParseBuildKitTrace(json.RawMessage(`"!!notbase64"`)); err == nil {
		t.Error("bad trace: expected error")
	}
}

// TestAccImageBuildKit builds with a secret mount (BuildKit-only syntax) on
// a live daemon. The secret value must not appear in state.
func TestAccImageBuildKit(t *testing.T) {
	testAccPreCheck(t)
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{
		"Dockerfile": hAccConfig("# syntax=docker/dockerfile:1\nFROM alpine:3.20\n" +
			"RUN --mount=type=secret,id=token test \"$(cat /run/secrets/token)\" = tfacc-secret && echo ok > /ok\n"),
	})
	t.Setenv("TFACC_IMG_BK_TOKEN", "tfacc-secret")
	cfg := fmt.Sprintf(`
resource "docker_image" "bk" {
  name = "tfacc-image-buildkit:latest"
  build {
    context = %q
    builder = "buildkit"
    secrets {
      id  = "token"
      env = "TFACC_IMG_BK_TOKEN"
    }
    cache_from = ["tfacc-image-buildkit:latest"]
  }
}
`, filepath.ToSlash(dir))
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccImageCheckDestroy(t),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestMatchResourceAttr("docker_image.bk", "id", imgTestSHA),
				resource.TestCheckResourceAttr("docker_image.bk", "build.secrets.0.env", "TFACC_IMG_BK_TOKEN"),
			),
		}},
	})
}
