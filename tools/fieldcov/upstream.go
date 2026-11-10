package main

// Upstream field enumeration: every writable leaf field of the three
// upstream sources the provider implements.
//
//   - engine: reference/upstream/engine-api/api/swagger.yaml (Engine API
//     v1.56). For every write operation (not GET/HEAD/DELETE) that
//     reference/inventory.json maps to a resource, action or ephemeral
//     resource: the leaves of the body schema (readOnly properties skipped)
//     and the query parameters. Header and path parameters are not fields.
//   - compose: reference/upstream/compose-spec/schema/compose-spec.json, the
//     properties under the service, network, volume, config, secret, model
//     and include definitions, recursively.
//   - bake: reference/upstream/buildx/docs/bake-reference.md, the target
//     attribute table.
//
// Pointers (the keys of reference/_gen/fieldmap.json):
//
//	engine:<Root>/<Prop>/[]/<Prop>   body field; Root is the body's $ref
//	                                 definition, else the operationId
//	engine:<OperationId>?<param>     query parameter
//	compose:<definition>/<prop>/...  compose-spec property ([] = array item)
//	bake:target/<attribute>          bake target attribute

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// field is one writable upstream leaf.
type field struct {
	Pointer string `json:"pointer"`
	Source  string `json:"source"` // engine | compose | bake
	// Ops lists the operationIds whose body or query carries the field
	// (engine only).
	Ops []string `json:"ops,omitempty"`
	// Segs is the property path below the root ("[]" for array items).
	Segs []string `json:"-"`
	// Context is the definition (or inline object property) that declares
	// the leaf: used to match Go struct types.
	Context string `json:"context,omitempty"`
	// Query is set for a query parameter.
	Query      bool   `json:"query,omitempty"`
	Type       string `json:"type,omitempty"`
	Deprecated bool   `json:"deprecated,omitempty"`
	Doc        string `json:"doc,omitempty"`
}

// ---------------------------------------------------------------------------
// Engine API (swagger 2.0)

type swagger struct {
	Paths       map[string]map[string]any `yaml:"paths"`
	Definitions map[string]any            `yaml:"definitions"`
}

func loadSwagger(path string) (*swagger, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s swagger
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// swaggerOp is one operation of the spec.
type swaggerOp struct {
	ID, Method, Path string
	Params           []map[string]any
}

func (s *swagger) operations() map[string]swaggerOp {
	out := map[string]swaggerOp{}
	for p, methods := range s.Paths {
		for m, raw := range methods {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := op["operationId"].(string)
			if id == "" {
				continue
			}
			o := swaggerOp{ID: id, Method: strings.ToUpper(m), Path: p}
			if ps, ok := op["parameters"].([]any); ok {
				for _, x := range ps {
					if pm, ok := x.(map[string]any); ok {
						o.Params = append(o.Params, pm)
					}
				}
			}
			out[id] = o
		}
	}
	return out
}

func refName(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	r, _ := m["$ref"].(string)
	return strings.TrimPrefix(r, "#/definitions/")
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}

func isDeprecated(m map[string]any) bool {
	if d, _ := m["x-deprecated"].(bool); d {
		return true
	}
	if d, _ := m["deprecated"].(bool); d {
		return true
	}
	desc, _ := m["description"].(string)
	l := strings.ToLower(desc)
	return strings.Contains(l, "deprecated") && (strings.HasPrefix(strings.TrimSpace(l), "deprecated") ||
		strings.Contains(l, "this field is deprecated") || strings.Contains(l, "is deprecated"))
}

// engineWalker collects the writable leaves below one body schema.
type engineWalker struct {
	s     *swagger
	root  string
	out   []field
	stack []string
}

// props merges the properties of a schema node (resolving $ref and allOf),
// returning property name -> (schema, declaring context).
func (w *engineWalker) props(node map[string]any, ctx string) (map[string]map[string]any, map[string]string) {
	props := map[string]map[string]any{}
	decl := map[string]string{}
	var visit func(n map[string]any, ctx string, depth int)
	visit = func(n map[string]any, ctx string, depth int) {
		if depth > 8 {
			return
		}
		if r := refName(n); r != "" {
			d, _ := w.s.Definitions[r].(map[string]any)
			if d != nil {
				visit(d, r, depth+1)
			}
			return
		}
		if all, ok := n["allOf"].([]any); ok {
			for _, x := range all {
				if xm, ok := x.(map[string]any); ok {
					visit(xm, ctx, depth+1)
				}
			}
		}
		if ps, ok := n["properties"].(map[string]any); ok {
			for k, v := range ps {
				if vm, ok := v.(map[string]any); ok {
					props[k] = vm
					decl[k] = ctx
				}
			}
		}
	}
	visit(node, ctx, 0)
	return props, decl
}

func (w *engineWalker) resolve(n map[string]any) (map[string]any, string) {
	for i := 0; i < 8; i++ {
		r := refName(n)
		if r == "" {
			return n, ""
		}
		d, _ := w.s.Definitions[r].(map[string]any)
		if d == nil {
			return n, r
		}
		if refName(d) == "" {
			return d, r
		}
		n = d
	}
	return n, ""
}

func hasProps(w *engineWalker, n map[string]any) bool {
	p, _ := w.props(n, "")
	return len(p) > 0
}

func schemaType(n map[string]any) string {
	t, _ := n["type"].(string)
	if t == "" {
		if _, ok := n["additionalProperties"]; ok {
			return "object"
		}
	}
	return t
}

func (w *engineWalker) walk(node map[string]any, ctx string, segs []string) {
	props, decl := w.props(node, ctx)
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		raw := props[k]
		if ro, _ := raw["readOnly"].(bool); ro {
			continue
		}
		res, def := w.resolve(raw)
		if ro, _ := res["readOnly"].(bool); ro && def != "" {
			continue
		}
		seg := append(append([]string{}, segs...), k)
		w.leafOrRecurse(raw, res, def, k, decl[k], seg)
	}
}

