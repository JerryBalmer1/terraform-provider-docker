package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	eschema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/container"
)

// contractTestItem registers a synthetic item for the duration of a test.
func contractTestItem(t *testing.T, it *contractItem) {
	t.Helper()
	contractItems[it.ID] = it
	t.Cleanup(func() {
		delete(contractItems, it.ID)
		contractNodeMu.Lock()
		for k := range contractNodeCache {
			if strings.HasPrefix(k, it.ID+"\x00") {
				delete(contractNodeCache, k)
			}
		}
		contractNodeMu.Unlock()
	})
}

// contractSynthetic covers every node rule: typed object, list of objects,
// map of strings, a fallback object without fields (dynamic at top, JSON
// in a list), list(dynamic), a sensitive subtree, a raw/hash pair and
// versions.
func contractSynthetic() *contractItem {
	return &contractItem{
		ID: "test:synthetic", Kind: "data_source", Name: "docker_synthetic",
		Outputs: []contractOutput{
			{Name: "id", Category: contractIdentity, TFType: "string"},
			{Name: "inspect_json", Category: contractRaw, TFType: "string", Sensitive: true, Serializes: "inspect"},
			{Name: "inspect", Category: contractStructured, TFType: "object({...})", Fields: []contractField{
				{Name: "id", Upstream: "Id", TFType: "string"},
				{Name: "size_rw", Upstream: "SizeRw", TFType: "number"},
				{Name: "running", Upstream: "Running", TFType: "bool"},
				{Name: "config", Upstream: "Config", TFType: "object({...})", Fields: []contractField{
					{Name: "image", Upstream: "Image", TFType: "string"},
					{Name: "env", Upstream: "Env", TFType: "list(string)", Sensitive: true},
					{Name: "labels", Upstream: "Labels", TFType: "map(string)"},
				}},
				{Name: "host_config", Upstream: "HostConfig", TFType: "object({...})"},
				{Name: "mounts", Upstream: "Mounts", TFType: "list(object({...}))", Fields: []contractField{
					{Name: "source", Upstream: "Source", TFType: "string"},
					{Name: "driver_opts", Upstream: "DriverOpts", TFType: "map(string)", Sensitive: true},
				}},
				{Name: "port_bindings", Upstream: "PortBindings", TFType: "map(list(object({...})))"},
				{Name: "extra", Upstream: "Extra", TFType: "list(dynamic)"},
				{Name: "secret_obj", Upstream: "SecretObj", TFType: "object({...})", Sensitive: true, Fields: []contractField{
					{Name: "value", Upstream: "Value", TFType: "string"},
				}},
			}},
			{Name: "content_sha256", Category: contractHash, TFType: "string", HashOf: "inspect_json"},
			{Name: "engine_api_version", Category: contractVersion, TFType: "string"},
			{Name: "docker_server_version", Category: contractVersion, TFType: "string"},
			{Name: "compose_go_version", Category: contractVersion, TFType: "string"},
			{Name: "dockerfile_syntax", Category: contractVersion, TFType: "string"},
			{Name: "buildkit_version", Category: contractVersion, TFType: "string"},
			{Name: "fidelity", Category: contractResult, TFType: "string"},
		},
	}
}

const contractSampleInspect = `{"Id":"abc","SizeRw":12,"Running":true,` +
	`"Config":{"Image":"nginx","Env":["A=1"],"Labels":{"com.example.Key":"v","MyLabel":"x"}},` +
	`"HostConfig":{"NetworkMode":"bridge","CpuShares":0,"Nested":{"IPAddress":"1.2.3.4"}},` +
	`"Mounts":[{"Source":"/data","DriverOpts":{"o":"secret"}}],` +
	`"PortBindings":{"80/tcp":[{"HostIp":"","HostPort":"8080"}]},` +
	`"Extra":[{"SomeKey":1},"x"],"SecretObj":{"Value":"s3cr3t"}}`

