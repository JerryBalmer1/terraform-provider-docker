package provider

// Pass 5.3 Lane B unit tests: multi-platform exports, resources, bake
// cross-target references and target: contexts, owner labels on
// docker_image / docker_buildx_builder / docker_buildx_bake, the remote
// builder driver and build-record materials and attachments.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	bkclient "github.com/moby/buildkit/client"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestBKIsContainerdStore(t *testing.T) {
	if !bkIsContainerdStore([][2]string{{"Backing Filesystem", "extfs"}, {"driver-type", bkContainerdSnapshotter}}) {
		t.Error("containerd store not detected")
	}
	if bkIsContainerdStore([][2]string{{"Backing Filesystem", "extfs"}, {"Supports d_type", "true"}}) {
		t.Error("overlay2 store detected as containerd")
	}
}

func TestBKMultiPlatformExports(t *testing.T) {
	ctx := context.Background()
	old := bkImageStore
	t.Cleanup(func() { bkImageStore = old })
	store := false
	calls := 0
	bkImageStore = func(context.Context, *providerData) (bool, string, error) {
		calls++
		return store, "overlay2", nil
	}
	container := &bkConn{Node: bkNode{Builder: "ci", Name: "ci0"}}
	docker := &bkConn{Node: bkDefaultNode(), Docker: true}
	two := []string{"linux/amd64", "linux/arm64"}

	// One platform: nothing checked.
	exps, loads, _ := bkExports(ctx, nil, container, seamBuildOutputs{Names: []string{"a"}, Load: true})
	p := &bkPrepared{opt: bkclient.SolveOpt{Exports: exps}, loads: loads}
	if err := bkMultiPlatformExports(ctx, nil, container, []string{"linux/amd64"}, p); err != nil || calls != 0 {
		t.Fatalf("single platform: %v %d", err, calls)
	}
	// Two platforms loaded into a graph-driver daemon: clear error.
	err := bkMultiPlatformExports(ctx, nil, container, two, p)
	if err == nil || !strings.Contains(err.Error(), "containerd image store") || !strings.Contains(err.Error(), "overlay2") {
		t.Fatalf("graph driver: %v", err)
	}
	exps, _, _ = bkExports(ctx, nil, docker, seamBuildOutputs{Names: []string{"a"}})
	if err := bkMultiPlatformExports(ctx, nil, docker, two, &bkPrepared{opt: bkclient.SolveOpt{Exports: exps}}); err == nil ||
		!strings.Contains(err.Error(), "daemon's own BuildKit") {
		t.Fatalf("docker driver: %v", err)
	}
	// Push only from a container builder: no store needed.
	exps, loads, _ = bkExports(ctx, nil, container, seamBuildOutputs{Names: []string{"a"}, Push: true})
	n := calls
	if err := bkMultiPlatformExports(ctx, nil, container, two, &bkPrepared{opt: bkclient.SolveOpt{Exports: exps}, loads: loads}); err != nil || calls != n {
		t.Fatalf("push only: %v", err)
	}
	// Containerd store: the docker exporter feeding the load becomes oci.
	store = true
	exps, loads, _ = bkExports(ctx, nil, container, seamBuildOutputs{Names: []string{"a"}, Load: true, Raw: []string{"type=local,dest=" + t.TempDir()}})
	p = &bkPrepared{opt: bkclient.SolveOpt{Exports: exps}, loads: loads}
	if err := bkMultiPlatformExports(ctx, nil, container, two, p); err != nil {
		t.Fatal(err)
	}
	if p.opt.Exports[0].Type != bkclient.ExporterOCI || p.opt.Exports[0].Attrs["name"] != "a" || p.opt.Exports[1].Type != bkclient.ExporterLocal {
		t.Errorf("exports = %+v", p.opt.Exports)
	}
	bkImageStore = func(context.Context, *providerData) (bool, string, error) { return false, "", errors.New("boom") }
	if err := bkMultiPlatformExports(ctx, nil, container, two, &bkPrepared{opt: bkclient.SolveOpt{Exports: exps}, loads: loads}); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Errorf("info error: %v", err)
	}
}

func TestBKImageStoreInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info") {
			fmt.Fprint(w, `{"Driver":"overlayfs","DriverStatus":[["driver-type","io.containerd.snapshotter.v1"]]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	ok, drv, err := bkImageStore(context.Background(), &providerData{Client: c})
	if err != nil || !ok || drv != "overlayfs" {
		t.Errorf("store = %v %q %v", ok, drv, err)
	}
	if _, _, err := bkImageStore(context.Background(), nil); err == nil {
		t.Error("nil provider accepted")
	}
}

func TestBKApplyResources(t *testing.T) {
	attrs := map[string]string{}
	err := bkApplyResources(attrs, map[string]string{"memory": "2g", "memory-swap": "-1", "cpu_quota": "50000", "cpu-period": "100000",
		"cpu-shares": "512", "cpuset-cpus": "0-1", "cpuset-mems": "0"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"memory": "2147483648", "memswap": "-1", "cpuquota": "50000", "cpuperiod": "100000", "cpushares": "512",
		"cpusetcpus": "0-1", "cpusetmems": "0"}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("%s = %q, want %q", k, attrs[k], v)
		}
	}
	for _, bad := range []map[string]string{{"gpus": "1"}, {"memory": "lots"}, {"cpu-quota": "-1"}, {"memory-swap": "0"}} {
		if err := bkApplyResources(map[string]string{}, bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

const bakeCrossHCL = `
target "app" {
  context = "app"
  tags    = ["reg.io/app:${target.base.args.VERSION}"]
  args    = target.base.args
  labels  = { from = target.base.name }
  contexts = {
    base = "target:base"
  }
  resources = {
    memory = "1g"
  }
}

target "base" {
  args = { VERSION = "1.2" }
  dockerfile = "base.Dockerfile"
}

target "m" {
  name   = "m-${v}"
  matrix = { v = ["a", "b"] }
  tags   = ["m:${v}"]
}

target "uses-matrix" {
  tags = target.m-b.tags
}
`

func TestBakeCrossTargetReferences(t *testing.T) {
	def, err := bakeParse([]bakeFile{{Name: "docker-bake.hcl", Data: []byte(bakeCrossHCL)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := def.resolve([]string{"app", "uses-matrix"}, map[string]string{"app.resource.cpu-quota": "1000"})
	if err != nil {
		t.Fatal(err)
	}
	app := res.Targets[0]
	if app.Tags[0] != "reg.io/app:1.2" || *app.Args["VERSION"] != "1.2" || *app.Labels["from"] != "base" || app.Contexts["base"] != "target:base" ||
		app.Resources["memory"] != "1g" || app.Resources["cpu-quota"] != "1000" {
		t.Errorf("app = %+v", app.printMap())
	}
	if um := res.Targets[1]; strings.Join(um.Tags, ",") != "m:b" {
		t.Errorf("uses-matrix = %+v", um.printMap())
	}
	for name, hcl := range map[string]string{
		"cycle":   `target "a" { tags = target.b.tags }` + "\n" + `target "b" { tags = target.a.tags }`,
		"self":    `target "a" { tags = ["x"]` + "\n" + `args = { T = target.a.tags[0] } }`,
		"missing": `target "a" { tags = target.zz.tags }`,
	} {
		if _, err := bakeParse([]bakeFile{{Name: "b.hcl", Data: []byte(hcl)}}, nil, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A user variable named target is left alone.
	def, err = bakeParse([]bakeFile{{Name: "b.hcl", Data: []byte(`variable "target" { default = "x" }` + "\n" + `target "a" { tags = [target] }`)}}, nil, nil)
	if err != nil || def.Targets["a"].Tags[0] != "x" {
		t.Errorf("user variable target: %v", err)
	}
	// matrix / name reach the parser's switch (field coverage) as no-ops.
	tg := &bakeTarget{}
	if tg.set("matrix", map[string]any{"v": []any{"1"}}) != nil || tg.set("name", "x") != nil {
		t.Error("matrix/name rejected by set")
	}
}

func TestBakeLinkSteps(t *testing.T) {
	hcl := `
target "base" {
  dockerfile = "b.Dockerfile"
  secret     = ["id=s,env=S"]
}
target "mid" {
  contexts = { base = "target:base" }
}
target "app" {
  contexts = { mid = "target:mid", base2 = "target:base", img = "docker-image://alpine" }
  secret = ["id=t,env=T"]
}
target "loop1" { contexts = { x = "target:loop2" } }
target "loop2" { contexts = { x = "target:loop1" } }
target "bad" { contexts = { x = "target:nope" } }
`
	def, err := bakeParse([]bakeFile{{Name: "b.hcl", Data: []byte(hcl)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app, _ := def.resolveTarget("app", nil)
	steps, err := bakeLinkSteps(def, nil, app, t.TempDir(), bakeGlobals{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "base,mid,app" {
		t.Errorf("steps = %v", ids)
	}
	merged, err := bkMergeSessionRequests(steps)
	if err != nil || len(merged.Secrets) != 2 {
		t.Errorf("merged secrets = %+v %v", merged.Secrets, err)
	}
	rest, refs := bkSplitTargetContexts(steps[2].Extra)
	if refs["mid"] != "mid" || refs["base2"] != "base" || rest.Contexts["img"] != "docker-image://alpine" || len(rest.Contexts) != 1 {
		t.Errorf("split = %+v %v", rest.Contexts, refs)
	}
	if !bkHasTargetContexts(steps[2].Extra) || bkHasTargetContexts(rest) {
		t.Error("bkHasTargetContexts")
	}
	for _, n := range []string{"loop1", "bad"} {
		tg, _ := def.resolveTarget(n, nil)
		if _, err := bakeLinkSteps(def, nil, tg, t.TempDir(), bakeGlobals{}); err == nil {
			t.Errorf("%s: no error", n)
		}
	}
	// The same secret id with two sources cannot share one session.
	steps[0].Req.Secrets = []seamBuildSecret{{ID: "t", Env: "OTHER"}}
	if _, err := bkMergeSessionRequests(steps); err == nil {
		t.Error("conflicting secret accepted")
	}
	// target: contexts outside bake are rejected with a pointer to bake.
	p := &bkPrepared{opt: bkclient.SolveOpt{FrontendAttrs: map[string]string{}}}
	if err := bkApplyExtra(p, &bkExtra{Contexts: map[string]string{"x": "target:y"}}); err == nil || !strings.Contains(err.Error(), "docker_buildx_bake") {
		t.Errorf("target: outside bake: %v", err)
	}
}

func TestBKLinkInputs(t *testing.T) {
	attrs := map[string]string{}
	fi := map[string]*pb.Definition{}
	lp := bkLinkedPrepared{id: "app", targets: map[string]string{"base": "base"}}
	results := map[string]*gateway.Result{"base": {Metadata: map[string][]byte{"containerimage.config": []byte(`{"config":{"Env":["A=1"]}}`)}}}
	if err := bkLinkInputs(lp, results, attrs, fi); err != nil {
		t.Fatal(err)
	}
	if attrs["context:base"] != "input:base" || fi["base"] == nil || !strings.Contains(attrs["input-metadata:base"], "containerimage.config") {
		t.Errorf("attrs = %v", attrs)
	}
	if err := bkLinkInputs(bkLinkedPrepared{id: "x", targets: map[string]string{"c": "missing"}}, results, attrs, fi); err == nil {
		t.Error("missing dependency accepted")
	}
	if got := bkLocalName("my app.v1", "context"); got != "tpd-my_app_v1-context" {
		t.Errorf("local name = %s", got)
	}
}

// TestBuildxBakeOwnerAndLinked checks the owner label reaches every
// target's frontend options (inject-only) and that a target with target:
// contexts goes through the linked build.
func TestBuildxBakeOwnerAndLinked(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker-bake.hcl"), []byte(`
target "base" {}
target "app" {
  contexts = { base = "target:base" }
  labels   = { a = "1" }
  resources = { memory = "1g" }
}
group "default" { targets = ["base", "app"] }
`), 0o600)
	oldB, oldC, oldL := bakeBuild, bakeConnect, bakeBuildLinked
	t.Cleanup(func() { bakeBuild, bakeConnect, bakeBuildLinked = oldB, oldC, oldL })
	bakeConnect = func(_ context.Context, _ *providerData, ref seamBuilderRef) (*bkConn, error) {
		return &bkConn{Node: bkNode{Builder: "default", Name: "default"}}, nil
	}
	labels := map[string]map[string]string{}
	var linked []string
	var extraRes map[string]string
	bakeBuild = func(_ context.Context, _ *providerData, _ *bkConn, req seamBuildRequest, _ *bkExtra) (*seamBuildResult, error) {
		attrs, err := bkFrontendAttrs(req)
		if err != nil {
			return nil, err
		}
		labels["base"] = map[string]string{"owner": attrs["label:"+ownerLabelKey]}
		return &seamBuildResult{Metadata: map[string]any{}}, nil
	}
	bakeBuildLinked = func(_ context.Context, _ *providerData, _ *bkConn, steps []bkLinkStep) (*seamBuildResult, error) {
		for _, s := range steps {
			linked = append(linked, s.ID)
		}
		last := steps[len(steps)-1]
		attrs, _ := bkFrontendAttrs(last.Req)
		labels["app"] = map[string]string{"owner": attrs["label:"+ownerLabelKey], "a": attrs["label:a"]}
		extraRes = last.Extra.Resources
		return &seamBuildResult{Metadata: map[string]any{}}, nil
	}
	a := NewBuildxBakeAction()
	pd.Owner = "team-a"
	if res := actInvokePD(t, pd, a, map[string]tftypes.Value{"working_dir": p3eStr(dir)}); res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if labels["base"]["owner"] != "team-a" || labels["app"]["owner"] != "team-a" || labels["app"]["a"] != "1" {
		t.Errorf("owner set: %v", labels)
	}
	if strings.Join(linked, ",") != "base,app" || extraRes["memory"] != "1g" {
		t.Errorf("linked = %v resources = %v", linked, extraRes)
	}
	pd.Owner = ""
	linked = nil
	if res := actInvokePD(t, pd, a, map[string]tftypes.Value{"working_dir": p3eStr(dir)}); res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if labels["base"]["owner"] != "" || labels["app"]["owner"] != "" {
		t.Errorf("owner unset: %v", labels)
	}
}

// --- docker_image ----------------------------------------------------------

type b53SeamCapture struct{ req seamBuildRequest }

func (c *b53SeamCapture) Build(_ context.Context, _ *providerData, req seamBuildRequest) (*seamBuildResult, error) {
	c.req = req
	return &seamBuildResult{ImageID: "sha256:idx", Digest: "sha256:idx", Platforms: req.Platforms}, nil
}

func TestImageBuildOwnerLabelAndPlatforms(t *testing.T) {
	ctx := context.Background()
	old := seamBuildKitImpl
	t.Cleanup(func() { seamBuildKitImpl = old })
	capt := &b53SeamCapture{}
	seamBuildKitImpl = capt
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o600)
	nullList := types.ListNull(types.StringType)
	plan := &imageResourceModel{Name: types.StringValue("app:1"), Platform: types.StringNull(), Build: &imageBuildModel{
		Context: types.StringValue(dir), Builder: types.StringValue(imgBuilderBuildKit), Platforms: stringListValue([]string{"linux/amd64", "linux/arm64"}, false),
		Args: types.MapNull(types.StringType), Labels: stringMapValue(map[string]string{"x": "y"}, false), CacheFrom: nullList, CacheTo: nullList,
		ExtraHosts: nullList, Tags: nullList, NoCacheFilter: nullList, Outputs: nullList, Attestations: nullList,
		AdditionalContexts: types.MapNull(types.StringType), Ulimits: types.MapNull(types.ObjectType{AttrTypes: imgUlimitAttrTypes}),
	}}
	for _, owner := range []string{"team-a", ""} {
		pd := &providerData{Owner: owner}
		var diags diag.Diagnostics
		opts := imgBuildOptions(ctx, plan, "Dockerfile", &diags)
		res, d := imgBuildViaSeam(ctx, pd, plan, opts)
		if d.HasError() || res == nil {
			t.Fatal(d)
		}
		if capt.req.Labels[ownerLabelKey] != owner || capt.req.Labels["x"] != "y" {
			t.Errorf("owner %q: labels = %v", owner, capt.req.Labels)
		}
		if _, set := capt.req.Labels[ownerLabelKey]; set != (owner != "") {
			t.Errorf("owner %q: label present = %v", owner, set)
		}
		if strings.Join(capt.req.Platforms, ",") != "linux/amd64,linux/arm64" {
			t.Errorf("platforms = %v", capt.req.Platforms)
		}
		// Classic path helper.
		imgOwnerBuildLabels(pd, &opts)
		if opts.Labels[ownerLabelKey] != owner {
			t.Errorf("classic labels = %v", opts.Labels)
		}
	}
	// build.builder takes a BuildKit address.
	if ref := imgSeamBuilder(&imageBuildModel{Builder: types.StringValue("tcp://bk:1234")}); ref.Endpoint != "tcp://bk:1234" || ref.Name != "" {
		t.Errorf("endpoint builder = %+v", ref)
	}
	if ref := imgSeamBuilder(&imageBuildModel{Builder: types.StringValue("ci")}); ref.Name != "ci" {
		t.Errorf("named builder = %+v", ref)
	}
}

func TestImageOwnerCreateCheck(t *testing.T) {
	owner := "other"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/images/app:1/json") {
			fmt.Fprintf(w, `{"Id":"sha256:x","Config":{"Labels":{%q:%q}}}`, ownerLabelKey, owner)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"No such image"}`)
	}))
	t.Cleanup(srv.Close)
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pd := &providerData{Client: c, Owner: "mine"}
	if err := imgOwnerCreateCheck(ctx, pd, "app:1"); err == nil || !strings.Contains(err.Error(), `owned by "other"`) {
		t.Errorf("conflict: %v", err)
	}
	if err := imgOwnerCreateCheck(ctx, pd, "new:1"); err != nil {
		t.Errorf("missing image: %v", err)
	}
	owner = "mine"
	if err := imgOwnerCreateCheck(ctx, pd, "app:1"); err != nil {
		t.Errorf("same owner: %v", err)
	}
	if err := imgOwnerCreateCheck(ctx, &providerData{Client: c}, "app:1"); err != nil {
		t.Errorf("owner unset: %v", err)
	}
}

