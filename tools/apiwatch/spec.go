package main

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the part of a Docker Engine API swagger 2.0 document apiwatch
// compares: every endpoint with its parameters, and every definition with its
// top-level properties.
type Spec struct {
	Version   string
	Endpoints map[string]Endpoint // key "METHOD /path"
	Defs      map[string][]string // definition name -> sorted property names
}

// Endpoint is one operation.
type Endpoint struct {
	Method, Path, OperationID string
	Params                    []string // "in:name", sorted
	Body                      string   // $ref or type of the body parameter, if any
	Deprecated                bool
}

var httpMethods = []string{"get", "put", "post", "delete", "head", "patch", "options"}

// ParseSpec reads a swagger 2.0 YAML document.
func ParseSpec(b []byte) (*Spec, error) {
	var doc struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
		Paths       map[string]map[string]yaml.Node `yaml:"paths"`
		Definitions map[string]struct {
			Properties map[string]yaml.Node `yaml:"properties"`
			AllOf      []struct {
				Properties map[string]yaml.Node `yaml:"properties"`
			} `yaml:"allOf"`
		} `yaml:"definitions"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("parsing spec: no paths (not an Engine API swagger document?)")
	}
	s := &Spec{Version: doc.Info.Version, Endpoints: map[string]Endpoint{}, Defs: map[string][]string{}}
	for p, ops := range doc.Paths {
		for _, m := range httpMethods {
			n, ok := ops[m]
			if !ok {
				continue
			}
			var op struct {
				OperationID string `yaml:"operationId"`
				Deprecated  bool   `yaml:"deprecated"`
				Parameters  []struct {
					Name   string `yaml:"name"`
					In     string `yaml:"in"`
					Schema struct {
						Ref  string `yaml:"$ref"`
						Type string `yaml:"type"`
					} `yaml:"schema"`
				} `yaml:"parameters"`
			}
			if err := n.Decode(&op); err != nil {
				return nil, fmt.Errorf("parsing %s %s: %w", strings.ToUpper(m), p, err)
			}
			e := Endpoint{Method: strings.ToUpper(m), Path: p, OperationID: op.OperationID, Deprecated: op.Deprecated}
			for _, prm := range op.Parameters {
				e.Params = append(e.Params, prm.In+":"+prm.Name)
				if prm.In == "body" {
					e.Body = prm.Schema.Ref
					if e.Body == "" {
						e.Body = prm.Schema.Type
					}
				}
			}
			sort.Strings(e.Params)
			s.Endpoints[e.Method+" "+p] = e
		}
	}
	for name, d := range doc.Definitions {
		var props []string
		for k := range d.Properties {
			props = append(props, k)
		}
		for _, a := range d.AllOf {
			for k := range a.Properties {
				props = append(props, k)
			}
		}
		sort.Strings(props)
		s.Defs[name] = props
	}
	return s, nil
}

// Change is one difference between two specs.
type Change struct {
	Op      string // NEW, CHANGED, REMOVED (endpoints); FIELD+, FIELD-, DEF+, DEF- (definitions)
	Subject string // "POST /containers/create" or "HostConfig.Foo"
	Detail  string
	Target  Target
}

// Diff lists what changed from old to new, endpoints first, each group sorted.
func Diff(old, new *Spec) []Change {
	var out []Change
	for _, k := range sortedKeys(new.Endpoints) {
		ne := new.Endpoints[k]
		oe, ok := old.Endpoints[k]
		if !ok {
			out = append(out, Change{Op: "NEW", Subject: k, Detail: opDetail(ne), Target: MapEndpoint(ne.Method, ne.Path)})
			continue
		}
		if d := endpointDelta(oe, ne); d != "" {
			out = append(out, Change{Op: "CHANGED", Subject: k, Detail: d, Target: MapEndpoint(ne.Method, ne.Path)})
		}
	}
	for _, k := range sortedKeys(old.Endpoints) {
		if _, ok := new.Endpoints[k]; !ok {
			oe := old.Endpoints[k]
			out = append(out, Change{Op: "REMOVED", Subject: k, Detail: opDetail(oe), Target: MapEndpoint(oe.Method, oe.Path)})
		}
	}
	for _, name := range sortedKeys(new.Defs) {
		props, ok := old.Defs[name]
		if !ok {
			out = append(out, Change{Op: "DEF+", Subject: name, Detail: fmt.Sprintf("%d field(s)", len(new.Defs[name])), Target: MapDefinition(name)})
			continue
		}
		added, removed := setDelta(props, new.Defs[name])
		for _, f := range added {
			out = append(out, Change{Op: "FIELD+", Subject: name + "." + f, Target: MapDefinition(name)})
		}
		for _, f := range removed {
			out = append(out, Change{Op: "FIELD-", Subject: name + "." + f, Target: MapDefinition(name)})
		}
	}
	for _, name := range sortedKeys(old.Defs) {
		if _, ok := new.Defs[name]; !ok {
			out = append(out, Change{Op: "DEF-", Subject: name, Target: MapDefinition(name)})
		}
	}
	return out
}

func opDetail(e Endpoint) string {
	if e.OperationID == "" {
		return ""
	}
	return "operationId " + e.OperationID
}

func endpointDelta(o, n Endpoint) string {
	var parts []string
	added, removed := setDelta(o.Params, n.Params)
	for _, p := range added {
		parts = append(parts, "+"+p)
	}
	for _, p := range removed {
		parts = append(parts, "-"+p)
	}
	if o.Body != n.Body {
		parts = append(parts, fmt.Sprintf("body %s -> %s", orDash(o.Body), orDash(n.Body)))
	}
	if !o.Deprecated && n.Deprecated {
		parts = append(parts, "now deprecated")
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// setDelta returns the elements only in b (added) and only in a (removed).
func setDelta(a, b []string) (added, removed []string) {
	in := func(s []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range s {
			m[x] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	for _, x := range b {
		if !ma[x] {
			added = append(added, x)
		}
	}
	for _, x := range a {
		if !mb[x] {
			removed = append(removed, x)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
