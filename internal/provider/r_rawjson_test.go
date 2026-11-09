package provider

// Pass 5.1 Lane R: raw *_json fidelity. Byte-exact outputs must equal the fake Engine's body (served
// with reversed key order, extra inner whitespace and surrounding newlines, so any re-encoding would
// show); re-encoded outputs must be canonical and give the same bytes and content_sha256 on two reads
// whose Engine responses differ only in key order and (for unordered lists) list order.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/plugin"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/volume"
)

// ---------------------------------------------------------------------------
// Helpers

// rReverseNone etc. say which arrays rOddJSON reverses: none, the top-level array, or the arrays that
// are values of the top-level object (e.g. GET /volumes {"Volumes": [...]}, GET /system/df).
const (
	rReverseNone = iota
	rReverseTop
	rReverseDepth1
)

// rOddJSON re-serializes body with object keys in reverse order, spaces around every token, a
// leading "\n  " and a trailing " \n\n"; reverse picks which arrays are reversed as well.
func rOddJSON(t *testing.T, body []byte, reverse int) []byte {
	t.Helper()
	val, err := rDecodeNumber(body)
	if err != nil {
		t.Fatalf("fake body is not JSON: %v: %s", err, body)
	}
	var b bytes.Buffer
	b.WriteString("\n  ")
	rEmitOdd(t, &b, val, reverse, 0)
	b.WriteString(" \n\n")
	return b.Bytes()
}

func rEmitOdd(t *testing.T, b *bytes.Buffer, v any, reverse, depth int) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
		b.WriteString("{ ")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(" ,  ")
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteString(" :  ")
			rEmitOdd(t, b, x[k], reverse, depth+1)
		}
		b.WriteString(" }")
	case []any:
		items := append([]any(nil), x...)
		if (reverse == rReverseTop && depth == 0) || (reverse == rReverseDepth1 && depth == 1) {
			for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
				items[i], items[j] = items[j], items[i]
			}
		}
		b.WriteString("[ ")
		for i, e := range items {
			if i > 0 {
				b.WriteString(" , ")
			}
			rEmitOdd(t, b, e, reverse, depth+1)
		}
		b.WriteString(" ]")
	default:
		vb, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(vb)
	}
}

// rServed records the bodies a wrapped route served.
type rServed struct{ bodies [][]byte }

func (s *rServed) last() []byte {
	if len(s.bodies) == 0 {
		return nil
	}
	return s.bodies[len(s.bodies)-1]
}

// rWrap replaces route key's handler: the original answer is captured and, on calls selected by
// odd(n) (n counts from 0), re-served through rOddJSON. Every served body is recorded.
func rWrap(t *testing.T, f *p3eFake, key string, reverse int, odd func(n int) bool) *rServed {
	t.Helper()
	f.mu.Lock()
	orig, ok := f.routes[key]
	f.mu.Unlock()
	if !ok {
		t.Fatalf("no fake route %q to wrap", key)
	}
	s := &rServed{}
	n := 0
	f.on(key, func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		orig(rec, r)
		body := rec.Body.Bytes()
		if rec.Code == http.StatusOK && odd(n) {
			body = rOddJSON(t, body, reverse)
		}
		n++
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body)
		s.bodies = append(s.bodies, append([]byte(nil), body...))
	})
	return s
}

