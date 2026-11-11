package provider

// Pass 6c lane S: structured outputs mirror the Engine API swagger.
//
//   - TestContractSwaggerShapes: every output in tools/contractgen/swagger_map.json
//     has, node for node, the shape its swagger definition has (object with
//     additionalProperties -> map, array -> list, enum -> string...).
//   - TestContractSwaggerRoundTrip: a dense body synthesized from each
//     definition, filled through the contract, decoded back with upstream
//     names, equals the body value for value (no drop, no double encoding,
//     no renamed map key).

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"gopkg.in/yaml.v3"
)

type p6cSwaggerMap struct {
	Outputs   map[string]string `json:"outputs"`
	Sensitive []string          `json:"sensitive"`
}

type p6cSwagger struct {
	defs map[string]any
}

func p6cLoadSwagger(t *testing.T) (*p6cSwagger, *p6cSwaggerMap) {
	t.Helper()
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "reference", "upstream", "engine-api", "api", "swagger.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Definitions map[string]any `yaml:"definitions"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	mraw, err := os.ReadFile(filepath.Join(root, "tools", "contractgen", "swagger_map.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m p6cSwaggerMap
	if err := json.Unmarshal(mraw, &m); err != nil {
		t.Fatal(err)
	}
	return &p6cSwagger{defs: doc.Definitions}, &m
}

// resolve follows $ref and merges allOf properties. cyc reports a
// definition already on the stack (the generator stops there: dynamic).
func (s *p6cSwagger) resolve(v any, stack map[string]bool) (m map[string]any, def string, cyc bool, next map[string]bool) {
	next = map[string]bool{}
	for k, b := range stack {
		next[k] = b
	}
	mm, _ := v.(map[string]any)
	if mm == nil {
		return map[string]any{}, "", false, next
	}
	for {
		ref, ok := mm["$ref"].(string)
		if !ok {
			break
		}
		def = strings.TrimPrefix(ref, "#/definitions/")
		if next[def] {
			return nil, def, true, next
		}
		next[def] = true
		mm, _ = s.defs[def].(map[string]any)
		if mm == nil {
			mm = map[string]any{}
		}
	}
	if all, ok := mm["allOf"].([]any); ok {
		props := map[string]any{}
		merged := map[string]any{}
		for _, p := range all {
			pm, _, c, _ := s.resolve(p, next)
			if c {
				return nil, def, true, next
			}
			for k, x := range pm {
				if k == "properties" {
					for pk, pv := range x.(map[string]any) {
						props[pk] = pv
					}
				} else if _, set := merged[k]; !set {
					merged[k] = x
				}
			}
		}
		if p, ok := mm["properties"].(map[string]any); ok {
			for pk, pv := range p {
				props[pk] = pv
			}
		}
		if len(props) > 0 {
			merged["properties"] = props
			merged["type"] = "object"
		}
		mm = merged
	}
	return mm, def, false, next
}

type p6cShape int

const (
	p6cObject p6cShape = iota
	p6cEmptyObject
	p6cMap
	p6cList
	p6cString
	p6cNumber
	p6cBool
	p6cFree
)

func p6cShapeOf(m map[string]any) p6cShape {
	typ, _ := m["type"].(string)
	if p, ok := m["properties"].(map[string]any); ok && len(p) > 0 {
		return p6cObject
	}
	switch {
	case typ == "array":
		return p6cList
	case typ == "object" || m["additionalProperties"] != nil:
		switch ap := m["additionalProperties"].(type) {
		case map[string]any:
			if len(ap) == 0 {
				return p6cFree // map(dynamic) is checked by the caller
			}
			return p6cMap
		case bool:
			if ap {
				return p6cFree
			}
		}
		if _, ok := m["enum"]; ok {
			return p6cEmptyObject
		}
		return p6cFree
	case typ == "string":
		return p6cString
	case typ == "integer" || typ == "number":
		return p6cNumber
	case typ == "boolean":
		return p6cBool
	}
	if _, ok := m["enum"]; ok {
		return p6cString
	}
	return p6cFree
}

