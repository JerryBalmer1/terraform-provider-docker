package provider

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// ---- test helpers ---------------------------------------------------------

// kwMoveResult runs r's MoveState the way the framework server does
// (fwserver MoveResourceState): target state null of r's schema, target
// identity null of r's identity schema, movers tried in order.
func kwMoveResult(t *testing.T, r resource.Resource, addr, srcType string, version int64, raw string) (*resource.MoveStateResponse, tftypes.Type) {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics)
	}
	typ := sr.Schema.Type().TerraformType(ctx)
	mover, ok := r.(resource.ResourceWithMoveState)
	if !ok {
		t.Fatalf("%T does not implement ResourceWithMoveState", r)
	}
	for _, m := range mover.MoveState(ctx) {
		resp := &resource.MoveStateResponse{TargetState: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
		if ri, ok := r.(resource.ResourceWithIdentity); ok {
			var ir resource.IdentitySchemaResponse
			ri.IdentitySchema(ctx, resource.IdentitySchemaRequest{}, &ir)
			resp.TargetIdentity = &tfsdk.ResourceIdentity{Schema: ir.IdentitySchema, Raw: tftypes.NewValue(ir.IdentitySchema.Type().TerraformType(ctx), nil)}
		}
		req := resource.MoveStateRequest{
			SourceProviderAddress: addr,
			SourceTypeName:        srcType,
			SourceSchemaVersion:   version,
			SourceRawState:        &tfprotov6.RawState{JSON: []byte(raw)},
		}
		m.StateMover(ctx, req, resp)
		if resp.Diagnostics.HasError() || !resp.TargetState.Raw.IsNull() {
			return resp, typ
		}
	}
	return nil, typ
}

// kwMustMove runs the move from kreuzwerker and returns the target as Go values.
func kwMustMove(t *testing.T, r resource.Resource, srcType string, version int64, raw string) (map[string]any, tftypes.Type, *resource.MoveStateResponse) {
	t.Helper()
	resp, typ := kwMoveResult(t, r, kreuzwerkerProviderAddress, srcType, version, raw)
	if resp == nil {
		t.Fatalf("no mover handled %s", srcType)
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("move: %v", resp.Diagnostics)
	}
	got, _ := tfToGo(resp.TargetState.Raw).(map[string]any)
	if got == nil {
		t.Fatal("moved state is null")
	}
	return got, typ, resp
}

// tfToGo converts a tftypes.Value to map[string]any / []any / string /
// float64 / bool / nil.
func tfToGo(v tftypes.Value) any {
	if v.IsNull() || !v.IsKnown() {
		return nil
	}
	typ := v.Type()
	switch {
	case typ.Is(tftypes.Object{}), typ.Is(tftypes.Map{}):
		var m map[string]tftypes.Value
		_ = v.As(&m)
		out := map[string]any{}
		for k, e := range m {
			out[k] = tfToGo(e)
		}
		return out
	case typ.Is(tftypes.List{}), typ.Is(tftypes.Set{}):
		var l []tftypes.Value
		_ = v.As(&l)
		out := []any{}
		for _, e := range l {
			out = append(out, tfToGo(e))
		}
		return out
	case typ.Is(tftypes.String):
		var s string
		_ = v.As(&s)
		return s
	case typ.Is(tftypes.Number):
		f := new(big.Float)
		_ = v.As(&f)
		x, _ := f.Float64()
		return x
	case typ.Is(tftypes.Bool):
		var b bool
		_ = v.As(&b)
		return b
	}
	return nil
}

// kwTypeHas reports whether typ has the dotted path (numeric segments index
// lists and sets, other segments under a map are keys).
func kwTypeHas(typ tftypes.Type, p string) bool {
	cur := typ
	for _, seg := range strings.Split(p, ".") {
		// Collections do not consume a path segment: renames.json writes a field
		// inside a list/set element or a map value as <attr>.<field>.
		for {
			switch t := cur.(type) {
			case tftypes.List:
				cur = t.ElementType
				continue
			case tftypes.Set:
				cur = t.ElementType
				continue
			case tftypes.Map:
				cur = t.ElementType
				continue
			}
			break
		}
		t, ok := cur.(tftypes.Object)
		if !ok {
			return false
		}
		at, ok := t.AttributeTypes[seg]
		if !ok {
			return false
		}
		cur = at
	}
	return true
}