func rAlways(int) bool      { return true }
func rOddCalls(n int) bool  { return n%2 == 1 }
func rSHA(b []byte) string  { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func rTrim(b []byte) string { return string(bytes.Trim(b, " \t\r\n")) }
func rIsCanonical(s string) bool {
	c, err := rCanonicalJSON(json.RawMessage(s))
	return err == nil && string(c) == s
}

type rGetter interface {
	GetAttribute(ctx context.Context, p path.Path, target interface{}) diag.Diagnostics
}

// rOut returns output out and its hash output (content_sha256 unless hash is given).
func rOut(t *testing.T, st rGetter, out, hash string) (string, string) {
	t.Helper()
	ctx := context.Background()
	var raw, sum types.String
	if d := st.GetAttribute(ctx, path.Root(out), &raw); d.HasError() {
		t.Fatalf("%s: %v", out, d)
	}
	if hash == "" {
		hash = "content_sha256"
	}
	if d := st.GetAttribute(ctx, path.Root(hash), &sum); d.HasError() {
		t.Fatalf("%s: %v", hash, d)
	}
	return raw.ValueString(), sum.ValueString()
}

// rReadDS reads d (schema + config validators, as swarmBReadDS) and fails on errors.
func rReadDS(t *testing.T, d datasource.DataSource, pd *providerData, cfg map[string]tftypes.Value) tfsdk.State {
	t.Helper()
	st, diags := swarmBReadDS(t, d, pd, cfg)
	if diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	for _, w := range diags.Warnings() {
		if strings.Contains(w.Summary(), "Outputs contract") {
			t.Errorf("contract warning: %s", w.Detail())
		}
	}
	return st
}

// rCheckExact asserts that out is the served body without its surrounding whitespace, and that the
// hash is the sha256 of exactly those bytes.
func rCheckExact(t *testing.T, name string, st rGetter, out string, served []byte) {
	t.Helper()
	raw, sum := rOut(t, st, out, "")
	want := rTrim(served)
	if want == "" {
		t.Fatalf("%s: nothing served", name)
	}
	if raw != want {
		t.Errorf("%s.%s is not the Engine body byte for byte:\n got %q\nwant %q", name, out, raw, want)
	}
	if sum != rSHA([]byte(want)) {
		t.Errorf("%s: content_sha256 %s is not sha256 of the body", name, sum)
	}
	if rIsCanonical(raw) {
		t.Errorf("%s: test body is already canonical, the check proves nothing", name)
	}
}

// rCheckStable asserts two reads gave the same canonical bytes and hash although the Engine bodies
// differed.
func rCheckStable(t *testing.T, name string, a, b rGetter, out, hash string, served ...*rServed) {
	t.Helper()
	ra, sa := rOut(t, a, out, hash)
	rb, sb := rOut(t, b, out, hash)
	if ra == "" {
		t.Fatalf("%s.%s is empty", name, out)
	}
	if ra != rb || sa != sb {
		t.Errorf("%s.%s differs between two reads of the same object:\n%s\n%s", name, out, ra, rb)
	}
	if sa != rSHA([]byte(ra)) {
		t.Errorf("%s: hash is not sha256 of %s", name, out)
	}
	if !rIsCanonical(ra) {
		t.Errorf("%s.%s is not canonical JSON: %s", name, out, ra)
	}
	for _, s := range served {
		if len(s.bodies) < 2 || bytes.Equal(s.bodies[0], s.bodies[1]) {
			t.Errorf("%s: the fake did not vary its answer (%d bodies)", name, len(s.bodies))
		}
	}
}

// ---------------------------------------------------------------------------
// The canonical encoder

func TestRCanonicalJSON(t *testing.T) {
	type inner struct {
		Z string `json:"z"`
		A uint64 `json:"a"`
	}
	v := map[string]any{"b": []any{inner{Z: "<&>", A: 18446744073709551615}}, "a": 1e21, "c": 0.1, "d": map[string]int{"y": 1, "x": 2}}
	got, err := rCanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":1e+21,"b":[{"a":18446744073709551615,"z":"<&>"}],"c":0.1,"d":{"x":2,"y":1}}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// A raw body in any key order and spacing canonicalizes to the same bytes; numbers keep their text.
	again, err := rCanonicalJSON(json.RawMessage(" {\"d\": {\"y\":1,\"x\":2}, \"c\":0.1,\"b\":[{\"z\":\"<&>\",\"a\":18446744073709551615}],\"a\":1e+21}\n"))
	if err != nil || string(again) != want {
		t.Errorf("raw: %s (%v)", again, err)
	}
	if b, _ := rCanonicalList(nil); string(b) != "[]" {
		t.Errorf("nil list = %s", b)
	}
	if _, err := rCanonicalJSON(json.RawMessage(`{"a":1} {"b":2}`)); err == nil {
		t.Error("trailing data must fail")
	}
	if rEngineBody([]byte(" \n\t")) != nil || string(rEngineBody([]byte("\n {\"b\" : 1 ,\"a\":2}\n"))) != `{"b" : 1 ,"a":2}` {
		t.Error("rEngineBody must trim only the surrounding whitespace")
	}
	sorted, err := rSortJSONArray([]map[string]any{{"Name": "b"}, {"Id": "a"}, {"Name": "b", "x": 1}, {"Name": "b", "x": 0}}, "Id", "Name")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := rCanonicalJSON(sorted); string(b) != `[{"Id":"a"},{"Name":"b","x":0},{"Name":"b","x":1},{"Name":"b"}]` {
		t.Errorf("sorted = %s", b)
	}
}

// ---------------------------------------------------------------------------
// Byte-exact outputs

func TestRRawByteExactDataSources(t *testing.T) {
	t.Run("docker_container", func(t *testing.T) {
		f, pd := copFake(t)
		s := rWrap(t, f, "GET /containers/web/json", rReverseNone, rAlways)
		st := rReadDS(t, NewContainerDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("web")})
		rCheckExact(t, "data.docker_container", st, "inspect_json", s.last())
	})
	t.Run("docker_container_stats", func(t *testing.T) {
		f, pd := copFake(t)
		f.json("GET /containers/c1/stats", map[string]any{"name": "/web", "id": "c1", "pids_stats": map[string]any{"current": 3}, "memory_stats": map[string]any{"usage": 1024}})
		s := rWrap(t, f, "GET /containers/c1/stats", rReverseNone, rAlways)
		st := rReadDS(t, NewContainerStatsDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("web")})
		rCheckExact(t, "data.docker_container_stats", st, "stats_json", s.last())
	})
	t.Run("docker_image", func(t *testing.T) {
		f, pd := newP3eFake(t)
		f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47"})
		f.json("GET /images/nginx:1.27/json", map[string]any{"Id": "sha256:abc", "RepoTags": []string{"nginx:1.27"}, "Os": "linux", "Architecture": "amd64",
			"Created": "2026-01-02T03:04:05Z", "Config": map[string]any{"Env": []string{"A=1"}, "Labels": map[string]string{"b": "2", "a": "1"}}})
		s := rWrap(t, f, "GET /images/nginx:1.27/json", rReverseNone, rAlways)
		st := rReadDS(t, NewImageDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("nginx:1.27")})
		rCheckExact(t, "data.docker_image", st, "inspect_json", s.last())
	})
	t.Run("docker_network", func(t *testing.T) {
		f, pd := objFake(t)
		insp := network.Inspect{Network: network.Network{ID: "nid", Name: "app", Driver: "bridge", Scope: "local", Labels: map[string]string{"b": "2", "a": "1"}}}
		f.json("GET /networks/app", insp)
		f.json("GET /networks/nid", insp)
		f.json("GET /networks", []network.Summary{{Network: network.Network{ID: "nid", Name: "app"}}})
		a := rWrap(t, f, "GET /networks/app", rReverseNone, rAlways)
		b := rWrap(t, f, "GET /networks/nid", rReverseNone, rAlways)
		st := rReadDS(t, NewNetworkDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("app")})
		served := a.last()
		if served == nil {
			served = b.last()
		}
		rCheckExact(t, "data.docker_network", st, "inspect_json", served)
	})
	t.Run("docker_volume", func(t *testing.T) {
		f, pd := objFake(t)
		f.json("GET /volumes/data", volume.Volume{Name: "data", Driver: "local", Mountpoint: "/var/lib/docker/volumes/data/_data", Labels: map[string]string{"b": "2", "a": "1"}})
		s := rWrap(t, f, "GET /volumes/data", rReverseNone, rAlways)
		st := rReadDS(t, NewVolumeDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("data")})
		rCheckExact(t, "data.docker_volume", st, "inspect_json", s.last())
	})
	t.Run("docker_plugin", func(t *testing.T) {
		f, pd := objFake(t)
		f.json("GET /plugins/pid/json", plugin.Plugin{ID: "pid", Name: "vieux/sshfs:latest", Enabled: true, Config: plugin.Config{Description: "sshfs"}})
		s := rWrap(t, f, "GET /plugins/pid/json", rReverseNone, rAlways)
		st := rReadDS(t, NewPluginDataSource(), pd, map[string]tftypes.Value{"id": p3eStr("pid")})
		rCheckExact(t, "data.docker_plugin", st, "inspect_json", s.last())
	})
	swarmCases := []struct {
		name  string
		ds    func() datasource.DataSource
		route string
		cfg   map[string]tftypes.Value
	}{
		{"docker_node", NewNodeDataSource, "GET /nodes/node1abc", map[string]tftypes.Value{"hostname": p3eStr("mgr1")}},
		{"docker_secret", NewSecretDataSource, "GET /secrets/sec1abc", map[string]tftypes.Value{"name": p3eStr("pw")}},
		{"docker_config", NewConfigDataSource, "GET /configs/cfg1abc", map[string]tftypes.Value{"id": p3eStr("cfg1")}},
		{"docker_service", NewServiceDataSource, "GET /services/svc1abc", map[string]tftypes.Value{"name": p3eStr("web")}},
		{"docker_task", NewTaskDataSource, "GET /tasks/task1abc", map[string]tftypes.Value{"id": p3eStr("task1abc")}},
	}
	for _, c := range swarmCases {
		t.Run(c.name, func(t *testing.T) {
			f, pd := swarmBFakeSwarmObjects(t)
			s := rWrap(t, f, c.route, rReverseNone, rAlways)
			st := rReadDS(t, c.ds(), pd, c.cfg)
			rCheckExact(t, "data."+c.name, st, "inspect_json", s.last())
		})
	}
}

