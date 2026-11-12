package provider

// Pass 6c (verification): TestContractSwaggerShapes and TestContractSwaggerRoundTrip
// enumerate their targets from reference/inventory.json, not from
// tools/contractgen/swagger_map.json. In the first cut both tests walked only
// the 43 outputs in that map, so a structured output that was never mapped
// (engine:image-data/config, ImageConfig: exposed_ports list(string) while the
// Engine returns a map) was checked by nothing.
//
// Every structured and result output of every non-skipped inventory item
// (resources, data sources, plural data sources, ephemeral resources, action
// results, Dockerfile blocks, list resources) is a target, checked in one mode:
//
//   - swagger-full:   listed in swagger_map.json; node for node against the
//     whole definition (every swagger property has a field).
//   - swagger-subset: the output's source pointer resolves to a swagger schema
//     of the whole value (definition, response schema); every field whose
//     upstream key is a swagger property has that property's shape.
//   - compose-go:     the compose `project` outputs (TestContractComposeGoShapes
//     walks the full ones against compose-go; the per-key ones are compared
//     with the full project node here).
//   - fragment:       no machine-readable upstream schema of the whole value
//     (Dockerfile reference, buildx/CLI output, derived values, a header or a
//     single property cited); the contract node must equal the inventory's
//     field tree, and the round trip uses a body synthesized from it.
//
// On top of the mode, every inventory field (at any depth) whose pointer is a
// swagger schema must have that schema's shape. A target missing from
// contract_gen.go, or an inventory output that lands in no mode, fails.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

type p6cInvField struct {
	Name     string        `json:"name"`
	Upstream string        `json:"upstream"`
	TFType   string        `json:"tf_type"`
	Pointer  string        `json:"pointer"`
	Fields   []p6cInvField `json:"fields"`
}

type p6cInvOutput struct {
	Name     string         `json:"name"`
	Category string         `json:"category"`
	TFType   string         `json:"tf_type"`
	Source   map[string]any `json:"source"`
	Fields   []p6cInvField  `json:"fields"`
}

type p6cInvItem struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Outputs []p6cInvOutput `json:"outputs"`
	// Status "planned" (Pass 7): an unregistered item a build pass implements;
	// contractgen gives it no contract until its lane registers it.
	Status string `json:"status"`
}

type p6cTarget struct {
	Key    string
	Item   *contractItem
	Out    contractOutput
	Inv    p6cInvOutput
	Mode   string // swagger-full, swagger-subset, compose-go, fragment
	Schema any    // swagger schema of one element (swagger-*)
	Wrap   string // "", "list", "map": how the output wraps Schema
	Why    string // fragment: why there is no whole-value schema
}

var (
	p6cDocOnce sync.Once
	p6cDoc     map[string]any
	p6cDocErr  error
)

func p6cSwaggerDoc(t *testing.T) map[string]any {
	t.Helper()
	p6cDocOnce.Do(func() {
		raw, err := os.ReadFile(filepath.Join("..", "..", "reference", "upstream", "engine-api", "api", "swagger.yaml"))
		if err != nil {
			p6cDocErr = err
			return
		}
		p6cDocErr = yaml.Unmarshal(raw, &p6cDoc)
	})
	if p6cDocErr != nil {
		t.Fatal(p6cDocErr)
	}
	return p6cDoc
}

