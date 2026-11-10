package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

func ctrUpload(file string) containerUploadModel {
	return containerUploadModel{
		Path: types.StringValue(file), Content: types.StringNull(), ContentBase64: types.StringNull(), Source: types.StringNull(),
		SourceHash: types.StringNull(), Mode: types.StringNull(), UID: types.Int64Null(), GID: types.Int64Null(),
	}
}

// ctrReadTar returns the single entry of an upload tar.
func ctrReadTar(t *testing.T, b []byte) (*tar.Header, string) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(b))
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("want exactly one tar entry, next err=%v", err)
	}
	return h, string(data)
}

func TestContainerUploadTar(t *testing.T) {
	now := time.Unix(1700000000, 0)
	src := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(src, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	u := ctrUpload("/etc/app/app.conf")
	u.Content = types.StringValue("hello")
	u.UID, u.GID = types.Int64Value(1000), types.Int64Value(2000)
	it, err := containerUploadTar(u, now)
	if err != nil {
		t.Fatal(err)
	}
	h, data := ctrReadTar(t, it.Tar)
	if it.Dir != "/etc/app" || h.Name != "app.conf" || data != "hello" || h.Mode != 0o644 || h.Uid != 1000 || h.Gid != 2000 || h.Typeflag != tar.TypeReg {
		t.Errorf("content upload: dir=%s hdr=%+v data=%q", it.Dir, h, data)
	}

	u = ctrUpload("/bin/run.sh")
	u.ContentBase64 = types.StringValue(base64.StdEncoding.EncodeToString([]byte{0, 1, 2}))
	u.Mode = types.StringValue("0744")
	it, err = containerUploadTar(u, now)
	if err != nil {
		t.Fatal(err)
	}
	if h, data = ctrReadTar(t, it.Tar); h.Mode != 0o744 || data != "\x00\x01\x02" || h.Uid != 0 {
		t.Errorf("base64 upload: hdr=%+v data=%q", h, data)
	}

	u = ctrUpload("/opt/x")
	u.Source = types.StringValue(src)
	u.Mode = types.StringValue("0600")
	it, err = containerUploadTar(u, now)
	if err != nil {
		t.Fatal(err)
	}
	if h, data = ctrReadTar(t, it.Tar); h.Mode != 0o600 || data != "from-file" || it.Dir != "/opt" {
		t.Errorf("source upload: hdr=%+v data=%q", h, data)
	}

	u.Mode = types.StringValue("755")
	if m, err := containerUploadMode(u); err != nil || m != 0o755 {
		t.Errorf("mode 755: %o %v", m, err)
	}
}

func TestContainerUploadErrors(t *testing.T) {
	cases := map[string]func(u *containerUploadModel){
		"none": func(u *containerUploadModel) {},
		"two": func(u *containerUploadModel) {
			u.Content = types.StringValue("a")
			u.ContentBase64 = types.StringValue("YQ==")
		},
		"bad base64":  func(u *containerUploadModel) { u.ContentBase64 = types.StringValue("!!") },
		"missing src": func(u *containerUploadModel) { u.Source = types.StringValue(filepath.Join(t.TempDir(), "nope")) },
		"relative":    func(u *containerUploadModel) { u.Content = types.StringValue("a"); u.Path = types.StringValue("etc/x") },
		"dir":         func(u *containerUploadModel) { u.Content = types.StringValue("a"); u.Path = types.StringValue("/etc/") },
		"bad mode": func(u *containerUploadModel) {
			u.Content = types.StringValue("a")
			u.Mode = types.StringValue("0999")
		},
	}
	for name, mod := range cases {
		u := ctrUpload("/etc/x")
		mod(&u)
		if _, err := containerPrepareUploads([]containerUploadModel{u}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestContainerUploadNeedsReplace(t *testing.T) {
	tr, fa := types.BoolValue(true), types.BoolValue(false)
	run, cre, exi := types.StringValue(ctrStateRunning), types.StringValue(ctrStateCreated), types.StringValue(ctrStateExited)
	for _, c := range []struct {
		state   types.String
		running types.Bool
		want    bool
	}{
		{run, fa, true}, {run, tr, true}, {cre, tr, true}, {cre, fa, false}, {exi, fa, true}, {types.StringNull(), fa, true},
		{cre, types.BoolUnknown(), true},
	} {
		if got := containerUploadNeedsReplace(c.state, c.running); got != c.want {
			t.Errorf("state=%v running=%v: got %v", c.state, c.running, got)
		}
	}
}

func TestContainerSELinuxRelabel(t *testing.T) {
	str := types.StringValue
	in := []containerMountModel{
		{Type: str("bind"), Source: str("/srv"), Target: str("/data"), ReadOnly: types.BoolValue(true), SelinuxRelabel: str("z")},
		{Type: str("volume"), Source: str("v1"), Target: str("/v"), ReadOnly: types.BoolValue(false), SelinuxRelabel: str("Z")},
		{Type: str("tmpfs"), Target: str("/tmp"), ReadOnly: types.BoolValue(false)},
	}
	mounts, binds, err := containerExpandMountsBinds(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Target != "/tmp" {
		t.Errorf("mounts: %+v", mounts)
	}
	if strings.Join(binds, " ") != "/srv:/data:ro,z v1:/v:rw,Z" {
		t.Errorf("binds: %v", binds)
	}
	back := containerFlattenMounts(mounts, binds, in)
	for i := range in {
		if !back[i].Target.Equal(in[i].Target) || !back[i].Type.Equal(in[i].Type) || !back[i].ReadOnly.Equal(in[i].ReadOnly) ||
			back[i].SelinuxRelabel.ValueString() != in[i].SelinuxRelabel.ValueString() {
			t.Errorf("round trip %d: got %+v want %+v", i, back[i], in[i])
		}
	}
	// A -v style bind without relabel (import) is reported as a plain mount.
	if got := containerFlattenBinds([]string{"/h:/c"}); len(got) != 1 || !got[0].SelinuxRelabel.IsNull() || got[0].Type.ValueString() != "bind" {
		t.Errorf("plain bind: %+v", got)
	}

	for i, bad := range []containerMountModel{
		{Type: str("tmpfs"), Target: str("/t"), SelinuxRelabel: str("z")},
		{Type: str("volume"), Target: str("/t"), SelinuxRelabel: str("z")},
		{Type: str("bind"), Source: str("rel"), Target: str("/t"), SelinuxRelabel: str("z")},
		{Type: str("bind"), Source: str("/a"), Target: str("/t"), SelinuxRelabel: str("q")},
	} {
		if _, _, err := containerExpandMountsBinds([]containerMountModel{bad}); err == nil {
			t.Errorf("bad relabel %d should fail", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Fake Engine: create -> upload -> start ordering and in-place re-upload.

type ctrFakeEngine struct {
	mu      sync.Mutex
	calls   []string
	created container.CreateRequest
	running bool
	uploads map[string][]byte // archive path -> tar

	createQuery string
	networks    map[string]*network.EndpointSettings
	gone        bool

	// Pass 5.3: a one-shot container exits right after start with
	// exitCode and logs (multiplexed frames); startBodies records the body
	// length of every POST /start; mutate edits every inspect response
	// (daemon-filled defaults).
	oneShot     bool
	exited      bool
	exitCode    int
	logs        []byte
	startBodies []int
	mutate      func(*container.InspectResponse)
}

func (f *ctrFakeEngine) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	for _, pre := range []string{"/containers/", "/images/", "/networks/", "/version"} {
		if i := strings.Index(p, pre); i >= 0 {
			p = p[i:]
			break
		}
	}
	switch {
	case r.Method == http.MethodGet && p == "/version":
		_ = json.NewEncoder(w).Encode(map[string]any{"Version": "29.0.0", "ApiVersion": "1.52"})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/networks/") && (strings.HasSuffix(p, "/connect") || strings.HasSuffix(p, "/disconnect")):
		parts := strings.Split(p, "/")
		f.calls = append(f.calls, parts[3]+":"+parts[2])
		var body struct {
			Container      string
			EndpointConfig *network.EndpointSettings
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.networks == nil {
			f.networks = map[string]*network.EndpointSettings{}
		}
		if parts[3] == "connect" {
			ep := body.EndpointConfig
			if ep == nil {
				ep = &network.EndpointSettings{}
			}
			ep.NetworkID = "n-" + parts[2]
			f.networks[parts[2]] = ep
		} else {
			delete(f.networks, strings.TrimPrefix(parts[2], "n-"))
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/containers/"):
		f.calls = append(f.calls, "remove")
		if f.gone {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"No such container"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/update"):
		f.calls = append(f.calls, "update")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	case f.gone && strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/json"):
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"No such container"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/images/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:img", "Config": map[string]any{}})
	case r.Method == http.MethodPost && p == "/containers/create":
		f.calls = append(f.calls, "create")
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		f.createQuery = r.URL.RawQuery
		f.networks = map[string]*network.EndpointSettings{}
		if f.created.NetworkingConfig != nil {
			for k, v := range f.created.NetworkingConfig.EndpointsConfig {
				cp := *v
				cp.NetworkID = "n-" + k
				cp.IPAddress = mustAddr("10.9.0.2")
				f.networks[k] = &cp
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "c0ffee0123456789"})
	case r.Method == http.MethodPut && strings.HasSuffix(p, "/archive"):
		f.calls = append(f.calls, "upload:"+r.URL.Query().Get("path"))
		b, _ := io.ReadAll(r.Body)
		f.uploads[r.URL.Query().Get("path")] = b
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/start"):
		f.calls = append(f.calls, "start")
		b, _ := io.ReadAll(r.Body)
		f.startBodies = append(f.startBodies, len(b))
		f.running = !f.oneShot
		f.exited = f.oneShot
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/wait"):
		f.calls = append(f.calls, "wait")
		_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": f.exitCode})
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/logs"):
		f.calls = append(f.calls, "logs")
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = w.Write(f.logs)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/json"):
		hc := f.created.HostConfig
		if hc == nil {
			hc = &container.HostConfig{}
		}
		st := &container.State{Running: f.running, Status: map[bool]container.ContainerState{true: "running", false: "created"}[f.running]}
		if f.exited {
			st.Status, st.ExitCode, st.FinishedAt = "exited", f.exitCode, "2026-11-09T10:00:00Z"
		}
		resp := container.InspectResponse{
			ID: "c0ffee0123456789", Name: "/up", Image: "sha256:img",
			State: st, HostConfig: hc,
			Config:          f.created.Config,
			NetworkSettings: &container.NetworkSettings{Networks: f.networks},
		}
		if f.mutate != nil {
			resp.Config = ctrCloneConfig(resp.Config)
			f.mutate(&resp)
		}
		_ = json.NewEncoder(w).Encode(resp)
	default:
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(w, `{"message":"fake: %s %s"}`, r.Method, p)
	}
}

func ctrFake(t *testing.T) (*ctrFakeEngine, *providerData) {
	t.Helper()
	f := &ctrFakeEngine{uploads: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	return f, &providerData{Client: c}
}

func ctrUploadModel() containerResourceModel {
	m := containerP2Model()
	m.ID, m.ImageID, m.Running = types.StringUnknown(), types.StringUnknown(), types.BoolUnknown()
	m.Name = types.StringValue("up")
	m.Networks = types.MapNull(types.ObjectType{AttrTypes: ctrNetworkAttrTypes})
	m.NetworkMode = types.StringNull()
	m.GPUs = types.StringNull()
	m.DeviceRequests = []ctrDeviceRequestModel{}
	m.Logging = &ctrLoggingModel{Driver: types.StringUnknown(), Options: types.MapUnknown(types.StringType)}
	m.MemorySwap = types.Int64Unknown()
	m.ExitCode = types.Int64Unknown()
	u := ctrUpload("/etc/app.conf")
	u.Content = types.StringValue("v1")
	m.Files = []containerUploadModel{u}
	m.contractSetUnknown()
	return m
}

func ctrPlan(t *testing.T, s resource.SchemaResponse, m containerResourceModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(context.Background()), nil)}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return p
}

func TestContainerUploadFakeEngine(t *testing.T) {
	ctx := context.Background()
	f, pd := ctrFake(t)
	r := &containerResource{pd: pd}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)

	plan := ctrUploadModel()
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: ctrPlan(t, sr, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "create,upload:/etc,start" {
		t.Errorf("call order = %s", got)
	}
	if h, data := ctrReadTar(t, f.uploads["/etc"]); h.Name != "app.conf" || data != "v1" {
		t.Errorf("uploaded %+v %q", h, data)
	}
	if b := f.created.HostConfig.Binds; len(b) != 2 || b[0] != "/srv/b:/b:ro,Z" || b[1] != "vol:/v:rw,z" {
		t.Errorf("binds sent: %v", b)
	}
	var state containerResourceModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &state)...)
	if len(state.Files) != 1 || state.Files[0].Content.ValueString() != "v1" || len(state.Mounts) != 4 {
		t.Fatalf("state: files=%+v mounts=%+v", state.Files, state.Mounts)
	}
	if !strings.Contains(state.InspectJSON.ValueString(), "c0ffee0123456789") || len(state.ContentSHA256.ValueString()) != 64 ||
		state.DockerServerVersion.ValueString() != "29.0.0" || state.Inspect.IsNull() {
		t.Errorf("contract outputs: json=%d sha=%q version=%v", len(state.InspectJSON.ValueString()), state.ContentSHA256.ValueString(), state.DockerServerVersion)
	}
	if !strings.Contains(f.createQuery, "platform=linux") {
		t.Errorf("platform not sent: %q", f.createQuery)
	}

	// Stopped container with state = "created": Update re-uploads in place
	// and does not start the container.
	f.running = false
	f.calls = nil
	state.Running = types.BoolValue(false)
	state.State = types.StringValue(ctrStateCreated)
	next := state
	u := ctrUpload("/etc/app.conf")
	u.Content = types.StringValue("v2")
	next.Files = []containerUploadModel{u}
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	if d := st.Set(ctx, &state); d.HasError() {
		t.Fatal(d)
	}
	uresp := resource.UpdateResponse{State: st}
	r.Update(ctx, resource.UpdateRequest{Plan: ctrPlan(t, sr, next), State: st}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "upload:/etc" {
		t.Errorf("update calls = %s", got)
	}
	if _, data := ctrReadTar(t, f.uploads["/etc"]); data != "v2" {
		t.Errorf("re-uploaded %q", data)
	}
}

// TestContainerUploadPlan checks the plan-time validators and that a
// changed upload on a running container is planned as a replacement.
func TestContainerUploadPlan(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{
			{
				PlanOnly: true, ExpectNonEmptyPlan: true,
				Config: `
resource "docker_container" "u" {
  name  = "u"
  image = "busybox:1.36"
  files {
    path = "/etc/motd"
    content = "hi"
    mode = "0600"
    uid = 1000
  }
  mounts {
    type            = "bind"
    source          = "/srv"
    target          = "/srv"
    selinux_relabel = "z"
  }
}`,
			},
			{
				PlanOnly: true,
				Config: `
resource "docker_container" "u" {
  name  = "u"
  image = "busybox:1.36"
  files {
    path           = "/etc/motd"
    content        = "hi"
    content_base64 = "aGk="
  }
}`,
				ExpectError: mustRegexpT(t, `(?s)Invalid Attribute Combination`),
			},
			{
				PlanOnly: true,
				Config: `
resource "docker_container" "u" {
  name  = "u"
  image = "busybox:1.36"
  files {
    path    = "/etc/motd"
    content = "hi"
    mode    = "rwx"
  }
}`,
				ExpectError: mustRegexpT(t, `(?s)octal mode`),
			},
			{
				PlanOnly: true,
				Config: `
resource "docker_container" "u" {
  name  = "u"
  image = "busybox:1.36"
  mounts {
    type            = "bind"
    source          = "/srv"
    target          = "/srv"
    selinux_relabel = "x"
  }
}`,
				ExpectError: mustRegexpT(t, `(?s)selinux_relabel`),
			},
		},
	})
}

func TestContainerUploadDiagnosticsPath(t *testing.T) {
	// containerBuildCreate must accept relabel mounts and route them to Binds.
	m := containerP2Model()
	var diags diag.Diagnostics
	_, hc, _ := containerBuildCreate(context.Background(), m, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if len(hc.Mounts) != 2 || len(hc.Binds) != 2 {
		t.Errorf("mounts=%+v binds=%v", hc.Mounts, hc.Binds)
	}
}

func mustRegexpT(t *testing.T, s string) *regexp.Regexp {
	t.Helper()
	return regexp.MustCompile(s)
}

// ctrCloneConfig copies a config so a mutate hook does not change the
// recorded create request.
func ctrCloneConfig(c *container.Config) *container.Config {
	if c == nil {
		return &container.Config{}
	}
	cp := *c
	return &cp
}