// rResRead runs a resource's Read from a prior state holding vals.
func rResRead(t *testing.T, r fwresource.Resource, pd *providerData, vals map[string]tftypes.Value) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	s := regResSchema(t, r, pd)
	st := tfsdk.State{Schema: s, Raw: p3eObject(s.Type().TerraformType(ctx), vals)}
	out, removed, diags := regRead(t, r, st)
	if diags.HasError() || removed {
		t.Fatalf("read: removed=%v %v", removed, diags)
	}
	return out
}

func TestRRawByteExactResources(t *testing.T) {
	ctx := context.Background()
	t.Run("docker_volume", func(t *testing.T) {
		f, pd := objFake(t)
		f.json("GET /volumes/data", volume.Volume{Name: "data", Driver: "local", Labels: map[string]string{"b": "2", "a": "1"}})
		s := rWrap(t, f, "GET /volumes/data", rReverseNone, rAlways)
		st := rResRead(t, NewVolumeResource(), pd, map[string]tftypes.Value{"id": p3eStr("data"), "name": p3eStr("data")})
		rCheckExact(t, "docker_volume", st, "inspect_json", s.last())
	})
	t.Run("docker_network", func(t *testing.T) {
		f, pd := objFake(t)
		f.json("GET /networks/nid", network.Inspect{Network: network.Network{ID: "nid", Name: "app", Driver: "bridge", Labels: map[string]string{"b": "2", "a": "1"}}})
		s := rWrap(t, f, "GET /networks/nid", rReverseNone, rAlways)
		st := rResRead(t, NewNetworkResource(), pd, map[string]tftypes.Value{"id": p3eStr("nid"), "name": p3eStr("app")})
		rCheckExact(t, "docker_network", st, "inspect_json", s.last())
	})
	t.Run("docker_container", func(t *testing.T) {
		body := []byte("\n {\"Name\" : \"/web\",  \"Id\":\"c1\" , \"Config\":{\"Labels\":{\"z\":\"1\",\"a\":\"2\"}}}\n\n")
		var m containerResourceModel
		if d := ctrFillContract(ctx, nil, &m, body, container.InspectResponse{}); d.HasError() {
			t.Fatal(d)
		}
		if m.InspectJSON.ValueString() != rTrim(body) || m.ContentSHA256.ValueString() != rSHA([]byte(rTrim(body))) {
			t.Errorf("docker_container inspect_json = %q", m.InspectJSON.ValueString())
		}
		// Without a raw body (never from ContainerInspect) the fallback is canonical.
		var fb containerResourceModel
		ctrFillContract(ctx, nil, &fb, nil, container.InspectResponse{ID: "c1", Name: "/web"})
		if !rIsCanonical(fb.InspectJSON.ValueString()) {
			t.Errorf("fallback inspect_json not canonical: %s", fb.InspectJSON.ValueString())
		}
	})
	t.Run("docker_image+docker_tag", func(t *testing.T) {
		f, pd := newP3eFake(t)
		f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47"})
		f.json("GET /images/app:1/json", map[string]any{"Id": "sha256:abc", "RepoTags": []string{"app:1"}, "Config": map[string]any{"Labels": map[string]string{"b": "2", "a": "1"}}})
		s := rWrap(t, f, "GET /images/app:1/json", rReverseNone, rAlways)
		_, raw, err := imgInspectRaw(ctx, pd, "app:1", true)
		if err != nil {
			t.Fatal(err)
		}
		var m imageResourceModel
		if d := imgFillContract(ctx, nil, &m, raw, nil); d.HasError() {
			t.Fatal(d)
		}
		rCheckExact(t, "docker_image", &modelGetter{map[string]string{"inspect_json": m.InspectJSON.ValueString(), "content_sha256": m.InspectJSONSHA256.ValueString()}}, "inspect_json", s.last())
		tm := tagResourceModel{TargetImage: types.StringValue("app:1")}
		if d := tagFillOutputs(ctx, pd, &tm); d.HasError() {
			t.Fatal(d)
		}
		rCheckExact(t, "docker_tag", &modelGetter{map[string]string{"inspect_json": tm.InspectJSON.ValueString(), "content_sha256": tm.ContentSHA256.ValueString()}}, "inspect_json", s.last())
	})
	t.Run("swarm resources", func(t *testing.T) {
		// docker_config / docker_secret / docker_service / docker_node / docker_swarm_node pass
		// *InspectResult.Raw to swarmInspectInput (svcContractInput for the service).
		body := []byte("\n{ \"Spec\" : {\"Name\":\"pw\"} , \"ID\":\"sec1\" }\n")
		for _, in := range []contractFillInput{swarmInspectInput(body, swarm.Secret{}), svcContractInput(body, swarm.Service{})} {
			if got := string(in.Raw["inspect_json"]); got != rTrim(body) {
				t.Errorf("swarm inspect_json = %q", got)
			}
		}
	})
}