// --- docker_buildx_builder -------------------------------------------------

func TestBuildxBuilderOwnerLabel(t *testing.T) {
	ctx := context.Background()
	oldR, oldW := bxbReady, bkWorkers
	t.Cleanup(func() { bxbReady, bkWorkers = oldR, oldW })
	bxbReady = func(context.Context, *providerData, string) (*bkclient.WorkerInfo, error) {
		return &bkclient.WorkerInfo{}, nil
	}
	bkWorkers = func(context.Context, *providerData, bkNode) ([]*bkclient.WorkerInfo, error) { return nil, nil }
	run := func(owner string, existing map[string]string) (*bxbFakeEngine, resource.CreateResponse) {
		f := &bxbFakeEngine{volumes: map[string]bool{}}
		if existing != nil {
			f.created = container.CreateRequest{Config: &container.Config{Labels: existing}}
		}
		srv := httptest.NewServer(http.HandlerFunc(f.handler))
		t.Cleanup(srv.Close)
		host := "tcp://" + srv.Listener.Addr().String()
		c, err := newDockerClient(endpointConfig{Host: host, APIVersion: "1.47"})
		if err != nil {
			t.Fatal(err)
		}
		r := &buildxBuilderResource{pd: &providerData{Client: c, Endpoint: endpointConfig{Host: host}, Owner: owner}}
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		m := bxbTestModel()
		m.Adopt = types.BoolNull()
		cresp := resource.CreateResponse{State: tfState(t, sr, nil)}
		r.Create(ctx, resource.CreateRequest{Plan: tfPlan(t, sr, m)}, &cresp)
		return f, cresp
	}
	f, resp := run("team-a", nil)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if f.created.Config.Labels[ownerLabelKey] != "team-a" || f.created.Config.Labels[bxbLabelBuilder] != "ci" {
		t.Errorf("labels with owner: %v", f.created.Config.Labels)
	}
	f, resp = run("", nil)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if _, ok := f.created.Config.Labels[ownerLabelKey]; ok {
		t.Errorf("labels without owner: %v", f.created.Config.Labels)
	}
	_, resp = run("team-a", map[string]string{ownerLabelKey: "team-b"})
	if !resp.Diagnostics.HasError() || !strings.Contains(fmt.Sprint(resp.Diagnostics), `owned by "team-b"`) {
		t.Errorf("conflict: %v", resp.Diagnostics)
	}
}