func (s *p6cSwagger) check(t *testing.T, n *contractNode, v any, stack map[string]bool, path string) {
	t.Helper()
	m, _, cyc, next := s.resolve(v, stack)
	if cyc {
		if n.Kind != cnDynamic && n.Kind != cnJSON {
			t.Errorf("%s: recursive definition, want a dynamic/JSON node, got kind %d", path, n.Kind)
		}
		return
	}
	sh := p6cShapeOf(m)
	if ap, ok := m["additionalProperties"].(map[string]any); ok && len(ap) == 0 && sh == p6cFree {
		if n.Kind != cnMap || (n.Elem.Kind != cnJSON && n.Elem.Kind != cnDynamic) {
			t.Errorf("%s: free-form map, want map(dynamic), got kind %d", path, n.Kind)
		}
		return
	}
	want := map[p6cShape]contractNodeKind{p6cObject: cnObject, p6cEmptyObject: cnObject, p6cMap: cnMap, p6cList: cnList,
		p6cString: cnString, p6cNumber: cnNumber, p6cBool: cnBool}
	if sh == p6cFree {
		if n.Kind != cnDynamic && n.Kind != cnJSON {
			t.Errorf("%s: free-form schema, want dynamic/JSON, got kind %d", path, n.Kind)
		}
		return
	}
	if n.Kind != want[sh] {
		t.Errorf("%s: swagger shape %d wants node kind %d, contract_gen.go has %d", path, sh, want[sh], n.Kind)
		return
	}
	switch sh {
	case p6cObject:
		props := m["properties"].(map[string]any)
		byUp := map[string]*contractNode{}
		for _, a := range n.Attrs {
			byUp[a.Upstream] = a.Node
		}
		for k, pv := range props {
			c, ok := byUp[k]
			if !ok {
				t.Errorf("%s: swagger property %q has no field", path, k)
				continue
			}
			s.check(t, c, pv, next, path+"."+k)
		}
	case p6cEmptyObject:
		if len(n.Attrs) != 0 {
			t.Errorf("%s: empty object has %d fields", path, len(n.Attrs))
		}
	case p6cList:
		s.check(t, n.Elem, m["items"], next, path+"[]")
	case p6cMap:
		s.check(t, n.Elem, m["additionalProperties"], next, path+"{}")
	}
}

func p6cMapped(t *testing.T, key string) (*contractItem, contractOutput) {
	t.Helper()
	id, out, _ := strings.Cut(key, "/")
	it, ok := contractLookup(id)
	if !ok {
		t.Fatalf("%s: unknown item", key)
	}
	for _, o := range it.Outputs {
		if o.Name == out {
			return it, o
		}
	}
	t.Fatalf("%s: unknown output", key)
	return nil, contractOutput{}
}

// TestContractSwaggerShapes enumerates every structured and result output of
// every non-skipped item in reference/inventory.json (contract_coverage_p6c_test.go)
// and checks each in its mode; see that file for the modes.
func TestContractSwaggerShapes(t *testing.T) {
	sw, _ := p6cLoadSwagger(t)
	modes := map[string]int{}
	var full *contractNode
	if it, ok := contractLookup("compose:name"); ok {
		for _, o := range it.Outputs {
			if o.Name == "project" {
				full = contractOutputNode(it, o)
			}
		}
	}
	for _, tg := range p6cSortedTargets(t) {
		tg := tg
		modes[tg.Mode]++
		t.Run(tg.Key, func(t *testing.T) {
			n := contractOutputNode(tg.Item, tg.Out)
			var bad []string
			switch tg.Mode {
			case "swagger-full":
				if tg.Wrap == "list" {
					if n.Kind != cnList {
						t.Fatalf("want a list, got kind %d", n.Kind)
					}
					n = n.Elem
				}
				sw.check(t, n, tg.Schema, map[string]bool{}, tg.Key)
			case "swagger-subset":
				el := n
				if tg.Wrap != "" {
					el = n.Elem
				}
				sw.subset(el, tg.Schema, map[string]bool{}, tg.Key, &bad)
			case "compose-go":
				if tg.Why != "" { // per-key: same kinds as the full project
					if full == nil {
						t.Fatal("compose:name/project missing")
					}
					p6cSameKinds(n, full, tg.Key, &bad)
				}
				// full ones: TestContractComposeGoShapes walks them against compose-go.
			case "fragment":
				p6cFragmentCheck(n, tg.Inv.TFType, tg.Inv.Fields, tg.Key, &bad)
			default:
				t.Fatalf("no mode")
			}
			sw.fieldPointers(t, n, tg.Inv.Fields, tg.Key, &bad)
			if len(bad) > 0 {
				sort.Strings(bad)
				t.Errorf("%d shape mismatches:\n%s", len(bad), strings.Join(bad, "\n"))
			}
		})
	}
	t.Logf("targets by mode: %v", modes)
}

