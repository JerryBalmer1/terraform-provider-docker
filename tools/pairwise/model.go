package main

// The model file, reference/_gen/pairwise/<type>.json:
//
//	{
//	  "type": "docker_container",
//	  "about": "free text for reviewers",
//	  "seed": 1,
//	  "inspect": {"item": "engine:container", "endpoint": "/containers/{id}/json", "id_attr": "id"},
//	  "prelude": "HCL placed before the resource (supporting resources); ${local.prefix} is the config's name prefix",
//	  "fixed": {"image": {"$hcl": "\"${var.pw_registry}busybox:1.36\""}, "command": ["sleep", "3600"]},
//	  "attributes": {
//	    "memory": {
//	      "inspect": "host_config.memory",      // snake_case path in the type's structured `inspect` output
//	      "match": "equal",                     // equal | subset | none
//	      "values": [null, 67108864, {"$set": "10s", "$expect": 10000000000, "$label": "10s"}]
//	    }
//	  },
//	  "constraints": [
//	    {"when": {"network_mode": ["host"]}, "forbid": {"ports": ["published"]},
//	     "reason": "...", "daemon_error": "exact text the Engine returned"}
//	  ]
//	}
//
// Values: a plain JSON value is set as is and expected as is in the inspect
// field. An object whose keys all start with "$" is a value spec: $set (what
// the configuration sets), $expect (what the inspect field must hold, in the
// Engine's JSON shape; omitted = $set), $label (the name constraints and the
// reports use), $hcl (a raw HCL expression instead of $set). null means the
// attribute is not set; nothing is expected unless $expect is given.
// Attribute paths may go through single blocks or nested attributes and the
// first element of list blocks (healthcheck.interval, resources.limits.memory_bytes).
//
// Constraints name values by label or by JSON value ($set). when: every
// listed attribute holds one of its values. forbid: then none of these
// attributes may hold one of the listed values. requires: then each listed
// attribute must hold one of the listed values. A constraint added because
// the Engine rejected a combination carries the daemon's exact error text.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// hclExpr is a raw HCL expression ({"$hcl": "..."}).
type hclExpr string

type model struct {
	Type        string              `json:"type"`
	About       string              `json:"about,omitempty"`
	Seed        uint64              `json:"seed"`
	Inspect     inspectSpec         `json:"inspect"`
	Prelude     string              `json:"prelude,omitempty"`
	Fixed       map[string]any      `json:"fixed,omitempty"`
	Attributes  map[string]attrSpec `json:"attributes"`
	Constraints []constraintSpec    `json:"constraints,omitempty"`
	Notes       map[string]string   `json:"notes,omitempty"`
}

type inspectSpec struct {
	Item     string `json:"item"`     // inventory item id, e.g. engine:container
	Output   string `json:"output"`   // structured output name, default "inspect"
	Endpoint string `json:"endpoint"` // Engine API path with {id}
	IDAttr   string `json:"id_attr"`  // state attribute that fills {id}
}

type attrSpec struct {
	Inspect         string `json:"inspect,omitempty"`
	Match           string `json:"match,omitempty"`
	NoInspectReason string `json:"no_inspect_reason,omitempty"`
	Note            string `json:"note,omitempty"`
	Values          []any  `json:"values"`
}

type constraintSpec struct {
	When        map[string][]any `json:"when"`
	Forbid      map[string][]any `json:"forbid,omitempty"`
	Requires    map[string][]any `json:"requires,omitempty"`
	Reason      string           `json:"reason"`
	DaemonError string           `json:"daemon_error,omitempty"`
}

func loadModel(path string) (*model, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseModel(b, path)
}

func parseModel(b []byte, name string) (*model, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m model
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if m.Type == "" {
		return nil, fmt.Errorf("%s: no type", name)
	}
	if len(m.Attributes) == 0 {
		return nil, fmt.Errorf("%s: no attributes", name)
	}
	return &m, nil
}

// value is one compiled candidate value of a factor.
type value struct {
	Label     string
	Set       any // decoded JSON, hclExpr, or nil (unset)
	Expect    any // nil = nothing to check
	HasExpect bool
	Key       string // canonical JSON of Set, for constraint matching
}

// factor is one attribute of the covering array.
type factor struct {
	Path     string
	Chain    []*schemaNode
	Inspect  string   // snake path
	Upstream []string // resolved path segments ("?name" = matched by normalized name at compare time)
	Match    string
	Values   []value
}

// constraint is compiled to "forbidden" clauses: a clause is violated when
// every listed factor holds one of its listed value indexes.
type clause struct {
	Lits   map[int]map[int]bool // factor -> allowed-to-trigger value indexes
	Source int                  // index into model constraints
}

type compiledModel struct {
	Model       *model
	Schema      *resourceSchema
	Factors     []*factor
	Constraints []constraintSpec
	Clauses     []clause
	Fixed       map[string]any
	Upstream    string // structured output description, for reports
}