func TestBuildxBuilderRemote(t *testing.T) {
	ctx := context.Background()
	r := &buildxBuilderResource{pd: &providerData{}}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	m := bxbTestModel()
	m.Driver = types.StringValue(bxbDriverRemote)
	m.Endpoint = types.StringValue("tcp://bk.example:1234")
	m.DockerContainer, m.TCPPort, m.BuildkitdConfigContent, m.Node = nil, types.Int64Null(), types.StringNull(), types.StringNull()
	m.Platform = types.ListNull(types.StringType)
	m.Bootstrap = types.BoolValue(false)
	m.Adopt = types.BoolNull()
	m.Remote = &bxbRemoteModel{CACert: types.StringValue("/ca.pem"), Cert: types.StringValue("/c.pem"), Key: types.StringValue("/k.pem"), ServerName: types.StringNull()}

	var vresp resource.ValidateConfigResponse
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: b53Config(t, sr, m)}, &vresp)
	if vresp.Diagnostics.HasError() {
		t.Fatalf("valid remote rejected: %v", vresp.Diagnostics)
	}
	bad := m
	bad.Endpoint = types.StringValue("http://x")
	bad.TCPPort = types.Int64Value(1)
	bad.Remote = &bxbRemoteModel{CACert: types.StringNull(), Cert: types.StringValue("/c"), Key: types.StringNull(), ServerName: types.StringNull()}
	vresp = resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: b53Config(t, sr, bad)}, &vresp)
	if vresp.Diagnostics.ErrorsCount() != 3 {
		t.Errorf("invalid remote: %v", vresp.Diagnostics)
	}
	dc := bxbTestModel()
	dc.Endpoint = types.StringValue("tcp://x:1")
	dc.Adopt = types.BoolNull()
	vresp = resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: b53Config(t, sr, dc)}, &vresp)
	if !vresp.Diagnostics.HasError() {
		t.Error("endpoint accepted for docker-container")
	}

	cresp := resource.CreateResponse{State: tfState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan(t, sr, m)}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	var st bxbModel
	cresp.State.Get(ctx, &st)
	if st.ID.ValueString() != "tcp://bk.example:1234" || !st.ContainerName.IsNull() || st.Node.ValueString() != "ci0" {
		t.Errorf("state = %+v", st)
	}
	n := bxbRemoteNode(st)
	if n.Driver != bkDriverRemote || n.DriverOpts["cacert"] != "/ca.pem" || len(bkTLSOpts(n.DriverOpts)) != 2 {
		t.Errorf("node = %+v", n)
	}
	var dresp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: cresp.State}, &dresp)
	if dresp.Diagnostics.HasError() {
		t.Errorf("delete: %v", dresp.Diagnostics)
	}
}