// kwGoAt returns the Go value at the dotted path (numeric segments index lists).
func kwGoAt(v any, p string) any {
	for _, seg := range strings.Split(p, ".") {
		switch t := v.(type) {
		case map[string]any:
			v = t[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i >= len(t) {
				return nil
			}
			v = t[i]
		default:
			return nil
		}
	}
	return v
}

// kwExpect checks got at p against want when typ has p; when the current
// schema does not have p (another lane renames or adds it in Pass 5) the
// check is logged and skipped.
func kwExpect(t *testing.T, got map[string]any, typ tftypes.Type, p string, want any) {
	t.Helper()
	if !kwTypeHas(typ, p) {
		t.Logf("schema has no %s yet: not checked", p)
		return
	}
	g := kwGoAt(got, p)
	if l, ok := g.([]any); ok && isSetPath(typ, p) {
		sort.Slice(l, func(i, j int) bool { return toJSON(l[i]) < toJSON(l[j]) })
	}
	if !reflect.DeepEqual(g, want) {
		t.Errorf("%s = %s, want %s", p, toJSON(g), toJSON(want))
	}
}

func isSetPath(typ tftypes.Type, p string) bool {
	cur := typ
	for _, seg := range strings.Split(p, ".") {
		switch t := cur.(type) {
		case tftypes.Object:
			cur = t.AttributeTypes[seg]
		case tftypes.List:
			cur = t.ElementType
		case tftypes.Set:
			cur = t.ElementType
		case tftypes.Map:
			cur = t.ElementType
		}
	}
	_, ok := cur.(tftypes.Set)
	return ok
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Compact tftypes builders for the post-Pass-5 target schemas in the tests.
var (
	tS = tftypes.String
	tN = tftypes.Number
	tB = tftypes.Bool
)

func tObj(kv ...any) tftypes.Object {
	m := map[string]tftypes.Type{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1].(tftypes.Type)
	}
	return tftypes.Object{AttributeTypes: m}
}
func tList(e tftypes.Type) tftypes.Type { return tftypes.List{ElementType: e} }
func tSet(e tftypes.Type) tftypes.Type  { return tftypes.Set{ElementType: e} }
func tMap(e tftypes.Type) tftypes.Type  { return tftypes.Map{ElementType: e} }

// kwValueGo runs m against typ (a post-Pass-5 target type) and returns Go values.
func kwValueGo(t *testing.T, m kwMove, typ tftypes.Type, raw string) map[string]any {
	t.Helper()
	v, skipped, err := m.value(typ, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skipped {
		t.Logf("skipped: %s", s)
	}
	got, _ := tfToGo(v).(map[string]any)
	return got
}

// ---- engine tests ---------------------------------------------------------

func TestKwIsSource(t *testing.T) {
	for addr, want := range map[string]bool{
		"registry.terraform.io/kreuzwerker/docker":  true,
		"Registry.Terraform.IO/Kreuzwerker/Docker":  true,
		"mirror.example.com/kreuzwerker/docker":     true,
		"registry.terraform.io/jerrybalmer1/docker": false,
		"registry.terraform.io/kreuzwerker/dockerx": false,
		"": false,
	} {
		if got := kwIsSource(addr); got != want {
			t.Errorf("kwIsSource(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestKwGetSet(t *testing.T) {
	src, err := kwDecode([]byte(`{"a":[{"b":[{"c":5}]}],"l":[],"s":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := kwString(kwGet(src, "a.b.c")); got != "5" {
		t.Errorf("a.b.c = %q", got)
	}
	if kwGet(src, "l.x") != nil || kwGet(src, "s.x") != nil || kwGet(src, "missing") != nil {
		t.Error("missing paths must be nil")
	}
	dst := map[string]any{}
	kwSet(dst, "x.y.z", 1)
	kwSet(dst, "x.y.w", nil)
	if !reflect.DeepEqual(dst, map[string]any{"x": map[string]any{"y": map[string]any{"z": 1}}}) {
		t.Errorf("dst = %v", dst)
	}
	if _, err := kwDecode([]byte(`null`)); err == nil {
		t.Error("null JSON must fail")
	}
	if _, err := kwDecode([]byte(`{`)); err == nil {
		t.Error("bad JSON must fail")
	}
}

func TestKwTransforms(t *testing.T) {
	num := func(v any) float64 {
		f, ok := kwNumber(v)
		if !ok {
			t.Fatalf("%v is not a number", v)
		}
		x, _ := f.Float64()
		return x
	}
	if got := num(tfMBToBytes(false)(json.Number("512"))); got != 536870912 {
		t.Errorf("512 MB = %v", got)
	}
	if got := num(tfMBToBytes(true)(json.Number("-1"))); got != -1 {
		t.Errorf("-1 MB swap = %v", got)
	}
	if got := num(tfCPUsToNano("1.5")); got != 1.5e9 {
		t.Errorf("cpus 1.5 = %v", got)
	}
	if tfCPUsToNano("") != nil || tfCPUsToNano("0") != nil {
		t.Error("empty or zero cpus must be dropped")
	}
	if got := tfSecondsToDuration(json.Number("60")); got != "60s" {
		t.Errorf("wait_timeout 60 = %v", got)
	}
	if got := tfKVListToMap([]any{"A=1", "B", "C=x=y", ""}); !reflect.DeepEqual(got, map[string]any{"A": "1", "B": "", "C": "x=y"}) {
		t.Errorf("env = %v", got)
	}
	if tfKVListToMap([]any{}) != nil {
		t.Error("empty env must be nil")
	}
	if got := tfBase64("hello"); got != "aGVsbG8=" {
		t.Errorf("base64 = %v", got)
	}
	if got := num(tfMemoryString("512m")); got != 536870912 {
		t.Errorf("512m = %v", got)
	}
	if got := tfMemoryString("lots"); got != "lots" {
		t.Errorf("unparsable memory must pass through, got %v", got)
	}
	for in, want := range map[string]string{
		"nginx:1.27":                    "nginx",
		"host:5000/app:1.0":             "host:5000/app",
		"host:5000/app":                 "host:5000/app",
		"app@sha256:abc":                "app",
		"r.example.com/a/b:t@sha256:ab": "r.example.com/a/b",
	} {
		if got := kwRepoOf(in); got != want {
			t.Errorf("kwRepoOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKwToValueCoercion(t *testing.T) {
	typ := tObj(
		"s", tS, "n", tN, "b", tB, "ns", tS, "sn", tN, "sb", tB, "bad", tN,
		"empty_s", tS, "empty_l", tList(tS), "empty_m", tMap(tS),
		"one", tObj("x", tS), "set", tSet(tS), "wrap", tList(tObj("x", tS)),
		"dyn", tftypes.DynamicPseudoType, "notmap", tMap(tS), "nested_null", tObj("x", tS),
	)
	src := map[string]any{
		"s": "v", "n": json.Number("12345678901234567890"), "b": true, "ns": json.Number("7"), "sn": "3.5", "sb": "true",
		"bad": "abc", "empty_s": "", "empty_l": []any{}, "empty_m": map[string]any{},
		"one": []any{map[string]any{"x": "first"}, map[string]any{"x": "second"}}, "set": []any{"a", "b"},
		"wrap": map[string]any{"x": "w"}, "dyn": "d", "notmap": "x", "nested_null": map[string]any{"x": ""},
		"extra": "not in schema",
	}
	var skipped []string
	v := kwToValue(typ, src, "", &skipped)
	got := tfToGo(v).(map[string]any)
	want := map[string]any{
		"s": "v", "n": 12345678901234567890.0, "b": true, "ns": "7", "sn": 3.5, "sb": true, "bad": nil,
		"empty_s": nil, "empty_l": nil, "empty_m": nil, "one": map[string]any{"x": "first"},
		"set": []any{"a", "b"}, "wrap": []any{map[string]any{"x": "w"}}, "dyn": nil, "notmap": nil, "nested_null": nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %s\nwant %s", toJSON(got), toJSON(want))
	}
	joined := strings.Join(skipped, "\n")
	for _, s := range []string{"extra (not in the target schema)", "bad (abc is not a number)", "one (2 elements", "dyn (dynamic", "notmap (not a map)"} {
		if !strings.Contains(joined, s) {
			t.Errorf("skipped lacks %q:\n%s", s, joined)
		}
	}
	// Exact large integers survive.
	var f big.Float
	var m map[string]tftypes.Value
	_ = v.As(&m)
	_ = m["n"].As(&f)
	if f.Text('f', 0) != "12345678901234567890" {
		t.Errorf("n = %s", f.Text('f', 0))
	}
}

func TestKwMoveErrorsAndNoop(t *testing.T) {
	r := NewVolumeResource()
	raw := `{"id":"v1","name":"v1","driver":"local"}`
	// Another provider or type: not handled, no diagnostics.
	if resp, _ := kwMoveResult(t, r, "registry.terraform.io/other/docker", "docker_volume", 1, raw); resp != nil {
		t.Errorf("other provider handled: %v", resp.Diagnostics)
	}
	if resp, _ := kwMoveResult(t, r, kreuzwerkerProviderAddress, "docker_network", 1, raw); resp != nil {
		t.Errorf("other type handled: %v", resp.Diagnostics)
	}
	// Old schema version.
	resp, _ := kwMoveResult(t, r, kreuzwerkerProviderAddress, "docker_volume", 0, raw)
	if resp == nil || !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), "refresh-only") {
		t.Errorf("old version: want an error naming refresh-only, got %+v", resp)
	}
	// No JSON.
	resp, _ = kwMoveResult(t, r, kreuzwerkerProviderAddress, "docker_volume", 1, "")
	if resp == nil || !resp.Diagnostics.HasError() {
		t.Error("empty raw state: want an error")
	}
	// Bad JSON.
	resp, _ = kwMoveResult(t, r, kreuzwerkerProviderAddress, "docker_volume", 1, "[1]")
	if resp == nil || !resp.Diagnostics.HasError() {
		t.Error("non-object raw state: want an error")
	}
	// Nothing maps.
	resp, _ = kwMoveResult(t, r, kreuzwerkerProviderAddress, "docker_volume", 1, `{"unknown":"x"}`)
	if resp == nil || !resp.Diagnostics.HasError() {
		t.Error("nothing mapped: want an error")
	}
	// Newer version: moved with a warning log only.
	got, _, _ := kwMustMove(t, r, "docker_volume", 5, raw)
	if got["name"] != "v1" {
		t.Errorf("newer version: name = %v", got["name"])
	}
}

// TestKwMoveProtocol drives MoveResourceState through the provider server,
// as Terraform does for a cross-provider moved block.
func TestKwMoveProtocol(t *testing.T) {
	ctx := context.Background()
	srv, err := kwProtoServer()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.MoveResourceState(ctx, &tfprotov6.MoveResourceStateRequest{
		SourceProviderAddress: kreuzwerkerProviderAddress,
		SourceTypeName:        "docker_volume",
		SourceSchemaVersion:   1,
		SourceState:           &tfprotov6.RawState{JSON: []byte(kwFixtureVolume)},
		TargetTypeName:        "docker_volume",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("%s: %s", d.Summary, d.Detail)
		}
	}
	if resp.TargetState == nil {
		t.Fatal("no target state")
	}
	if resp.TargetIdentity == nil || resp.TargetIdentity.IdentityData == nil {
		t.Error("no target identity")
	}
	// Unknown source type: the framework's error.
	resp, err = srv.MoveResourceState(ctx, &tfprotov6.MoveResourceStateRequest{
		SourceProviderAddress: kreuzwerkerProviderAddress,
		SourceTypeName:        "docker_container",
		SourceSchemaVersion:   2,
		SourceState:           &tfprotov6.RawState{JSON: []byte(kwFixtureVolume)},
		TargetTypeName:        "docker_volume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Diagnostics) == 0 || !strings.Contains(resp.Diagnostics[0].Detail, "does not include support for the given source resource") {
		t.Errorf("want the framework's unsupported-source error, got %+v", resp.Diagnostics)
	}
}

// ---- coverage against reference/_gen/renames.json -------------------------

// kwMoves maps each of our resource types to its mover.
var kwMoves = map[string]kwMove{
	"docker_buildx_builder": kwMoveBuildxBuilder,
	"docker_compose":        kwMoveCompose,
	"docker_config":         kwMoveConfig,
	"docker_container":      kwMoveContainer,
	"docker_image":          kwMoveImage,
	"docker_network":        kwMoveNetwork,
	"docker_plugin":         kwMovePlugin,
	"docker_registry_image": kwMoveRegistryImage,
	"docker_secret":         kwMoveSecret,
	"docker_service":        kwMoveService,
	"docker_tag":            kwMoveTag,
	"docker_volume":         kwMoveVolume,
}

type kwRenameRow struct {
	Type            string  `json:"type"`
	KreuzwerkerAttr *string `json:"kreuzwerker_attr"`
	OurAttr         *string `json:"our_attr"`
	Transform       string  `json:"transform"`
	Reason          string  `json:"reason"`
}

func kwLoadRenames(t *testing.T) []kwRenameRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "reference", "_gen", "renames.json"))
	if err != nil {
		t.Skipf("renames.json not readable: %v", err)
	}
	var doc struct {
		Renames []kwRenameRow `json:"renames"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Renames
}

func kwPairs(m kwMove) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, r := range m.rules {
		for _, p := range r.pairs {
			out[p] = true
		}
	}
	return out
}

// TestMoveCoversRenames: every renames.json row of a managed resource type
// that maps a kreuzwerker attribute (our_attr set) is a pair of that type's
// mover, every kreuzwerker resource type has a mover, and every gap row
// ("if added, name it `x`") is mapped forward to x.
func TestMoveCoversRenames(t *testing.T) {
	rows := kwLoadRenames(t)
	seenTypes := map[string]bool{}
	gapName := regexp.MustCompile("if added, name it `([^`]+)`")
	for _, r := range rows {
		if r.KreuzwerkerAttr == nil || strings.Contains(r.Type, ".") {
			continue // our own renames, data sources, actions
		}
		seenTypes[r.Type] = true
		m, ok := kwMoves[r.Type]
		if !ok {
			t.Errorf("%s: no kwMove", r.Type)
			continue
		}
		pairs := kwPairs(m)
		switch {
		case r.OurAttr != nil:
			if !pairs[[2]string{*r.KreuzwerkerAttr, *r.OurAttr}] {
				t.Errorf("%s: %s -> %s (%s) is not mapped", r.Type, *r.KreuzwerkerAttr, *r.OurAttr, r.Transform)
			}
		case strings.HasPrefix(r.Reason, "gap:"):
			g := gapName.FindStringSubmatch(r.Reason)
			if g == nil || strings.ContainsAny(g[1], " *(") {
				continue
			}
			found := false
			for p := range pairs {
				if p[0] == *r.KreuzwerkerAttr && (p[1] == g[1] || strings.HasSuffix(g[1], "."+p[1])) {
					found = true
				}
			}
			if !found {
				t.Logf("%s: gap %s -> %s not mapped forward", r.Type, *r.KreuzwerkerAttr, g[1])
			}
		}
	}
	for typ := range kwMoves {
		if !seenTypes[typ] {
			t.Errorf("%s has a kwMove but no kreuzwerker rows in renames.json", typ)
		}
	}
}

// TestMoveTargetsInSchema lists mover targets the current schema lacks.
// They are expected while Pass 5 lanes rename attributes in parallel; set
// TPD_MOVE_STRICT=1 after the merge to make each one a failure (forward
// mappings for renames.json gaps are always allowed to be absent).
func TestMoveTargetsInSchema(t *testing.T) {
	ctx := context.Background()
	strict := os.Getenv("TPD_MOVE_STRICT") == "1"
	rows := kwLoadRenames(t)
	mapped := map[string]map[[2]string]bool{}
	for _, r := range rows {
		if r.KreuzwerkerAttr != nil && r.OurAttr != nil && !strings.Contains(r.Type, ".") {
			if mapped[r.Type] == nil {
				mapped[r.Type] = map[[2]string]bool{}
			}
			mapped[r.Type][[2]string{*r.KreuzwerkerAttr, *r.OurAttr}] = true
		}
	}
	ctors := map[string]func() resource.Resource{
		"docker_buildx_builder": NewBuildxBuilderResource, "docker_compose": NewComposeResource,
		"docker_config": NewConfigResource, "docker_container": NewContainerResource, "docker_image": NewImageResource,
		"docker_network": NewNetworkResource, "docker_plugin": NewPluginResource,
		"docker_registry_image": NewRegistryImageResource, "docker_secret": NewSecretResource,
		"docker_service": NewServiceResource, "docker_tag": NewTagResource, "docker_volume": NewVolumeResource,
	}
	for typ, m := range kwMoves {
		var sr resource.SchemaResponse
		ctors[typ]().Schema(ctx, resource.SchemaRequest{}, &sr)
		tt := sr.Schema.Type().TerraformType(ctx)
		for p := range kwPairs(m) {
			if p[1] == "" || !mapped[typ][p] {
				continue
			}
			if !kwTypeHas(tt, p[1]) {
				if strict {
					t.Errorf("%s: target %s (from %s) is not in the schema", typ, p[1], p[0])
				} else {
					t.Logf("%s: target %s (from %s) is not in the schema yet", typ, p[1], p[0])
				}
			}
		}
	}
}

// kwProtoServer is the provider's protocol 6 server, as Terraform runs it.
func kwProtoServer() (tfprotov6.ProviderServer, error) {
	return providerserver.NewProtocol6WithError(New("test")())()
}

var pathRootID = path.Root("id")