func canonical(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// decodeValue turns a model value into a value.
func decodeValue(raw any) (value, error) {
	m, ok := raw.(map[string]any)
	if ok && len(m) > 0 && allDollar(m) {
		var v value
		for k := range m {
			switch k {
			case "$set", "$expect", "$label", "$hcl":
			default:
				return v, fmt.Errorf("unknown value key %q (want $set, $expect, $label, $hcl)", k)
			}
		}
		if h, ok := m["$hcl"]; ok {
			s, ok := h.(string)
			if !ok {
				return v, fmt.Errorf("$hcl must be a string")
			}
			if _, both := m["$set"]; both {
				return v, fmt.Errorf("give $set or $hcl, not both")
			}
			v.Set = hclExpr(s)
		} else {
			v.Set = substituteHCL(m["$set"])
		}
		if e, ok := m["$expect"]; ok {
			v.Expect, v.HasExpect = e, true
		} else if _, isHCL := v.Set.(hclExpr); !isHCL && v.Set != nil {
			v.Expect, v.HasExpect = m["$set"], true
		}
		if l, ok := m["$label"].(string); ok {
			v.Label = l
		}
		v.Key = canonical(m["$set"])
		if _, isHCL := v.Set.(hclExpr); isHCL {
			v.Key = canonical(m["$hcl"])
		}
		if v.Label == "" {
			v.Label = shortLabel(v.Key)
		}
		return v, nil
	}
	v := value{Set: substituteHCL(raw), Key: canonical(raw)}
	if raw != nil {
		v.Expect, v.HasExpect = raw, true
	}
	v.Label = shortLabel(v.Key)
	return v, nil
}

// substituteHCL replaces nested {"$hcl": "..."} objects with hclExpr.
func substituteHCL(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 1 {
			if s, ok := t["$hcl"].(string); ok {
				return hclExpr(s)
			}
		}
		out := map[string]any{}
		for k, e := range t {
			out[k] = substituteHCL(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = substituteHCL(e)
		}
		return out
	}
	return v
}

func allDollar(m map[string]any) bool {
	for k := range m {
		if !strings.HasPrefix(k, "$") {
			return false
		}
	}
	return true
}

func shortLabel(key string) string {
	if key == "null" {
		return "unset"
	}
	var s string
	if json.Unmarshal([]byte(key), &s) == nil {
		key = s
	}
	if len(key) > 40 {
		return key[:37] + "..."
	}
	return key
}

// compileModel checks m against the live schema and the outputs contract.
func compileModel(m *model, s *resourceSchema, c *contract) (*compiledModel, error) {
	if s == nil {
		return nil, fmt.Errorf("no schema for %s", m.Type)
	}
	cm := &compiledModel{Model: m, Schema: s, Constraints: m.Constraints, Fixed: map[string]any{}}
	out := m.Inspect.Output
	if out == "" {
		out = "inspect"
	}
	fields, err := c.fields(m.Inspect.Item, out)
	if err != nil {
		return nil, err
	}
	if m.Inspect.Endpoint == "" || m.Inspect.IDAttr == "" {
		return nil, fmt.Errorf("inspect.endpoint and inspect.id_attr are required")
	}
	if _, ok := s.Children[m.Inspect.IDAttr]; !ok {
		return nil, fmt.Errorf("inspect.id_attr %q is not an attribute of %s", m.Inspect.IDAttr, m.Type)
	}
	cm.Upstream = m.Inspect.Item + " " + out
	for _, p := range sortedKeys(m.Fixed) {
		chain, err := s.lookup(p)
		if err != nil {
			return nil, fmt.Errorf("fixed: %w", err)
		}
		v := substituteHCL(m.Fixed[p])
		if err := checkValue(chain[len(chain)-1], v, m.Type+"."+p); err != nil {
			return nil, fmt.Errorf("fixed: %w", err)
		}
		cm.Fixed[p] = v
	}
	for _, p := range sortedKeys(m.Attributes) {
		a := m.Attributes[p]
		if _, dup := m.Fixed[p]; dup {
			return nil, fmt.Errorf("%s is both fixed and a factor", p)
		}
		chain, err := s.lookup(p)
		if err != nil {
			return nil, err
		}
		if len(a.Values) < 2 {
			return nil, fmt.Errorf("%s: a factor needs at least 2 values", p)
		}
		f := &factor{Path: p, Chain: chain, Inspect: a.Inspect, Match: a.Match}
		if f.Match == "" {
			f.Match = "equal"
		}
		switch f.Match {
		case "equal", "subset":
			if a.Inspect == "" {
				return nil, fmt.Errorf("%s: match %q needs an inspect path", p, f.Match)
			}
			up, err := resolveInspect(fields, a.Inspect)
			if err != nil {
				return nil, fmt.Errorf("%s: inspect %q: %w", p, a.Inspect, err)
			}
			f.Upstream = up
		case "none":
			if a.NoInspectReason == "" {
				return nil, fmt.Errorf("%s: match none needs no_inspect_reason", p)
			}
		default:
			return nil, fmt.Errorf("%s: unknown match %q (equal, subset, none)", p, f.Match)
		}
		seen := map[string]bool{}
		for i, raw := range a.Values {
			v, err := decodeValue(raw)
			if err != nil {
				return nil, fmt.Errorf("%s value %d: %w", p, i, err)
			}
			if err := checkValue(chain[len(chain)-1], v.Set, m.Type+"."+p); err != nil {
				return nil, fmt.Errorf("value %d: %w", i, err)
			}
			if seen[v.Label] {
				return nil, fmt.Errorf("%s: duplicate value label %q", p, v.Label)
			}
			seen[v.Label] = true
			f.Values = append(f.Values, v)
		}
		cm.Factors = append(cm.Factors, f)
	}
	// Two factors may not overlap (one a prefix of the other).
	for i, a := range cm.Factors {
		for _, b := range cm.Factors[i+1:] {
			if strings.HasPrefix(b.Path, a.Path+".") || strings.HasPrefix(a.Path, b.Path+".") {
				return nil, fmt.Errorf("factors %s and %s overlap", a.Path, b.Path)
			}
		}
		for fp := range cm.Fixed {
			if strings.HasPrefix(a.Path, fp+".") || strings.HasPrefix(fp, a.Path+".") {
				return nil, fmt.Errorf("factor %s overlaps fixed %s", a.Path, fp)
			}
		}
	}
	for ci, c := range m.Constraints {
		cls, err := cm.compileConstraint(ci, c)
		if err != nil {
			return nil, fmt.Errorf("constraint %d: %w", ci, err)
		}
		cm.Clauses = append(cm.Clauses, cls...)
	}
	return cm, nil
}

func (cm *compiledModel) factorIndex(path string) int {
	for i, f := range cm.Factors {
		if f.Path == path {
			return i
		}
	}
	return -1
}

// valueSet resolves a constraint's value list (labels or JSON values).
func (cm *compiledModel) valueSet(path string, vals []any) (int, map[int]bool, error) {
	fi := cm.factorIndex(path)
	if fi < 0 {
		return 0, nil, fmt.Errorf("%q is not a factor", path)
	}
	set := map[int]bool{}
	for _, want := range vals {
		found := false
		for vi, v := range cm.Factors[fi].Values {
			if s, ok := want.(string); ok && s == v.Label {
				set[vi], found = true, true
				continue
			}
			if canonical(want) == v.Key {
				set[vi], found = true, true
			}
		}
		if !found {
			return 0, nil, fmt.Errorf("%s has no value %s", path, canonical(want))
		}
	}
	return fi, set, nil
}

func (cm *compiledModel) compileConstraint(ci int, c constraintSpec) ([]clause, error) {
	if c.Reason == "" {
		return nil, fmt.Errorf("no reason")
	}
	if len(c.Forbid) == 0 && len(c.Requires) == 0 {
		return nil, fmt.Errorf("needs forbid or requires")
	}
	base := map[int]map[int]bool{}
	for _, p := range sortedKeys(c.When) {
		fi, set, err := cm.valueSet(p, c.When[p])
		if err != nil {
			return nil, fmt.Errorf("when: %w", err)
		}
		base[fi] = set
	}
	var out []clause
	add := func(fi int, set map[int]bool) error {
		if _, clash := base[fi]; clash {
			return fmt.Errorf("%s is in both when and forbid/requires", cm.Factors[fi].Path)
		}
		lits := map[int]map[int]bool{fi: set}
		for k, v := range base {
			lits[k] = v
		}
		out = append(out, clause{Lits: lits, Source: ci})
		return nil
	}
	for _, p := range sortedKeys(c.Forbid) {
		fi, set, err := cm.valueSet(p, c.Forbid[p])
		if err != nil {
			return nil, fmt.Errorf("forbid: %w", err)
		}
		if err := add(fi, set); err != nil {
			return nil, err
		}
	}
	for _, p := range sortedKeys(c.Requires) {
		fi, set, err := cm.valueSet(p, c.Requires[p])
		if err != nil {
			return nil, fmt.Errorf("requires: %w", err)
		}
		inv := map[int]bool{}
		for vi := range cm.Factors[fi].Values {
			if !set[vi] {
				inv[vi] = true
			}
		}
		if len(inv) == 0 {
			continue
		}
		if err := add(fi, inv); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// violates reports whether the (possibly partial, -1 = unassigned) row
// violates a clause whose factors are all assigned.
func (cm *compiledModel) violates(row []int) bool {
	for _, cl := range cm.Clauses {
		hit := true
		for fi, set := range cl.Lits {
			if row[fi] < 0 || !set[row[fi]] {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

// labels returns the row as attribute -> value label, sorted by attribute.
func (cm *compiledModel) labels(row []int) [][2]string {
	out := make([][2]string, len(row))
	for i, vi := range row {
		out[i] = [2]string{cm.Factors[i].Path, cm.Factors[i].Values[vi].Label}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// labelMap returns the row as attribute -> value label.
func (cm *compiledModel) labelMap(row []int) map[string]string {
	out := map[string]string{}
	for i, vi := range row {
		out[cm.Factors[i].Path] = cm.Factors[i].Values[vi].Label
	}
	return out
}