// --- docker_buildx_history -------------------------------------------------

type b53FakeContent struct{ blobs map[string][]byte }
type b53ReadReq struct{ Digest string }
type b53ReadResp struct{ Data []byte }
type b53Stream struct{ parts [][]byte }

func (s *b53Stream) Recv() (*b53ReadResp, error) {
	if len(s.parts) == 0 {
		return nil, io.EOF
	}
	p := s.parts[0]
	s.parts = s.parts[1:]
	return &b53ReadResp{Data: p}, nil
}

func (f b53FakeContent) Read(_ context.Context, in *b53ReadReq, _ ...any) (*b53Stream, error) {
	b, ok := f.blobs[in.Digest]
	if !ok {
		return nil, errors.New("not found")
	}
	return &b53Stream{parts: [][]byte{b[:len(b)/2], b[len(b)/2:]}}, nil
}

func TestBxhReadBlobAndMaterials(t *testing.T) {
	v02 := `{"predicateType":"https://slsa.dev/provenance/v0.2","predicate":{"materials":[{"uri":"pkg:docker/alpine@3.20","digest":{"sha256":"aa"}}]}}`
	v1 := `{"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"resolvedDependencies":[{"uri":"git+https://x","digest":{"sha1":"bb"}}]}}}`
	cc := b53FakeContent{blobs: map[string][]byte{"sha256:p": []byte(v02)}}
	b, err := bxhReadBlob(context.Background(), cc, "sha256:p")
	if err != nil || string(b) != v02 {
		t.Fatalf("read = %q %v", b, err)
	}
	if _, err := bxhReadBlob(context.Background(), cc, "sha256:none"); err == nil {
		t.Error("missing blob read")
	}
	if _, err := bxhReadBlob(context.Background(), struct{}{}, "x"); err == nil {
		t.Error("no Read method accepted")
	}
	ms := bxhMaterials([]byte(v02))
	if len(ms) != 1 || ms[0]["URI"] != "pkg:docker/alpine@3.20" || fmt.Sprint(ms[0]["Digests"]) != "[sha256:aa]" {
		t.Errorf("v0.2 = %v", ms)
	}
	ms = bxhMaterials([]byte(v1))
	if len(ms) != 1 || fmt.Sprint(ms[0]["Digests"]) != "[sha1:bb]" || bxhMaterialsStructured(ms)[0].(map[string]any)["uri"] != "git+https://x" {
		t.Errorf("v1 = %v", ms)
	}
	if len(bxhMaterials([]byte("nope"))) != 0 {
		t.Error("garbage materials")
	}
	atts := []map[string]any{
		{"digest": "sha256:p1", "type": "https://slsa.dev/provenance/v0.2", "platform": "linux/amd64"},
		{"digest": "sha256:p2", "type": "https://slsa.dev/provenance/v0.2", "platform": "linux/arm64"},
		{"digest": "sha256:s1", "type": "https://spdx.dev/Document", "platform": "linux/amd64"},
	}
	if a, err := bxhPickAttachment(atts, "provenance", "linux/arm64"); err != nil || a["digest"] != "sha256:p2" {
		t.Errorf("provenance arm64 = %v %v", a, err)
	}
	if a, err := bxhPickAttachment(atts, "sbom", ""); err != nil || a["digest"] != "sha256:s1" {
		t.Errorf("sbom = %v %v", a, err)
	}
	if a, err := bxhPickAttachment(atts, "sha256:p1", ""); err != nil || a["digest"] != "sha256:p1" {
		t.Errorf("digest = %v %v", a, err)
	}
	if _, err := bxhPickAttachment(atts, "provenance", ""); err == nil {
		t.Error("ambiguous accepted")
	}
	if _, err := bxhPickAttachment(atts, "sha256:zz", ""); err == nil {
		t.Error("unknown accepted")
	}
}