// p6cPointer resolves a swagger JSON pointer to a schema: an operation gives
// its 200/201 response schema, a response its schema. ok is false for a
// non-swagger pointer, a header, or a pointer that resolves to nothing.
func p6cPointer(t *testing.T, ptr string) (any, bool) {
	if !strings.HasPrefix(ptr, "#/definitions/") && !strings.HasPrefix(ptr, "#/paths/") {
		return nil, false
	}
	var cur any = p6cSwaggerDoc(t)
	for _, seg := range strings.Split(strings.TrimPrefix(ptr, "#/"), "/") {
		seg = strings.NewReplacer("~1", "/", "~0", "~").Replace(seg)
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[seg]; !ok {
			return nil, false
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, false
	}
	if strings.Contains(ptr, "/headers/") || strings.HasSuffix(ptr, "/parameters") {
		return nil, false
	}
	if rs, ok := m["responses"].(map[string]any); ok {
		for _, code := range []string{"200", "201"} {
			if r, ok := rs[code].(map[string]any); ok {
				if s, ok := r["schema"]; ok {
					return s, true
				}
			}
		}
		return nil, false
	}
	if s, ok := m["schema"]; ok && strings.Contains(ptr, "/responses/") {
		return s, true
	}
	if _, isParam := m["in"]; isParam {
		return nil, false
	}
	return m, true
}

func p6cInventoryItems(t *testing.T) []p6cInvItem {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "reference", "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inv struct {
		Items []p6cInvItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	return inv.Items
}

func p6cKindName(k contractNodeKind) string { return p6czKindName(k) }

// p6cTargets classifies every structured / result output of every non-skipped
// inventory item. Anything unclassifiable is a test failure.
func p6cTargets(t *testing.T) []p6cTarget {
	t.Helper()
	sw, m := p6cLoadSwagger(t)
	composeFull := map[string]bool{"compose:name/project": true, "compose:cli-config/project": true}
	var out []p6cTarget
	seenMap := map[string]bool{}
	for _, iv := range p6cInventoryItems(t) {
		if iv.Kind == "skip" || iv.Status == "planned" {
			continue
		}
		for _, o := range iv.Outputs {
			if o.Category != contractStructured && o.Category != contractResult {
				continue
			}
			key := iv.ID + "/" + o.Name
			it, ok := contractLookup(iv.ID)
			if !ok {
				t.Errorf("%s: inventory item has no contract in contract_gen.go: not checked", key)
				continue
			}
			var co *contractOutput
			for i := range it.Outputs {
				if it.Outputs[i].Name == o.Name {
					co = &it.Outputs[i]
				}
			}
			if co == nil {
				t.Errorf("%s: inventory output missing from contract_gen.go: not checked", key)
				continue
			}
			tg := p6cTarget{Key: key, Item: it, Out: *co, Inv: o}
			n := contractOutputNode(it, *co)
			ptr, _ := o.Source["pointer"].(string)
			switch {
			case m.Outputs[key] != "":
				seenMap[key] = true
				def := m.Outputs[key]
				tg.Mode = "swagger-full"
				tg.Schema = map[string]any{"$ref": "#/definitions/" + strings.TrimSuffix(def, "[]")}
				if strings.HasSuffix(def, "[]") {
					tg.Wrap = "list"
				}
			case strings.HasPrefix(iv.ID, "compose:") && o.Name == "project":
				tg.Mode = "compose-go"
				if !composeFull[key] {
					tg.Why = "per-key project output: compared with compose:name/project"
				}
			default:
				s, ok := p6cPointer(t, ptr)
				if ok {
					sm, _, cyc, _ := sw.resolve(s, map[string]bool{})
					sh := p6cFree
					if !cyc {
						sh = p6cShapeOf(sm)
					}
					// The schema must be the value itself: at least one field
					// claims a property by its exact upstream key (an action
					// result that wraps the definition under its own key, or
					// a flattened projection, is provider-shaped).
					el := n
					for el != nil && (el.Kind == cnList || el.Kind == cnSet || el.Kind == cnMap) {
						el = el.Elem
					}
					claims := false
					if props, isObj := sm["properties"].(map[string]any); isObj && el != nil {
						for _, a := range el.Attrs {
							_, hit := props[a.Upstream]
							claims = claims || hit
						}
					} else if !cyc && sh == p6cList {
						im, _, _, _ := sw.resolve(sm["items"], map[string]bool{})
						if props, isObj := im["properties"].(map[string]any); isObj && el != nil {
							for _, a := range el.Attrs {
								_, hit := props[a.Upstream]
								claims = claims || hit
							}
						}
					}
					switch {
					case !claims:
						ok = false
					case n.Kind == cnObject && sh == p6cObject,
						n.Kind == cnList && sh == p6cList:
						tg.Mode, tg.Schema = "swagger-subset", s
					case n.Kind == cnList && sh == p6cObject:
						tg.Mode, tg.Schema, tg.Wrap = "swagger-subset", s, "list"
					case n.Kind == cnMap && sh == p6cObject:
						tg.Mode, tg.Schema, tg.Wrap = "swagger-subset", s, "map"
					default:
						ok = false
					}
				}
				if !ok {
					tg.Mode = "fragment"
					st, _ := o.Source["type"].(string)
					tg.Why = fmt.Sprintf("source %s %s is not a swagger schema of the whole value", st, ptr)
				}
			}
			out = append(out, tg)
		}
	}
	for k := range m.Outputs {
		if !seenMap[k] {
			t.Errorf("swagger_map.json %s is not a structured output of a non-skipped item", k)
		}
	}
	if len(out) < 170 {
		t.Fatalf("only %d structured/result outputs enumerated from inventory.json", len(out))
	}
	return out
}

// p6cSubset checks every field of n whose upstream key is a property of
// schema v against that property's shape (provider-only fields are skipped).
func (s *p6cSwagger) subset(n *contractNode, v any, stack map[string]bool, path string, bad *[]string) {
	m, _, cyc, next := s.resolve(v, stack)
	if cyc || n == nil {
		return
	}
	sh := p6cShapeOf(m)
	if ap, ok := m["additionalProperties"].(map[string]any); ok && len(ap) == 0 && sh == p6cFree {
		if n.Kind != cnMap && n.Kind != cnDynamic && n.Kind != cnJSON {
			*bad = append(*bad, fmt.Sprintf("%s: free-form map, contract has %s", path, p6cKindName(n.Kind)))
		}
		return
	}
	switch sh {
	case p6cFree:
		if typ, _ := m["type"].(string); typ == "object" && (n.Kind == cnString || n.Kind == cnNumber || n.Kind == cnBool) {
			*bad = append(*bad, fmt.Sprintf("%s: swagger free-form object, contract has %s (the value would be JSON-encoded into it)", path, p6cKindName(n.Kind)))
		}
		return
	case p6cObject:
		if n.Kind == cnDynamic || n.Kind == cnJSON {
			*bad = append(*bad, fmt.Sprintf("%s: swagger object, contract has an untyped %s (a JSON string or dynamic)", path, p6cKindName(n.Kind)))
			return
		}
		if n.Kind != cnObject {
			*bad = append(*bad, fmt.Sprintf("%s: swagger object, contract has %s", path, p6cKindName(n.Kind)))
			return
		}
		props := m["properties"].(map[string]any)
		for _, a := range n.Attrs {
			if pv, ok := props[a.Upstream]; ok {
				s.subset(a.Node, pv, next, path+"."+a.Upstream, bad)
			}
		}
	case p6cEmptyObject:
		if n.Kind != cnObject && n.Kind != cnDynamic && n.Kind != cnJSON {
			*bad = append(*bad, fmt.Sprintf("%s: swagger empty object, contract has %s", path, p6cKindName(n.Kind)))
		}
	case p6cMap:
		if n.Kind != cnMap {
			*bad = append(*bad, fmt.Sprintf("%s: swagger map, contract has %s", path, p6cKindName(n.Kind)))
			return
		}
		s.subset(n.Elem, m["additionalProperties"], next, path+"{}", bad)
	case p6cList:
		if n.Kind != cnList && n.Kind != cnSet {
			*bad = append(*bad, fmt.Sprintf("%s: swagger array, contract has %s", path, p6cKindName(n.Kind)))
			return
		}
		s.subset(n.Elem, m["items"], next, path+"[]", bad)
	case p6cString:
		if n.Kind != cnString {
			*bad = append(*bad, fmt.Sprintf("%s: swagger string, contract has %s", path, p6cKindName(n.Kind)))
		}
	case p6cNumber:
		if n.Kind != cnNumber {
			*bad = append(*bad, fmt.Sprintf("%s: swagger number, contract has %s", path, p6cKindName(n.Kind)))
		}
	case p6cBool:
		if n.Kind != cnBool {
			*bad = append(*bad, fmt.Sprintf("%s: swagger boolean, contract has %s", path, p6cKindName(n.Kind)))
		}
	}
}

// p6cFieldPointers walks the inventory field tree with the contract node and
// checks every field whose pointer is a swagger schema (Definition or
// Definition/properties/X) against that schema.
func (s *p6cSwagger) fieldPointers(t *testing.T, n *contractNode, fields []p6cInvField, path string, bad *[]string) {
	for n != nil && (n.Kind == cnList || n.Kind == cnSet || n.Kind == cnMap) {
		n = n.Elem
	}
	if n == nil || n.Kind != cnObject {
		return
	}
	byName := map[string]*contractNode{}
	for _, a := range n.Attrs {
		byName[a.Name] = a.Node
	}
	for _, f := range fields {
		c := byName[f.Name]
		if c == nil {
			continue
		}
		segs := strings.Split(strings.TrimPrefix(f.Pointer, "#/definitions/"), "/")
		// A whole definition, or a property whose key is the field's upstream
		// key; a property cited for a value derived from it (a label, Names[0])
		// is a citation, not a shape.
		cites := len(segs) == 1 || segs[len(segs)-1] == f.Upstream
		if strings.HasPrefix(f.Pointer, "#/definitions/") && cites {
			if sch, ok := p6cPointer(t, f.Pointer); ok {
				// A definition cited for a list/map field describes its elements.
				sm, _, cyc, _ := s.resolve(sch, map[string]bool{})
				target := c
				if !cyc && p6cShapeOf(sm) == p6cObject {
					for target != nil && (target.Kind == cnList || target.Kind == cnSet || target.Kind == cnMap) {
						target = target.Elem
					}
				}
				s.subset(target, sch, map[string]bool{}, path+"."+f.Name+" ("+f.Pointer+")", bad)
			}
		}
		s.fieldPointers(t, c, f.Fields, path+"."+f.Name, bad)
	}
}

// p6cFragmentCheck: the contract node equals the inventory field tree
// (names, kinds, nesting); no untyped node where the inventory has fields.
func p6cFragmentCheck(n *contractNode, tfType string, fields []p6cInvField, path string, bad *[]string) {
	e, err := contractParseType(tfType)
	if err != nil {
		*bad = append(*bad, fmt.Sprintf("%s: tf_type %q: %v", path, tfType, err))
		return
	}
	var walk func(n *contractNode, e *contractTypeExpr, fields []p6cInvField, path string)
	walk = func(n *contractNode, e *contractTypeExpr, fields []p6cInvField, path string) {
		switch e.head {
		case "list", "set", "map":
			want := map[string]contractNodeKind{"list": cnList, "set": cnSet, "map": cnMap}[e.head]
			if n.Kind != want {
				*bad = append(*bad, fmt.Sprintf("%s: inventory %s, contract has %s", path, e.head, p6cKindName(n.Kind)))
				return
			}
			walk(n.Elem, e.elem, fields, path+"[]")
		case "object":
			if len(fields) == 0 && len(e.attrs) == 0 {
				return
			}
			if n.Kind != cnObject {
				*bad = append(*bad, fmt.Sprintf("%s: inventory object, contract has %s", path, p6cKindName(n.Kind)))
				return
			}
			byName := map[string]*contractNode{}
			for _, a := range n.Attrs {
				byName[a.Name] = a.Node
			}
			for _, f := range fields {
				c := byName[f.Name]
				if c == nil {
					*bad = append(*bad, fmt.Sprintf("%s.%s: inventory field missing from contract_gen.go", path, f.Name))
					continue
				}
				p6cFragmentCheck(c, f.TFType, f.Fields, path+"."+f.Name, bad)
			}
			if len(fields) == 0 {
				for _, a := range e.attrs {
					c := byName[a.name]
					if c == nil {
						*bad = append(*bad, fmt.Sprintf("%s.%s: inventory attribute missing from contract_gen.go", path, a.name))
						continue
					}
					walk(c, a.expr, nil, path+"."+a.name)
				}
			}
		case "string", "number", "bool":
			want := map[string]contractNodeKind{"string": cnString, "number": cnNumber, "bool": cnBool}[e.head]
			if n.Kind != want {
				*bad = append(*bad, fmt.Sprintf("%s: inventory %s, contract has %s", path, e.head, p6cKindName(n.Kind)))
			}
		}
	}
	walk(n, e, fields, path)
}

// p6cSynthNode builds a dense upstream-keyed body from a contract node (the
// fragment-mode round trip input).
func p6cSynthNode(n *contractNode, depth int) any {
	if n == nil || depth > 14 {
		return nil
	}
	switch n.Kind {
	case cnString:
		return "volume"
	case cnNumber:
		return json.Number("7")
	case cnBool:
		return true
	case cnDynamic, cnJSON:
		return map[string]any{"FreeKey": map[string]any{"Inner-Key": "v"}}
	case cnList, cnSet:
		if x := p6cSynthNode(n.Elem, depth+1); x != nil {
			return []any{x}
		}
		return nil
	case cnMap:
		if x := p6cSynthNode(n.Elem, depth+1); x != nil {
			return map[string]any{"Max-File.Key": x}
		}
		return nil
	case cnObject:
		out := map[string]any{}
		for _, a := range n.Attrs {
			if x := p6cSynthNode(a.Node, depth+1); x != nil {
				out[a.Upstream] = x
			}
		}
		return out
	}
	return nil
}

// p6cProject keeps the parts of body v the contract node has a field for by
// exact upstream key (the input and expected value of a subset round trip:
// provider-flattened fields such as "Descriptor.size" are not in the body).
func p6cProject(n *contractNode, v any) any {
	if v == nil || n == nil {
		return nil
	}
	switch n.Kind {
	case cnList, cnSet:
		arr, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, 0, len(arr))
		for _, x := range arr {
			out = append(out, p6cProject(n.Elem, x))
		}
		return out
	case cnMap:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := map[string]any{}
		for k, x := range m {
			out[k] = p6cProject(n.Elem, x)
		}
		return out
	case cnObject:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := map[string]any{}
		for _, a := range n.Attrs {
			if x, ok := m[a.Upstream]; ok {
				if p := p6cProject(a.Node, x); p != nil {
					out[a.Upstream] = p
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return v
}

// p6cPrune drops empty objects and nulls so a projected body and a filled
// value compare equal when a provider-only field is null.
func p6cPrune(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if p := p6cPrune(e); p != nil {
				out[k] = p
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			out = append(out, p6cPrune(e))
		}
		return out
	}
	return v
}

// p6cRoundTrip fills body through the contract as output o of it and returns
// the filled value in upstream names, failing on any diagnostic.
func p6cRoundTrip(t *testing.T, tg p6cTarget, body any) any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	in := contractFillInput{Raw: map[string][]byte{}, Structured: map[string]any{}, Result: map[string]any{}}
	rawName := ""
	for _, r := range tg.Item.Outputs {
		if r.Category == contractRaw && r.Serializes == tg.Out.Name && strings.HasSuffix(r.Name, "_json") {
			rawName = r.Name
		}
	}
	switch {
	case tg.Out.Category == contractResult:
		in.Result[tg.Out.Name] = json.RawMessage(raw)
	case rawName != "":
		in.Raw[rawName] = raw
	default:
		in.Structured[tg.Out.Name] = json.RawMessage(raw)
	}
	plain, diags := contractCompute(context.Background(), nil, tg.Item, in, func(x contractOutput) bool { return x.Name == tg.Out.Name })
	if diags.HasError() || diags.WarningsCount() > 0 {
		t.Fatalf("fill: %v", diags)
	}
	n := contractOutputNode(tg.Item, tg.Out)
	v, d := contractToAttr(n, plain[tg.Out.Name])
	if d.HasError() {
		t.Fatalf("to attr: %v", d)
	}
	return p6cPlain(t, n, v)
}

func p6cDecode(t *testing.T, body any) any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func p6cWrapBody(tg p6cTarget, el any) any {
	switch tg.Wrap {
	case "list":
		return []any{el}
	case "map":
		return map[string]any{"Max-File.Key": el}
	}
	return el
}

func p6cSortedTargets(t *testing.T) []p6cTarget {
	tgs := p6cTargets(t)
	sort.Slice(tgs, func(i, j int) bool { return tgs[i].Key < tgs[j].Key })
	return tgs
}

// p6cFirstDiff shows where two canonical JSON strings first differ.
func p6cFirstDiff(g, w string) string {
	i := 0
	for i < len(g) && i < len(w) && g[i] == w[i] {
		i++
	}
	lo := max(0, i-160)
	return fmt.Sprintf("at byte %d\n got: ...%.320s\nwant: ...%.320s", i, g[lo:], w[lo:])
}