// modelGetter adapts plain values to rGetter.
type modelGetter struct{ v map[string]string }

func (g *modelGetter) GetAttribute(_ context.Context, p path.Path, target interface{}) diag.Diagnostics {
	*(target.(*types.String)) = types.StringValue(g.v[p.String()])
	return nil
}

// ---------------------------------------------------------------------------
// Re-encoded outputs: deterministic

type rStableCase struct {
	name   string
	setup  func(t *testing.T) (*p3eFake, *providerData)
	routes map[string]int // route -> reverse mode
	ds     func() datasource.DataSource
	cfg    map[string]tftypes.Value
	out    string
}

func TestRRawReencodedDeterministic(t *testing.T) {
	cases := []rStableCase{
		{"docker_containers", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := copFake(t)
			f.json("GET /containers/json", []container.Summary{{ID: "c2", Names: []string{"/web"}, Labels: map[string]string{"b": "1", "a": "2"}}, {ID: "c1", Names: []string{"/api"}}})
			return f, pd
		}, map[string]int{"GET /containers/json": rReverseTop}, NewContainersDataSource, nil, "containers_json"},
		{"docker_images", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := objFake(t)
			f.json("GET /images/json", []image.Summary{{ID: "sha256:b", RepoTags: []string{"b:1"}, Labels: map[string]string{"y": "1", "x": "2"}}, {ID: "sha256:a"}})
			return f, pd
		}, map[string]int{"GET /images/json": rReverseTop}, NewImagesDataSource, nil, "images_json"},
		{"docker_image_history", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := objFake(t)
			f.json("GET /images/nginx:1.27/history", []image.HistoryResponseItem{{ID: "sha256:top", Created: 2, CreatedBy: "CMD", Tags: []string{"nginx:1.27"}}, {ID: "<missing>", Created: 1, Size: 100}})
			return f, pd
		}, map[string]int{"GET /images/nginx:1.27/history": rReverseNone}, NewImageHistoryDataSource, map[string]tftypes.Value{"name": p3eStr("nginx:1.27")}, "history_json"},
		{"docker_image_search", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := objFake(t)
			f.json("GET /images/search", []registry.SearchResult{{Name: "nginx", IsOfficial: true, StarCount: 100, Description: "<web>"}, {Name: "bitnami/nginx"}})
			return f, pd
		}, map[string]int{"GET /images/search": rReverseNone}, NewImageSearchDataSource, map[string]tftypes.Value{"term": p3eStr("nginx")}, "results_json"},
		{"docker_info", objFake, map[string]int{"GET /info": rReverseNone}, NewInfoDataSource, nil, "info_json"},
		{"docker_version", objFake, map[string]int{"GET /version": rReverseNone}, NewVersionDataSource, nil, "version_json"},
		{"docker_system_df", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := objFake(t)
			f.json("GET /system/df", map[string]any{"LayersSize": 10,
				"Images":     []map[string]any{{"Id": "sha256:b", "Size": 2}, {"Id": "sha256:a", "Size": 1}},
				"Containers": []map[string]any{{"Id": "c2"}, {"Id": "c1"}},
				"Volumes":    []map[string]any{{"Name": "v2", "UsageData": map[string]any{"Size": 1, "RefCount": 1}}, {"Name": "v1", "UsageData": map[string]any{"Size": 2, "RefCount": 0}}},
				"BuildCache": []map[string]any{{"ID": "b2", "Size": 5}, {"ID": "b1", "Size": 6}}})
			return f, pd
		}, map[string]int{"GET /system/df": rReverseDepth1}, NewSystemDfDataSource, map[string]tftypes.Value{"verbose": tftypes.NewValue(tftypes.Bool, true)}, "df_json"},
		{"docker_networks", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := objFake(t)
			f.json("GET /networks", []network.Summary{{Network: network.Network{ID: "n2", Name: "b", Labels: map[string]string{"y": "1", "x": "2"}}}, {Network: network.Network{ID: "n1", Name: "a"}}})
			return f, pd
		}, map[string]int{"GET /networks": rReverseTop}, NewNetworksDataSource, nil, "networks_json"},
		{"docker_volumes", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := objFake(t)
			f.json("GET /volumes", map[string]any{"Volumes": []volume.Volume{{Name: "v2", Driver: "local", Labels: map[string]string{"y": "1", "x": "2"}}, {Name: "v1", Driver: "local"}}, "Warnings": []string{}})
			return f, pd
		}, map[string]int{"GET /volumes": rReverseDepth1}, NewVolumesDataSource, nil, "volumes_json"},
		{"docker_plugins", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := objFake(t)
			f.json("GET /plugins", []plugin.Plugin{{ID: "p2", Name: "b/b:latest", Settings: plugin.Settings{Env: []string{"A=1"}}}, {ID: "p1", Name: "a/a:latest"}})
			return f, pd
		}, map[string]int{"GET /plugins": rReverseTop}, NewPluginsDataSource, nil, "plugins_json"},
		{"docker_container_changes", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := copFake(t)
			f.json("GET /containers/c1/changes", []container.FilesystemChange{{Path: "/tmp/x", Kind: 1}, {Path: "/etc", Kind: 0}, {Path: "/a", Kind: 2}})
			return f, pd
		}, map[string]int{"GET /containers/c1/changes": rReverseTop}, NewContainerChangesDataSource, map[string]tftypes.Value{"container": p3eStr("web")}, "changes_json"},
		{"docker_container_top", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := copFake(t)
			f.json("GET /containers/c1/top", map[string]any{"Titles": []string{"PID", "CMD"}, "Processes": [][]string{{"1", "sleep"}, {"7", "sh"}}})
			return f, pd
		}, map[string]int{"GET /containers/c1/top": rReverseNone}, NewContainerTopDataSource, map[string]tftypes.Value{"container": p3eStr("web")}, "top_json"},
		{"docker_registry_image", func(t *testing.T) (*p3eFake, *providerData) {
			f, pd := objFake(t)
			f.json("GET /distribution/*", regDistributionJSON())
			return f, pd
		}, map[string]int{"GET /distribution/*": rReverseNone}, NewRegistryImageDataSource, map[string]tftypes.Value{"name": p3eStr("nginx:1.27")}, "inspect_json"},
		{"docker_swarm", swarmBFakeSwarmObjects, map[string]int{"GET /swarm": rReverseNone}, NewSwarmDataSource, nil, "inspect_json"},
		{"docker_nodes", swarmBFakeSwarmObjects, map[string]int{"GET /nodes": rReverseTop}, NewNodesDataSource, nil, "nodes_json"},
		{"docker_tasks", swarmBFakeSwarmObjects, map[string]int{"GET /tasks": rReverseTop}, NewTasksDataSource, nil, "tasks_json"},
		{"docker_services", swarmBFakeSwarmObjects, map[string]int{"GET /services": rReverseTop}, NewServicesDataSource, nil, "services_json"},
		{"docker_secrets", swarmBFakeSwarmObjects, map[string]int{"GET /secrets": rReverseTop}, NewSecretsDataSource, nil, "secrets_json"},
		{"docker_configs", swarmBFakeSwarmObjects, map[string]int{"GET /configs": rReverseTop}, NewConfigsDataSource, nil, "configs_json"},
		{"docker_compose_containers", composeCFake, map[string]int{"GET /containers/json": rReverseTop}, NewComposeContainersDataSource, map[string]tftypes.Value{"name": p3eStr("shop")}, "containers_json"},
		{"docker_compose_volumes", composeCFake, map[string]int{"GET /volumes": rReverseDepth1}, NewComposeVolumesDataSource, map[string]tftypes.Value{"name": p3eStr("shop")}, "volumes_json"},
		{"docker_compose_images", composeCFake, map[string]int{"GET /containers/json": rReverseTop, "GET /images/sha256:img1/json": rReverseNone}, NewComposeImagesDataSource, map[string]tftypes.Value{"name": p3eStr("shop")}, "images_json"},
		{"docker_compose_top", composeCFake, map[string]int{"GET /containers/c1/top": rReverseNone}, NewComposeTopDataSource, map[string]tftypes.Value{"name": p3eStr("shop")}, "top_json"},
		{"docker_stack", rStackFake, map[string]int{"GET /services": rReverseTop, "GET /tasks": rReverseTop}, NewStackDataSource, map[string]tftypes.Value{"name": p3eStr("shop")}, "services_json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, pd := c.setup(t)
			var served []*rServed
			for route, rev := range c.routes {
				served = append(served, rWrap(t, f, route, rev, rOddCalls))
			}
			a := rReadDS(t, c.ds(), pd, c.cfg)
			b := rReadDS(t, c.ds(), pd, c.cfg)
			rCheckStable(t, "data."+c.name, a, b, c.out, "", served...)
			if c.name == "docker_swarm" {
				if raw, _ := rOut(t, a, "inspect_json", ""); strings.Contains(raw, "SWMTKN") || strings.Contains(raw, "JoinTokens") {
					t.Errorf("docker_swarm inspect_json (not sensitive) leaks the join tokens: %s", raw)
				}
			}
		})
	}
}