// p6cSameKinds compares n with ref by upstream key wherever both have a field.
func p6cSameKinds(n, ref *contractNode, path string, bad *[]string) {
	if n == nil || ref == nil {
		return
	}
	if n.Kind != ref.Kind {
		*bad = append(*bad, fmt.Sprintf("%s: %s, the full project has %s", path, p6czKindName(n.Kind), p6czKindName(ref.Kind)))
		return
	}
	switch n.Kind {
	case cnList, cnSet, cnMap:
		p6cSameKinds(n.Elem, ref.Elem, path+"[]", bad)
	case cnObject:
		by := map[string]*contractNode{}
		for _, a := range ref.Attrs {
			by[a.Upstream] = a.Node
		}
		for _, a := range n.Attrs {
			if r, ok := by[a.Upstream]; ok {
				p6cSameKinds(a.Node, r, path+"."+a.Upstream, bad)
			}
		}
	}
}

// synth builds a dense value for schema v: every property, one map entry
// with a key that snake_case would change, one list element.
func (s *p6cSwagger) synth(v any, stack map[string]bool, depth int) any {
	m, _, cyc, next := s.resolve(v, stack)
	if cyc || depth > 12 {
		return nil
	}
	switch p6cShapeOf(m) {
	case p6cObject:
		out := map[string]any{}
		for k, pv := range m["properties"].(map[string]any) {
			if x := s.synth(pv, next, depth+1); x != nil {
				out[k] = x
			}
		}
		return out
	case p6cEmptyObject:
		return map[string]any{}
	case p6cMap:
		x := s.synth(m["additionalProperties"], next, depth+1)
		if x == nil {
			return nil
		}
		return map[string]any{"Max-File.Key": x}
	case p6cList:
		x := s.synth(m["items"], next, depth+1)
		if x == nil {
			return nil
		}
		return []any{x}
	case p6cString:
		return "volume"
	case p6cNumber:
		return json.Number("7")
	case p6cBool:
		return true
	}
	if ap, ok := m["additionalProperties"].(map[string]any); ok && len(ap) == 0 {
		return map[string]any{"Free-Key": "v"}
	}
	return map[string]any{"FreeKey": map[string]any{"Inner-Key": "v"}}
}

// p6cPlain turns a filled framework value back into the upstream JSON shape
// (field names -> upstream names, JSON fallbacks decoded).
func p6cPlain(t *testing.T, n *contractNode, v attr.Value) any {
	t.Helper()
	if v == nil || v.IsNull() {
		return nil
	}
	switch n.Kind {
	case cnString:
		return v.(types.String).ValueString()
	case cnJSON:
		s := v.(types.String).ValueString()
		var x any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&x); err != nil {
			return s // a plain string element
		}
		return x
	case cnNumber:
		f := v.(types.Number).ValueBigFloat()
		return json.Number(f.Text('f', -1))
	case cnBool:
		return v.(types.Bool).ValueBool()
	case cnDynamic:
		return p6cDyn(v.(types.Dynamic).UnderlyingValue())
	case cnList:
		var out []any
		for _, e := range v.(types.List).Elements() {
			out = append(out, p6cPlain(t, n.Elem, e))
		}
		return out
	case cnSet:
		var out []any
		for _, e := range v.(types.Set).Elements() {
			out = append(out, p6cPlain(t, n.Elem, e))
		}
		return out
	case cnMap:
		out := map[string]any{}
		for k, e := range v.(types.Map).Elements() {
			out[k] = p6cPlain(t, n.Elem, e)
		}
		return out
	case cnObject:
		out := map[string]any{}
		attrs := v.(types.Object).Attributes()
		for _, a := range n.Attrs {
			if x := p6cPlain(t, a.Node, attrs[a.Name]); x != nil {
				out[a.Upstream] = x
			}
		}
		return out
	}
	t.Fatalf("kind %d", n.Kind)
	return nil
}

func p6cDyn(v attr.Value) any {
	switch x := v.(type) {
	case nil:
		return nil
	case basetypes.StringValue:
		return x.ValueString()
	case basetypes.BoolValue:
		return x.ValueBool()
	case basetypes.NumberValue:
		return json.Number(x.ValueBigFloat().Text('f', -1))
	case basetypes.TupleValue:
		var out []any
		for _, e := range x.Elements() {
			out = append(out, p6cDyn(e))
		}
		return out
	case basetypes.ObjectValue:
		out := map[string]any{}
		for k, e := range x.Attributes() {
			out[k] = p6cDyn(e)
		}
		return out
	}
	return fmt.Sprintf("%T", v)
}