func TestBuildxHistoryMaterialsAndAttachment(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	bk, _ := newBKFake(t)
	bk.records = bkTestRecords()
	old := bxhReadContent
	t.Cleanup(func() { bxhReadContent = old })
	prov := `{"predicateType":"https://slsa.dev/provenance/v0.2","predicate":{"materials":[{"uri":"pkg:docker/alpine@3.20","digest":{"sha256":"aa"}}]}}`
	bxhReadContent = func(_ context.Context, _ *providerData, _ bkNode, ds []string) (map[string][]byte, error) {
		out := map[string][]byte{}
		for _, d := range ds {
			out[d] = []byte(prov)
		}
		return out, nil
	}
	st, diags := p3eReadDS(t, NewBuildxHistoryDataSource(), pd, map[string]tftypes.Value{"builder": p3eStr("ci"), "ref": p3eStr("aaa111"),
		"attachment": p3eStr("provenance")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var im bxhInspectModel
	st.Get(context.Background(), &im)
	if im.AttachmentBody.ValueString() != prov || !strings.Contains(im.InspectJSON.ValueString(), `"URI":"pkg:docker/alpine@3.20"`) {
		t.Errorf("body=%s inspect=%s", im.AttachmentBody.ValueString(), im.InspectJSON.ValueString())
	}
	if !strings.Contains(im.Record.String(), "pkg:docker/alpine@3.20") {
		t.Errorf("record = %s", im.Record)
	}
	if _, diags := p3eReadDS(t, NewBuildxHistoryDataSource(), pd, map[string]tftypes.Value{"builder": p3eStr("ci"), "ref": p3eStr("aaa111"),
		"attachment": p3eStr("sha256:zzz")}); !diags.HasError() {
		t.Error("unknown attachment accepted")
	}
	// A failed provenance read without `attachment` is only a warning.
	bxhReadContent = func(context.Context, *providerData, bkNode, []string) (map[string][]byte, error) {
		return nil, errors.New("down")
	}
	st, diags = p3eReadDS(t, NewBuildxHistoryDataSource(), pd, map[string]tftypes.Value{"builder": p3eStr("ci"), "ref": p3eStr("aaa111")})
	if diags.HasError() || diags.WarningsCount() == 0 {
		t.Errorf("read failure: %v", diags)
	}
	st.Get(context.Background(), &im)
	if !im.AttachmentBody.IsNull() {
		t.Error("attachment_body set without attachment")
	}
}

// b53Config is a resource config holding v.
func b53Config(t *testing.T, sr resource.SchemaResponse, v any) tfsdk.Config {
	t.Helper()
	st := tfState(t, sr, v)
	return tfsdk.Config{Schema: sr.Schema, Raw: st.Raw}
}

// TestAccBuildxP53_multiPlatform builds a two-platform docker_image (a
// FROM scratch image, so no emulation is needed) and a bake target with a
// target: context on the harness. On a graph-driver daemon the
// multi-platform step expects the clear containerd-store error. On the
// harness it runs on the containerd-store node (TF_ACC_CONTAINERD_HOST), so
// the multi-platform build runs for real.
func TestAccBuildxP53_multiPlatform(t *testing.T) {
	testAccPreCheck(t)
	p6aUseContainerdNode(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY Dockerfile /Dockerfile\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "base.Dockerfile"), []byte("FROM "+hAccImage("busybox:1.36")+"\nRUN echo base > /base\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "app.Dockerfile"), []byte("FROM base\nRUN cat /base > /copied\n"), 0o600)
	ctx := context.Background()
	pd := &providerData{Client: testAccDockerClient(t)}
	ok, drv, err := bkImageStore(ctx, pd)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("daemon image store: containerd=%v driver=%s", ok, drv)
	step := tfresource.TestStep{Config: fmt.Sprintf(`
resource "docker_image" "multi" {
  name = "tpd-p53b-multi:1"
  build {
    context   = %q
    builder   = "buildkit"
    platforms = ["linux/amd64", "linux/arm64"]
  }
}
`, filepath.ToSlash(dir))}
	if !ok {
		step.ExpectError = regexp.MustCompile(`containerd image[\s]+store`)
	} else {
		step.Check = tfresource.TestCheckResourceAttr("docker_image.multi", "platforms.#", "2")
	}
	hAccTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []tfresource.TestStep{step}})

	// target: context through a linked build on the daemon's BuildKit.
	def, err := bakeParse([]bakeFile{{Name: "b.hcl", Data: []byte(`
target "base" {
  dockerfile = "base.Dockerfile"
}
target "app" {
  dockerfile = "app.Dockerfile"
  contexts = { base = "target:base" }
  tags = ["tpd-p53b-linked:1"]
}`)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app, err := def.resolveTarget("app", nil)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := bakeLinkSteps(def, nil, app, dir, bakeGlobals{load: true})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := bkConnect(ctx, pd, seamBuilderRef{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := bkBuildLinked(ctx, pd, conn, steps); err != nil {
		t.Fatalf("linked build: %v", err)
	}
	if _, err := pd.Client.ImageInspect(ctx, "tpd-p53b-linked:1"); err != nil {
		t.Errorf("linked image not loaded: %v", err)
	}
	_, _ = pd.Client.ImageRemove(ctx, "tpd-p53b-linked:1", client.ImageRemoveOptions{Force: true})
}