// rStackFake is the swarm fake of TestStackDataSourcesC.
func rStackFake(t *testing.T) (*p3eFake, *providerData) {
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]any{"Version": "28.5.0", "ApiVersion": "1.47"})
	rep := uint64(2)
	f.json("GET /services", []map[string]any{
		{"ID": "s1", "Spec": map[string]any{"Name": "shop_web", "Labels": map[string]string{labelStackNamespace: "shop"},
			"TaskTemplate": map[string]any{"ContainerSpec": map[string]any{"Image": "nginx:1.27"}},
			"Mode":         map[string]any{"Replicated": map[string]any{"Replicas": rep}}},
			"Endpoint": map[string]any{"Ports": []map[string]any{{"Protocol": "tcp", "TargetPort": 80, "PublishedPort": 8080}}}},
		{"ID": "s3", "Spec": map[string]any{"Name": "shop_db", "Labels": map[string]string{labelStackNamespace: "shop"},
			"TaskTemplate": map[string]any{"ContainerSpec": map[string]any{"Image": "postgres:17"}}, "Mode": map[string]any{"Global": map[string]any{}}}},
	})
	f.json("GET /tasks", []map[string]any{
		{"ID": "t1", "ServiceID": "s1", "NodeID": "n1", "DesiredState": "running", "Status": map[string]any{"State": "running"}},
		{"ID": "t2", "ServiceID": "s3", "NodeID": "n2", "DesiredState": "running", "Status": map[string]any{"State": "running"}},
		{"ID": "t3", "ServiceID": "s3", "NodeID": "n1", "DesiredState": "running", "Status": map[string]any{"State": "running"}},
	})
	return f, pd
}