func (w *engineWalker) leafOrRecurse(raw, res map[string]any, def, name, declCtx string, seg []string) {
	inStack := func(d string) bool {
		for _, x := range w.stack {
			if x == d {
				return true
			}
		}
		return false
	}
	// Object with properties: recurse.
	if hasProps(w, res) && (def == "" || !inStack(def)) && len(seg) < 12 {
		ctx := def
		if ctx == "" {
			ctx = declCtx + "." + name
		}
		if def != "" {
			w.stack = append(w.stack, def)
		}
		w.walk(res, ctx, seg)
		if def != "" {
			w.stack = w.stack[:len(w.stack)-1]
		}
		return
	}
	// Array of objects with properties: recurse into the item.
	if schemaType(res) == "array" {
		if it, ok := res["items"].(map[string]any); ok {
			ires, idef := w.resolve(it)
			if hasProps(w, ires) && (idef == "" || !inStack(idef)) && len(seg) < 12 {
				ctx := idef
				if ctx == "" {
					ctx = declCtx + "." + name
				}
				if idef != "" {
					w.stack = append(w.stack, idef)
				}
				w.walk(ires, ctx, append(append([]string{}, seg...), "[]"))
				if idef != "" {
					w.stack = w.stack[:len(w.stack)-1]
				}
				return
			}
		}
	}
	// Map of objects with properties (EndpointsConfig: name -> EndpointSettings):
	// recurse into the value schema under a "{}" segment.
	if ap, ok := res["additionalProperties"].(map[string]any); ok && !hasProps(w, res) {
		ares, adef := w.resolve(ap)
		if hasProps(w, ares) && (adef == "" || !inStack(adef)) && len(seg) < 12 {
			ctx := adef
			if ctx == "" {
				ctx = declCtx + "." + name
			}
			if adef != "" {
				w.stack = append(w.stack, adef)
			}
			w.walk(ares, ctx, append(append([]string{}, seg...), "{}"))
			if adef != "" {
				w.stack = w.stack[:len(w.stack)-1]
			}
			return
		}
	}
	desc, _ := raw["description"].(string)
	if desc == "" {
		desc, _ = res["description"].(string)
	}
	w.out = append(w.out, field{
		Pointer:    "engine:" + w.root + "/" + strings.Join(seg, "/"),
		Source:     "engine",
		Segs:       seg,
		Context:    declCtx,
		Type:       schemaType(res),
		Deprecated: isDeprecated(raw) || isDeprecated(res),
		Doc:        firstSentence(desc),
	})
}