// fakeVersionEngine answers /_ping and /version, counting /version calls.
func fakeVersionEngine(t *testing.T, api string, versionCalls *int32) *providerData {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", api)
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = io.WriteString(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/version"):
			if versionCalls != nil {
				atomic.AddInt32(versionCalls, 1)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Version":"29.5.3","ApiVersion":"`+api+`","Os":"linux"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	ep := endpointConfig{Host: "tcp://" + srv.Listener.Addr().String()}
	c, err := newDockerClient(ep)
	if err != nil {
		t.Fatal(err)
	}
	return &providerData{Client: c, Endpoint: ep}
}

func TestContractSnake(t *testing.T) {
	cases := map[string]string{
		"HostConfig": "host_config", "IPAddress": "ip_address", "ID": "id", "Id": "id",
		"EnableIPv6": "enable_ipv6", "LinkLocalIPs": "link_local_ips", "NanoCPUs": "nano_cpus",
		"SELinuxContext": "selinux_context", "BlkioDeviceReadIOps": "blkio_device_read_iops",
		"CpuShares": "cpu_shares", "OomKillDisable": "oom_kill_disable", "IPv4Address": "ipv4_address",
		"already_snake": "already_snake", "keep-git-dir": "keep_git_dir", "com.docker.compose.project": "com.docker.compose.project",
		"80/tcp": "80/tcp", "MacAddress": "mac_address", "IPAMConfig": "ipam_config", "Os": "os",
	}
	for in, want := range cases {
		if got := contractSnake(in); got != want {
			t.Errorf("contractSnake(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestContractSnakeMatchesInventory checks the mechanical rule against the
// contract's own mappings: every identifier-like upstream name whose
// snake_case the inventory derived mechanically must agree.
func TestContractSnakeMatchesInventory(t *testing.T) {
	// Pairs where the inventory renames on purpose (not mechanical).
	renamed := map[string]bool{
		"BuildKit/buildkit_version": true, "INSTRUCTION/keyword": true, "Size/layer_size": true,
		"StatusCode/exit_code": true, "ID/source": true, "Names/container_name": true,
		"InsecureRegistryCIDRs/insecure_registry_cid_rs": true,
		"Services/services_count":                        true, // Q51
	}
	var walk func(id string, fs []contractField)
	total, bad := 0, 0
	walk = func(id string, fs []contractField) {
		for _, f := range fs {
			if contractIdentRe.MatchString(f.Upstream) && !renamed[f.Upstream+"/"+f.Name] {
				total++
				if got := contractSnake(f.Upstream); got != f.Name {
					bad++
					t.Errorf("%s: contractSnake(%q) = %q, inventory says %q", id, f.Upstream, got, f.Name)
				}
			}
			walk(id, f.Fields)
		}
	}
	for id, it := range contractItems {
		for _, o := range it.Outputs {
			walk(id, o.Fields)
		}
	}
	if total < 1000 {
		t.Fatalf("only %d mappings checked; is contract_gen.go populated?", total)
	}
}

func TestContractParseType(t *testing.T) {
	for _, s := range []string{"string", "list(object({...}))", "map(list(object({...})))",
		"list(object({timestamp=string, stream=string, source=string, message=string}))", "set(number)", "dynamic"} {
		if _, err := contractParseType(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"", "lst(string)", "list(string", "object({a=})", "string x"} {
		if _, err := contractParseType(s); err == nil {
			t.Errorf("%q: want error", s)
		}
	}
	e, _ := contractParseType("list(object({a=string, b=list(number)}))")
	if e.head != "list" || len(e.elem.attrs) != 2 || e.elem.attrs[1].expr.elem.head != "number" {
		t.Fatalf("parse: %+v", e.elem)
	}
}

// TestContractAllItemsSchemasValid builds the three schema flavours of
// every item and runs the framework's ValidateImplementation, which
// rejects a dynamic type inside a list/set/map.
func TestContractAllItemsSchemasValid(t *testing.T) {
	ctx := context.Background()
	if len(contractItems) < 100 {
		t.Fatalf("contractItems has %d items", len(contractItems))
	}
	for id := range contractModels {
		rs := rschema.Schema{Attributes: contractResourceAttributes(id)}
		if d := rs.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s resource schema: %v", id, d)
		}
		ds := dschema.Schema{Attributes: contractDataSourceAttributes(id)}
		if d := ds.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s data source schema: %v", id, d)
		}
		es := eschema.Schema{Attributes: contractEphemeralAttributes(id)}
		if d := es.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s ephemeral schema: %v", id, d)
		}
	}
}

// TestContractAllModelsRoundTrip sets every generated model (unknown and
// null) into a state of its own schema: the generated Go field types match
// the runtime's node types.
func TestContractAllModelsRoundTrip(t *testing.T) {
	ctx := context.Background()
	for id, mk := range contractModels {
		s := rschema.Schema{Attributes: contractResourceAttributes(id)}
		for _, mode := range []string{"unknown", "null"} {
			m := mk()
			if mode == "unknown" {
				m.contractAssign(contractUnknownValues(id))
			} else {
				m.contractAssign(contractNullValues(id))
			}
			st := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
			if d := st.Set(ctx, m); d.HasError() {
				t.Errorf("%s %s: %v", id, mode, d)
			}
		}
		if got := mk().contractItemID(); got != id {
			t.Errorf("contractItemID: %s != %s", got, id)
		}
	}
	// Every model item is in the table; identity outputs are not attributes.
	for id := range contractModels {
		attrs := contractResourceAttributes(id)
		for _, n := range contractIdentityOutputNames(id) {
			if _, ok := attrs[n]; ok && !contractIn(n, contractOutputNames(id, contractRaw, contractStructured, contractHash, contractVersion, contractResult)) {
				t.Errorf("%s: identity output %s generated as an attribute", id, n)
			}
		}
	}
}

func TestContractSchemaEngineContainer(t *testing.T) {
	attrs := contractResourceAttributes("engine:container")
	for _, want := range []string{"inspect_json", "inspect", "content_sha256", "engine_api_version", "docker_server_version"} {
		if _, ok := attrs[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	for _, no := range []string{"id", "name", "image_id", "labels"} {
		if _, ok := attrs[no]; ok {
			t.Errorf("identity output %s must not be generated", no)
		}
	}
	if got := contractIdentityOutputNames("engine:container"); strings.Join(got, ",") != "id,name,image_id,labels" {
		t.Errorf("identity names: %v", got)
	}
	raw := attrs["inspect_json"].(rschema.StringAttribute)
	if !raw.Computed || !raw.Sensitive {
		t.Errorf("inspect_json: %+v", raw)
	}
	if h := attrs["content_sha256"].(rschema.StringAttribute); h.Sensitive {
		t.Error("hash must not be sensitive")
	}
	insp, ok := attrs["inspect"].(rschema.SingleNestedAttribute)
	if !ok || insp.Sensitive || !insp.Computed {
		t.Fatalf("inspect: %T %+v", attrs["inspect"], attrs["inspect"])
	}
	cfg := insp.Attributes["config"].(rschema.SingleNestedAttribute)
	if env := cfg.Attributes["env"]; !env.IsSensitive() {
		t.Error("inspect.config.env must be sensitive")
	}
	if img := cfg.Attributes["image"]; img.IsSensitive() {
		t.Error("inspect.config.image must be readable")
	}
	// Pass 6c: shapes come from the swagger. PortBindings is a map of lists
	// of objects (a plain map attribute of list(object)), not JSON strings.
	hc := insp.Attributes["host_config"].(rschema.SingleNestedAttribute)
	pb, ok := hc.Attributes["port_bindings"].(rschema.MapAttribute)
	want := types.ListType{ElemType: types.ObjectType{AttrTypes: map[string]attr.Type{"host_ip": types.StringType, "host_port": types.StringType}}}
	if !ok || !pb.ElementType.Equal(want) {
		t.Fatalf("port_bindings: %T %+v", hc.Attributes["port_bindings"], hc.Attributes["port_bindings"])
	}
	if strings.Contains(pb.MarkdownDescription, "canonical JSON") {
		t.Errorf("port_bindings description: %s", pb.MarkdownDescription)
	}
}

func TestContractSchemaSwarmService(t *testing.T) {
	attrs := contractDataSourceAttributes("swarm:service")
	pp, ok := attrs["published_ports"].(dschema.ListNestedAttribute)
	if !ok {
		t.Fatalf("published_ports: %T", attrs["published_ports"])
	}
	if len(pp.NestedObject.Attributes) == 0 {
		t.Fatal("published_ports has no nested attributes")
	}
	insp := attrs["inspect"].(dschema.SingleNestedAttribute)
	spec := insp.Attributes["spec"].(dschema.SingleNestedAttribute)
	tt := spec.Attributes["task_template"].(dschema.SingleNestedAttribute)
	cs := tt.Attributes["container_spec"].(dschema.SingleNestedAttribute)
	if !cs.Attributes["env"].IsSensitive() || cs.Attributes["image"].IsSensitive() {
		t.Error("container_spec: env must be sensitive, image readable")
	}
	if _, ok := contractEphemeralAttributes("swarm:service")["inspect"].(eschema.SingleNestedAttribute); !ok {
		t.Error("ephemeral flavour")
	}
}

func TestContractSchemaLogs(t *testing.T) {
	attrs := contractDataSourceAttributes("engine:container-logs")
	le := attrs["log_entries"].(dschema.ListNestedAttribute)
	for name, a := range le.NestedObject.Attributes {
		if a.IsSensitive() != (name == "message") {
			t.Errorf("log_entries.%s sensitive = %v", name, a.IsSensitive())
		}
	}
	if !attrs["logs"].IsSensitive() || attrs["content_sha256"].IsSensitive() {
		t.Error("logs sensitive, content_sha256 not")
	}
	it := contractMustItem("engine:container-logs")
	if len(it.Inputs) < 2 || it.Inputs[0].Name != "tail" || it.Inputs[0].Default != "1000" {
		t.Errorf("inputs: %+v", it.Inputs)
	}
}

func TestContractSyntheticSchema(t *testing.T) {
	contractTestItem(t, contractSynthetic())
	attrs := contractDataSourceAttributes("test:synthetic")
	if _, ok := attrs["id"]; ok {
		t.Error("identity generated")
	}
	if _, ok := attrs["fidelity"].(dschema.StringAttribute); !ok {
		t.Error("result output of a data source must be an attribute")
	}
	insp := attrs["inspect"].(dschema.SingleNestedAttribute)
	if _, ok := insp.Attributes["host_config"].(dschema.DynamicAttribute); !ok {
		t.Errorf("host_config fallback (no collection ancestor): %T", insp.Attributes["host_config"])
	}
	ex, ok := insp.Attributes["extra"].(dschema.ListAttribute)
	if !ok || !ex.ElementType.Equal(types.StringType) {
		t.Errorf("list(dynamic) fallback: %T", insp.Attributes["extra"])
	}
	m := insp.Attributes["mounts"].(dschema.ListNestedAttribute)
	if !m.NestedObject.Attributes["driver_opts"].IsSensitive() || m.NestedObject.Attributes["source"].IsSensitive() {
		t.Error("mounts sensitivity")
	}
	so := insp.Attributes["secret_obj"].(dschema.SingleNestedAttribute)
	if !so.Sensitive || !so.Attributes["value"].IsSensitive() {
		t.Error("sensitive subtree")
	}
	if insp.Sensitive {
		t.Error("structured output must not be sensitive as a whole")
	}
}

// contractSyntheticOutputs is hand-written in the shape contractgen
// generates, for the synthetic item.
type contractSyntheticOutputs struct {
	InspectJSON         types.String `tfsdk:"inspect_json"`
	Inspect             types.Object `tfsdk:"inspect"`
	ContentSHA256       types.String `tfsdk:"content_sha256"`
	EngineAPIVersion    types.String `tfsdk:"engine_api_version"`
	DockerServerVersion types.String `tfsdk:"docker_server_version"`
	ComposeGoVersion    types.String `tfsdk:"compose_go_version"`
	DockerfileSyntax    types.String `tfsdk:"dockerfile_syntax"`
	BuildkitVersion     types.String `tfsdk:"buildkit_version"`
	Fidelity            types.String `tfsdk:"fidelity"`
}

func (*contractSyntheticOutputs) contractItemID() string { return "test:synthetic" }
func (m *contractSyntheticOutputs) contractAssign(v map[string]attr.Value) {
	m.InspectJSON = v["inspect_json"].(types.String)
	m.Inspect = v["inspect"].(types.Object)
	m.ContentSHA256 = v["content_sha256"].(types.String)
	m.EngineAPIVersion = v["engine_api_version"].(types.String)
	m.DockerServerVersion = v["docker_server_version"].(types.String)
	m.ComposeGoVersion = v["compose_go_version"].(types.String)
	m.DockerfileSyntax = v["dockerfile_syntax"].(types.String)
	m.BuildkitVersion = v["buildkit_version"].(types.String)
	m.Fidelity = v["fidelity"].(types.String)
}

// contractSyntheticModel is a lane-style model embedding the outputs.
type contractSyntheticModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
	contractSyntheticOutputs
}

func TestContractFillRoundTrip(t *testing.T) {
	ctx := context.Background()
	contractTestItem(t, contractSynthetic())
	var calls int32
	pd := fakeVersionEngine(t, "1.47", &calls)

	m := contractSyntheticModel{ID: types.StringValue("abc"), Name: types.StringValue("web")}
	// Structured omitted: decoded from inspect_json.
	diags := m.contractFill(ctx, pd, contractFillInput{
		Raw:              map[string][]byte{"inspect_json": []byte(contractSampleInspect)},
		Result:           map[string]any{"fidelity": "exact"},
		DockerfileSyntax: "",
	})
	if diags.HasError() || diags.WarningsCount() > 0 {
		t.Fatalf("fill: %v", diags)
	}
	// Second fill: the server version is cached.
	var again contractSyntheticOutputs
	_ = contractFillModel(ctx, pd, &again, contractFillInput{Raw: map[string][]byte{"inspect_json": []byte(contractSampleInspect)}})
	if calls != 1 {
		t.Errorf("GET /version called %d times, want 1 (cached)", calls)
	}

	sum := sha256.Sum256([]byte(contractSampleInspect))
	if m.ContentSHA256.ValueString() != hex.EncodeToString(sum[:]) {
		t.Errorf("hash %s", m.ContentSHA256.ValueString())
	}
	if m.InspectJSON.ValueString() != contractSampleInspect {
		t.Error("raw must be the exact bytes")
	}
	if m.EngineAPIVersion.ValueString() != "1.47" || m.DockerServerVersion.ValueString() != "29.5.3" {
		t.Errorf("versions: %s %s", m.EngineAPIVersion, m.DockerServerVersion)
	}
	if m.ComposeGoVersion.ValueString() != contractComposeGoVersion || contractComposeGoVersion != "v2.16.2" {
		t.Errorf("compose_go_version %s", m.ComposeGoVersion)
	}
	if m.DockerfileSyntax.ValueString() != contractDockerfileDefaultSyntax || !m.BuildkitVersion.IsNull() {
		t.Errorf("dockerfile_syntax %s buildkit %s", m.DockerfileSyntax, m.BuildkitVersion)
	}

	// Into a real state and back.
	attrs := map[string]dschema.Attribute{
		"id":   dschema.StringAttribute{Computed: true},
		"name": dschema.StringAttribute{Computed: true},
	}
	s := dschema.Schema{Attributes: contractAddDataSourceAttributes(attrs, "test:synthetic")}
	st := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if d := st.Set(ctx, &m); d.HasError() {
		t.Fatalf("State.Set: %v", d)
	}
	var back contractSyntheticModel
	if d := st.Get(ctx, &back); d.HasError() {
		t.Fatalf("State.Get: %v", d)
	}
	insp := back.Inspect.Attributes()
	if insp["id"].(types.String).ValueString() != "abc" || insp["running"].(types.Bool).ValueBool() != true {
		t.Errorf("inspect: %v", insp)
	}
	if n, _ := insp["size_rw"].(types.Number).ValueBigFloat().Int64(); n != 12 {
		t.Errorf("size_rw %v", insp["size_rw"])
	}
	cfg := insp["config"].(types.Object).Attributes()
	labels := cfg["labels"].(types.Map).Elements()
	if labels["com.example.Key"].(types.String).ValueString() != "v" || labels["MyLabel"] == nil {
		t.Errorf("typed map keys must stay as is: %v", labels)
	}
	// Dynamic fallback: no schema below it, so keys pass through untouched (Pass 6c).
	hc := insp["host_config"].(types.Dynamic).UnderlyingValue().(types.Object).Attributes()
	if hc["NetworkMode"].(types.String).ValueString() != "bridge" {
		t.Errorf("host_config: %v", hc)
	}
	if hc["Nested"].(types.Object).Attributes()["IPAddress"].(types.String).ValueString() != "1.2.3.4" {
		t.Errorf("host_config.nested: %v", hc["nested"])
	}
	// JSON fallback below a collection.
	pb := insp["port_bindings"].(types.Map).Elements()["80/tcp"].(types.List).Elements()
	if got := pb[0].(types.String).ValueString(); got != `{"HostIp":"","HostPort":"8080"}` {
		t.Errorf("port_bindings JSON: %s", got)
	}
	ex := insp["extra"].(types.List).Elements()
	// A string element is stored as is, never JSON-encoded twice (Pass 6c).
	if ex[0].(types.String).ValueString() != `{"SomeKey":1}` || ex[1].(types.String).ValueString() != `x` {
		t.Errorf("extra: %v", ex)
	}
	mounts := insp["mounts"].(types.List).Elements()
	if mounts[0].(types.Object).Attributes()["source"].(types.String).ValueString() != "/data" {
		t.Errorf("mounts: %v", mounts)
	}
	if back.Fidelity.ValueString() != "exact" {
		t.Errorf("fidelity %s", back.Fidelity)
	}
}

func TestContractFillStructuredOnly(t *testing.T) {
	ctx := context.Background()
	contractTestItem(t, contractSynthetic())
	var up struct {
		ID     string `json:"Id"`
		Config struct {
			Image string
		}
	}
	up.ID = "x1"
	up.Config.Image = "alpine"
	var m contractSyntheticOutputs
	d := m.contractFillVia(ctx, contractFillInput{Structured: map[string]any{"inspect": up}})
	if d.HasError() {
		t.Fatal(d)
	}
	// The *_json raw is serialized from the structured value.
	var back map[string]any
	if err := json.Unmarshal([]byte(m.InspectJSON.ValueString()), &back); err != nil || back["Id"] != "x1" {
		t.Errorf("inspect_json %s", m.InspectJSON)
	}
	if m.Inspect.Attributes()["config"].(types.Object).Attributes()["image"].(types.String).ValueString() != "alpine" {
		t.Error("structured from struct")
	}
	// No pd: engine versions are null, pins still set.
	if !m.EngineAPIVersion.IsNull() || m.ComposeGoVersion.IsNull() {
		t.Error("versions without pd")
	}
}

func (m *contractSyntheticOutputs) contractFill(ctx context.Context, pd *providerData, in contractFillInput) diag.Diagnostics {
	return contractFillModel(ctx, pd, m, in)
}

func (m *contractSyntheticOutputs) contractFillVia(ctx context.Context, in contractFillInput) diag.Diagnostics {
	return contractFillModel(ctx, nil, m, in)
}

func TestContractShapeMismatchWarns(t *testing.T) {
	ctx := context.Background()
	contractTestItem(t, contractSynthetic())
	var m contractSyntheticOutputs
	d := contractFillModel(ctx, nil, &m, contractFillInput{Structured: map[string]any{"inspect": map[string]any{"Mounts": "nope", "SizeRw": true}}})
	if d.HasError() || d.WarningsCount() != 2 {
		t.Fatalf("want 2 warnings, got %v", d)
	}
	if !m.Inspect.Attributes()["mounts"].IsNull() {
		t.Error("mismatched value must be null")
	}
}

func TestContractUnknownValues(t *testing.T) {
	v := contractUnknownValue("engine:container", "inspect")
	if !v.IsUnknown() {
		t.Fatal("want unknown")
	}
	var m contractEngineContainer
	m.contractSetUnknown()
	if !m.Inspect.IsUnknown() || !m.ContentSHA256.IsUnknown() {
		t.Error("contractSetUnknown")
	}
	m.contractSetNull()
	if !m.Inspect.IsNull() {
		t.Error("contractSetNull")
	}
}

func TestContractOutputNames(t *testing.T) {
	got := contractOutputNames("engine:container-logs", contractRaw, contractHash)
	if strings.Join(got, ",") != "logs,content_sha256" {
		t.Errorf("got %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("unknown id must panic")
		}
	}()
	contractOutputNames("nope:nope")
}

// TestContractFillEngineContainer fills the generated engine:container
// struct from a moby InspectResponse plus raw JSON and stores it in a
// resource state built from the generated schema.
func TestContractFillEngineContainer(t *testing.T) {
	ctx := context.Background()
	pd := fakeVersionEngine(t, "1.52", nil)
	up := container.InspectResponse{
		ID:     "c0ffee",
		Name:   "/web",
		Config: &container.Config{Image: "nginx:1.27", Env: []string{"TOKEN=x"}, Labels: map[string]string{"a.b": "c"}},
		HostConfig: &container.HostConfig{
			NetworkMode: "bridge",
		},
	}
	raw, err := json.Marshal(up)
	if err != nil {
		t.Fatal(err)
	}
	type model struct {
		ID types.String `tfsdk:"id"`
		contractEngineContainer
	}
	var m model
	m.ID = types.StringValue("c0ffee")
	if d := m.contractFill(ctx, pd, contractFillInput{Raw: map[string][]byte{"inspect_json": raw}, Structured: map[string]any{"inspect": up}}); d.HasError() {
		t.Fatal(d)
	}
	s := rschema.Schema{Attributes: contractAddResourceAttributes(map[string]rschema.Attribute{"id": rschema.StringAttribute{Computed: true}}, "engine:container")}
	st := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if d := st.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	var img types.String
	if d := st.GetAttribute(ctx, path.Root("inspect").AtName("config").AtName("image"), &img); d.HasError() || img.ValueString() != "nginx:1.27" {
		t.Fatalf("inspect.config.image = %s %v", img, d)
	}
	if m.EngineAPIVersion.ValueString() != "1.52" {
		t.Errorf("engine_api_version %s", m.EngineAPIVersion)
	}
}

func TestContractAddAttributesClashPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want panic on a clash")
		}
	}()
	contractAddResourceAttributes(map[string]rschema.Attribute{"inspect_json": rschema.StringAttribute{Computed: true}}, "engine:container")
}