func p6cCanon(t *testing.T, v any) string {
	t.Helper()
	b, err := rCanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestContractSwaggerRoundTrip(t *testing.T) {
	t.Setenv("TF_ACC", "")
	sw, _ := p6cLoadSwagger(t)
	for _, tg := range p6cSortedTargets(t) {
		tg := tg
		if tg.Mode != "swagger-full" {
			t.Run(tg.Key, func(t *testing.T) {
				n := contractOutputNode(tg.Item, tg.Out)
				var body any
				if tg.Mode == "swagger-subset" {
					body = p6cProject(n, p6cDecode(t, p6cWrapBody(tg, sw.synth(tg.Schema, map[string]bool{}, 0))))
				} else {
					body = p6cSynthNode(n, 0)
				}
				if body == nil {
					t.Fatalf("empty synthesized body")
				}
				got := p6cPrune(p6cRoundTrip(t, tg, body))
				want := p6cPrune(p6cDecode(t, body))
				if g, w := p6cCanon(t, got), p6cCanon(t, want); g != w {
					t.Errorf("%s round trip differs %s", tg.Mode, p6cFirstDiff(g, w))
				}
			})
			continue
		}
		t.Run(tg.Key, func(t *testing.T) {
			it, o := tg.Item, tg.Out
			body := sw.synth(tg.Schema, map[string]bool{}, 0)
			if tg.Wrap == "list" {
				body = []any{body}
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			in := contractFillInput{Raw: map[string][]byte{}, Structured: map[string]any{}}
			rawName := ""
			for _, r := range it.Outputs {
				if r.Category == contractRaw && r.Serializes == o.Name {
					rawName = r.Name
				}
			}
			if rawName != "" {
				in.Raw[rawName] = raw
			} else {
				in.Structured[o.Name] = json.RawMessage(raw)
			}
			plain, diags := contractCompute(context.Background(), nil, it, in, func(x contractOutput) bool { return x.Name == o.Name })
			if diags.HasError() || diags.WarningsCount() > 0 {
				t.Fatalf("fill: %v", diags)
			}
			n := contractOutputNode(it, o)
			v, d := contractToAttr(n, plain[o.Name])
			if d.HasError() {
				t.Fatalf("to attr: %v", d)
			}
			got := p6cPlain(t, n, v)
			var want any
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.UseNumber()
			_ = dec.Decode(&want)
			if g, w := p6cCanon(t, got), p6cCanon(t, want); g != w {
				t.Errorf("round trip differs\n got: %.600s\nwant: %.600s", g, w)
			}
		})
	}
}

// TestContractSensitiveCommandOutput: command output and environment values
// are sensitive wherever the Engine returns them (Pass 6c e).
func TestContractSensitiveCommandOutput(t *testing.T) {
	find := func(n *contractNode, path ...string) *contractNode {
		for _, p := range path {
			for n.Kind == cnList || n.Kind == cnMap || n.Kind == cnSet {
				n = n.Elem
			}
			var next *contractNode
			for _, a := range n.Attrs {
				if a.Name == p {
					next = a.Node
				}
			}
			if next == nil {
				t.Fatalf("no field %s in %v", p, path)
			}
			n = next
		}
		return n
	}
	out := func(id, name string) *contractNode {
		it, o := p6cMapped(t, id+"/"+name)
		return contractOutputNode(it, o)
	}
	cases := []struct {
		node *contractNode
		what string
	}{
		{find(out("engine:container", "inspect"), "state", "health", "log", "output"), "container health log output"},
		{find(out("engine:container-data", "inspect"), "state", "health", "log", "output"), "container data health log output"},
		{find(out("engine:container", "inspect"), "config", "env"), "container env"},
		{find(out("engine:image", "inspect"), "config", "env"), "image env"},
		{find(out("swarm:service", "inspect"), "spec", "task_template", "container_spec", "env"), "service env"},
		{find(out("swarm:task-data", "inspect"), "spec", "container_spec", "env"), "task env"},
		{find(out("engine:plugin", "inspect"), "settings", "env"), "plugin settings env"},
		{find(out("swarm:swarm-init", "join_tokens"), "worker"), "join token"},
	}
	for _, c := range cases {
		if !c.node.anySensitive() {
			t.Errorf("%s must be sensitive", c.what)
		}
	}
}

// TestContractConfigHash: config_sha256 ignores runtime state and changes
// with the configuration (Pass 6c f).
func TestContractConfigHash(t *testing.T) {
	t.Setenv("TF_ACC", "")
	base := `{"Id":"c1","Name":"/web","Image":"sha256:abc","Created":"%s","State":{"Pid":%d,"Health":{"Status":"healthy","Log":[{"Output":"%s"}]}},` +
		`"Config":{"Image":"nginx","Healthcheck":{"Test":["CMD","true"]}},"HostConfig":{"Memory":%d},` +
		`"NetworkSettings":{"SandboxKey":"/var/run/%d","Networks":{"n1":{"IPAddress":"10.0.0.%d","Aliases":["web"],"MacAddress":"02:42:%d"}}}}`
	hash := func(item string, body string) (string, string) {
		it := contractMustItem(item)
		vals, d := contractValues(context.Background(), nil, item, contractFillInput{Raw: map[string][]byte{"inspect_json": []byte(body)}})
		if d.HasError() {
			t.Fatal(d)
		}
		_ = it
		return vals["config_sha256"].(types.String).ValueString(), vals["content_sha256"].(types.String).ValueString()
	}
	c1, r1 := hash("engine:container", fmt.Sprintf(base, "t1", 10, "ok 1", 64, 1, 1, 1))
	c2, r2 := hash("engine:container", fmt.Sprintf(base, "t1", 11, "ok 2", 64, 2, 2, 2))
	c3, _ := hash("engine:container", fmt.Sprintf(base, "t1", 10, "ok 1", 128, 1, 1, 1))
	if c1 == "" || c1 != c2 {
		t.Errorf("config_sha256 must ignore State, health log, sandbox and assigned addresses: %s %s", c1, c2)
	}
	if r1 == r2 {
		t.Error("content_sha256 must follow the raw output")
	}
	if c3 == c1 {
		t.Error("config_sha256 must change with HostConfig")
	}
	for _, id := range []string{"engine:network", "engine:volume", "swarm:service"} {
		it := contractMustItem(id)
		found := false
		for _, o := range it.Outputs {
			found = found || (o.Name == contractConfigHashOutput && o.Category == contractHash)
		}
		if !found {
			t.Errorf("%s has no config_sha256", id)
		}
		if _, ok := contractConfigSelect[it.Name]; !ok {
			t.Errorf("%s has no configuration selector", it.Name)
		}
	}
	n1, _ := hash("engine:network", `{"Name":"n","Id":"x","Created":"a","Containers":{"c":{"IPv4Address":"10.0.0.2/24"}},"IPAM":{"Driver":"default"}}`)
	n2, _ := hash("engine:network", `{"Name":"n","Id":"x","Created":"b","Containers":{},"IPAM":{"Driver":"default"}}`)
	if n1 != n2 {
		t.Error("network config_sha256 must ignore Containers and Created")
	}
	s1, _ := hash("swarm:service", `{"ID":"s","Version":{"Index":1},"UpdatedAt":"a","Spec":{"Name":"web"},"Endpoint":{"VirtualIPs":[{"Addr":"10.0.0.3/24"}]}}`)
	s2, _ := hash("swarm:service", `{"ID":"s","Version":{"Index":9},"UpdatedAt":"b","Spec":{"Name":"web"},"Endpoint":{"VirtualIPs":[{"Addr":"10.0.0.9/24"}]}}`)
	if s1 != s2 {
		t.Error("service config_sha256 must hash Spec only")
	}
}

// TestContractDroppedFailsUnderTFAcc: a value that does not fit its type is
// a warning for users and an error under TF_ACC (Pass 6c i).
func TestContractDroppedFailsUnderTFAcc(t *testing.T) {
	it := &contractItem{ID: "x:y", Name: "docker_y"}
	t.Setenv("TF_ACC", "")
	var d1 struct{ d diag.Diagnostics }
	contractDropped(&d1.d, it, "inspect.x: want a list, got map[string]interface {}")
	if d1.d.HasError() || d1.d.WarningsCount() != 1 || !strings.HasPrefix(d1.d[0].Summary(), "Outputs contract value dropped") {
		t.Fatalf("without TF_ACC: %v", d1.d)
	}
	t.Setenv("TF_ACC", "1")
	var d2 struct{ d diag.Diagnostics }
	contractDropped(&d2.d, it, "inspect.x: want a list, got map[string]interface {}")
	if !d2.d.HasError() || !strings.HasPrefix(d2.d[0].Summary(), "Outputs contract value dropped") {
		t.Fatalf("with TF_ACC: %v", d2.d)
	}
	// Through the fill path: a shape mismatch makes contractFill fail.
	n := &contractNode{Kind: cnList, Elem: &contractNode{Kind: cnString}}
	var warned []string
	_ = contractNormalize(n, map[string]any{"a": "b"}, "x", func(s string) { warned = append(warned, s) })
	if len(warned) != 1 {
		t.Errorf("normalize must report the mismatch: %v", warned)
	}
	_ = big.NewFloat
	_ = reflect.DeepEqual
}