// engineFields enumerates the body and query leaves of the given operations.
func engineFields(s *swagger, ops []string) ([]field, error) {
	all := s.operations()
	byPtr := map[string]*field{}
	var order []string
	add := func(f field, op string) {
		if ex, ok := byPtr[f.Pointer]; ok {
			for _, o := range ex.Ops {
				if o == op {
					return
				}
			}
			ex.Ops = append(ex.Ops, op)
			return
		}
		f.Ops = []string{op}
		byPtr[f.Pointer] = &f
		order = append(order, f.Pointer)
	}
	for _, id := range ops {
		op, ok := all[id]
		if !ok {
			return nil, fmt.Errorf("operation %s is not in the swagger spec", id)
		}
		for _, p := range op.Params {
			in, _ := p["in"].(string)
			name, _ := p["name"].(string)
			switch in {
			case "query":
				desc, _ := p["description"].(string)
				t, _ := p["type"].(string)
				add(field{Pointer: "engine:" + id + "?" + name, Source: "engine", Segs: []string{name}, Context: id,
					Query: true, Type: t, Deprecated: isDeprecated(p), Doc: firstSentence(desc)}, id)
			case "body":
				sch, _ := p["schema"].(map[string]any)
				if sch == nil {
					continue
				}
				root := refName(sch)
				if root == "" {
					root = id
				}
				w := &engineWalker{s: s, root: root}
				res, def := w.resolve(sch)
				if !hasProps(w, res) {
					// A non-object body (a tar stream, a string) is one field.
					t := schemaType(res)
					if t == "" {
						t = "binary"
					}
					desc, _ := p["description"].(string)
					add(field{Pointer: "engine:" + id + "#body", Source: "engine", Segs: []string{name}, Context: id,
						Type: t, Doc: firstSentence(desc)}, id)
					continue
				}
				if def != "" {
					w.stack = append(w.stack, def)
				}
				ctx := def
				if ctx == "" {
					ctx = id
				}
				w.walk(res, ctx, nil)
				for _, f := range w.out {
					add(f, id)
				}
			}
		}
	}
	out := make([]field, 0, len(order))
	for _, p := range order {
		out = append(out, *byPtr[p])
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Compose specification (JSON Schema)

// composeDefinitions are the top-level definitions whose properties count.
var composeDefinitions = []string{"service", "network", "volume", "config", "secret", "model", "include"}

type composeWalker struct {
	defs  map[string]any
	out   []field
	stack []string
}

func (w *composeWalker) resolve(n map[string]any) (map[string]any, string) {
	for i := 0; i < 8; i++ {
		r, _ := n["$ref"].(string)
		if r == "" {
			return n, ""
		}
		name := strings.TrimPrefix(strings.TrimPrefix(r, "#/$defs/"), "#/definitions/")
		d, _ := w.defs[name].(map[string]any)
		if d == nil {
			return n, name
		}
		if _, ok := d["$ref"]; !ok {
			return w.flatten(d), name
		}
		n = d
	}
	return n, ""
}

// flatten merges the properties of an allOf composition into one object
// node (service = container_spec + workload_spec + its own properties).
func (w *composeWalker) flatten(n map[string]any) map[string]any {
	all, ok := n["allOf"].([]any)
	if !ok {
		return n
	}
	props := map[string]any{}
	for _, a := range all {
		am, _ := a.(map[string]any)
		if am == nil {
			continue
		}
		res, _ := w.resolve(am)
		if ps, ok := res["properties"].(map[string]any); ok {
			for k, v := range ps {
				props[k] = v
			}
		}
	}
	if ps, ok := n["properties"].(map[string]any); ok {
		for k, v := range ps {
			props[k] = v
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	return out
}

// objectBranch returns the branch of n that is an object with properties
// (n itself, or one of its oneOf/anyOf alternatives), or nil.
func (w *composeWalker) objectBranch(n map[string]any) (map[string]any, string) {
	res, def := w.resolve(n)
	res = w.flatten(res)
	if _, ok := res["properties"].(map[string]any); ok {
		return res, def
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if alts, ok := res[key].([]any); ok {
			for _, a := range alts {
				if am, ok := a.(map[string]any); ok {
					if b, d := w.objectBranch(am); b != nil {
						if d == "" {
							d = def
						}
						return b, d
					}
				}
			}
		}
	}
	return nil, ""
}

// itemsBranch returns the object branch of an array's items (n itself or an
// alternative of it that is an array).
func (w *composeWalker) itemsBranch(n map[string]any) (map[string]any, string) {
	res, _ := w.resolve(n)
	cands := []map[string]any{res}
	for _, key := range []string{"oneOf", "anyOf"} {
		if alts, ok := res[key].([]any); ok {
			for _, a := range alts {
				if am, ok := a.(map[string]any); ok {
					ar, _ := w.resolve(am)
					cands = append(cands, ar)
				}
			}
		}
	}
	for _, c := range cands {
		if t, _ := c["type"].(string); t == "array" {
			if it, ok := c["items"].(map[string]any); ok {
				if b, d := w.objectBranch(it); b != nil {
					return b, d
				}
			}
		}
	}
	return nil, ""
}

// mapBranch returns the object branch of a map's values (n itself or an
// alternative of it that is an object keyed by patternProperties or
// additionalProperties): service networks are name -> {aliases, gw_priority,
// interface_name, ...}.
func (w *composeWalker) mapBranch(n map[string]any) (map[string]any, string) {
	res, _ := w.resolve(n)
	cands := []map[string]any{res}
	for _, key := range []string{"oneOf", "anyOf"} {
		if alts, ok := res[key].([]any); ok {
			for _, a := range alts {
				if am, ok := a.(map[string]any); ok {
					ar, _ := w.resolve(am)
					cands = append(cands, ar)
				}
			}
		}
	}
	for _, c := range cands {
		if _, has := c["properties"]; has {
			continue
		}
		var vals []map[string]any
		if pp, ok := c["patternProperties"].(map[string]any); ok {
			for k, v := range pp {
				if strings.HasPrefix(k, "^x-") {
					continue
				}
				if vm, ok := v.(map[string]any); ok {
					vals = append(vals, vm)
				}
			}
		}
		if ap, ok := c["additionalProperties"].(map[string]any); ok {
			vals = append(vals, ap)
		}
		for _, v := range vals {
			if b, d := w.objectBranch(v); b != nil {
				return b, d
			}
		}
	}
	return nil, ""
}

func (w *composeWalker) walk(def string, node map[string]any, segs []string) {
	ps, _ := node["properties"].(map[string]any)
	names := make([]string, 0, len(ps))
	for k := range ps {
		names = append(names, k)
	}
	sort.Strings(names)
	inStack := func(d string) bool {
		for _, x := range w.stack {
			if x == d {
				return true
			}
		}
		return false
	}
	for _, k := range names {
		pm, _ := ps[k].(map[string]any)
		if pm == nil {
			continue
		}
		seg := append(append([]string{}, segs...), k)
		if b, d := w.objectBranch(pm); b != nil && (d == "" || (!inStack(d) && !isComposeTop(d))) && len(seg) < 10 {
			if d != "" {
				w.stack = append(w.stack, d)
			}
			w.walk(def, b, seg)
			if d != "" {
				w.stack = w.stack[:len(w.stack)-1]
			}
			continue
		}
		if b, d := w.itemsBranch(pm); b != nil && (d == "" || (!inStack(d) && !isComposeTop(d))) && len(seg) < 10 {
			if d != "" {
				w.stack = append(w.stack, d)
			}
			w.walk(def, b, append(seg, "[]"))
			if d != "" {
				w.stack = w.stack[:len(w.stack)-1]
			}
			continue
		}
		if b, d := w.mapBranch(pm); b != nil && (d == "" || (!inStack(d) && !isComposeTop(d))) && len(seg) < 10 {
			if d != "" {
				w.stack = append(w.stack, d)
			}
			w.walk(def, b, append(seg, "{}"))
			if d != "" {
				w.stack = w.stack[:len(w.stack)-1]
			}
			continue
		}
		res, _ := w.resolve(pm)
		desc, _ := pm["description"].(string)
		if desc == "" {
			desc, _ = res["description"].(string)
		}
		t, _ := res["type"].(string)
		w.out = append(w.out, field{
			Pointer:    "compose:" + def + "/" + strings.Join(seg, "/"),
			Source:     "compose",
			Segs:       seg,
			Context:    def,
			Type:       t,
			Deprecated: isDeprecated(pm),
			Doc:        firstSentence(desc),
		})
	}
}

func isComposeTop(d string) bool {
	for _, x := range composeDefinitions {
		if x == d {
			return true
		}
	}
	return false
}

func composeFields(path string) ([]field, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Definitions map[string]any `json:"definitions"`
		Defs        map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Definitions == nil {
		doc.Definitions = doc.Defs
	}
	w := &composeWalker{defs: doc.Definitions}
	for _, d := range composeDefinitions {
		n, _ := doc.Definitions[d].(map[string]any)
		if n == nil {
			return nil, fmt.Errorf("%s: no definition %q", path, d)
		}
		res, _ := w.resolve(n)
		if b, _ := w.objectBranch(res); b != nil {
			res = b
		}
		w.stack = []string{d}
		w.walk(d, res, nil)
	}
	return w.out, nil
}

// ---------------------------------------------------------------------------
// Bake

var bakeRow = regexp.MustCompile("^\\| \\[`([a-z0-9-]+)`\\]\\(#target([a-z0-9-]+)\\)\\s*\\|\\s*([A-Za-z]+)\\s*\\|\\s*(.*?)\\s*\\|\\s*$")

func bakeFields(path string) ([]field, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []field
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		m := bakeRow.FindStringSubmatch(sc.Text())
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, field{Pointer: "bake:target/" + m[1], Source: "bake", Segs: []string{m[1]}, Context: "target",
			Type: strings.ToLower(m[3]), Doc: firstSentence(m[4])})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no target attribute table found", path)
	}
	return out, nil
}