// The container_file stat header and the /auth response are not plain routes: vary them by hand.
func TestRRawReencodedHeaderAndAuth(t *testing.T) {
	t.Run("docker_container_file", func(t *testing.T) {
		f, pd := copFake(t)
		hdrs := []string{
			`{"name":"etc","size":4096,"mode":` + jsonInt(uint32(fs.ModeDir|0o755)) + `,"mtime":"2026-01-01T00:00:00Z","linkTarget":""}`,
			`{ "linkTarget" : "", "mtime":"2026-01-01T00:00:00Z" , "mode":` + jsonInt(uint32(fs.ModeDir|0o755)) + `,"size":4096,"name":"etc"}`,
		}
		n := 0
		f.on("HEAD /containers/c1/archive", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString([]byte(hdrs[n%2])))
			n++
		})
		cfg := map[string]tftypes.Value{"container": p3eStr("web"), "path": p3eStr("/etc")}
		a := rReadDS(t, NewContainerFileDataSource(), pd, cfg)
		b := rReadDS(t, NewContainerFileDataSource(), pd, cfg)
		rCheckStable(t, "data.docker_container_file", a, b, "stat_json", "stat_json_sha256")
	})
	t.Run("docker_registry_login", func(t *testing.T) {
		f, pd := newP3eFake(t)
		pd.RegistryAuth = []registryCredential{{Address: "ghcr.io", Username: "bob", Password: "pw"}}
		f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
		f.json("POST /auth", map[string]string{"Status": "Login Succeeded", "IdentityToken": "idtok"})
		s := rWrap(t, f, "POST /auth", rReverseNone, rOddCalls)
		cfg := map[string]tftypes.Value{"server_address": p3eStr("ghcr.io")}
		a, d := p3eOpenEphemeral(t, NewRegistryLoginEphemeralResource(), pd, cfg)
		if d.HasError() {
			t.Fatal(d)
		}
		b, d := p3eOpenEphemeral(t, NewRegistryLoginEphemeralResource(), pd, cfg)
		if d.HasError() {
			t.Fatal(d)
		}
		rCheckStable(t, "ephemeral.docker_registry_login", &a, &b, "auth_json", "", s)
	})
	t.Run("docker_registry_image resource+data (regDistributionFill)", func(t *testing.T) {
		var di1, di2 registry.DistributionInspect
		_ = json.Unmarshal([]byte(`{"Descriptor":{"mediaType":"m","digest":"sha256:`+strings.Repeat("a", 64)+`","size":1},"Platforms":[{"os":"linux","architecture":"amd64"}]}`), &di1)
		_ = json.Unmarshal([]byte(`{"Platforms":[{"architecture":"amd64","os":"linux"}],"Descriptor":{"size":1,"digest":"sha256:`+strings.Repeat("a", 64)+`","mediaType":"m"}}`), &di2)
		a, err1 := regDistributionFill(di1, "distribution_json")
		b, err2 := regDistributionFill(di2, "distribution_json")
		if err1 != nil || err2 != nil || !bytes.Equal(a.Raw["distribution_json"], b.Raw["distribution_json"]) || !rIsCanonical(string(a.Raw["distribution_json"])) {
			t.Errorf("distribution_json: %s / %s", a.Raw["distribution_json"], b.Raw["distribution_json"])
		}
	})
}

func jsonInt(v uint32) string { b, _ := json.Marshal(v); return string(b) }

// The structured-only fallback of contractCompute is canonical too.
func TestRContractFallbackCanonical(t *testing.T) {
	in := swarmListInput("nodes", []map[string]any{{"b": 1, "a": json.Number("1.50")}})
	if got := string(in.Raw["nodes_json"]); got != `[{"a":1.50,"b":1}]` {
		t.Errorf("nodes_json = %s", got)
	}
	if got := string(swarmListInput("nodes", nil).Raw["nodes_json"]); got != "[]" {
		t.Errorf("nil list = %s", got)
	}
}
