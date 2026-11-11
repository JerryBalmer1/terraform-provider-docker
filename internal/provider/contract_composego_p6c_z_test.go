package provider

// Pass 6c Lane Z: the structured `project` output of the compose items takes its shape from compose-go's Go types
// (tools/contractgen/composego.go). These tests fail on any mismatch between contract_gen.go and the pinned
// compose-go, and on any fixture value the contract cannot hold.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	ctypes "github.com/compose-spec/compose-go/v2/types"
)

// p6czComposeKind is an independent restatement of composego.go's rule: the Terraform kind compose-go's JSON gives
// a Go type (after composeServiceFixups). It returns the kind and, for collections, the element type; for structs,
// t itself.
func p6czComposeKind(t reflect.Type) (contractNodeKind, reflect.Type) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.PkgPath() == reflect.TypeOf(ctypes.Project{}).PkgPath() {
		switch t.Name() {
		case "HostsList": // composeServiceFixups: host -> addresses
			return cnMap, reflect.TypeOf([]string{})
		case "UlimitsConfig": // composeServiceFixups: always the struct shape
			return cnObject, t
		}
	}
	mt := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	if t.Implements(mt) || reflect.PointerTo(t).Implements(mt) {
		v := reflect.New(t)
		if t.Kind() == reflect.Struct {
			for i := 0; i < t.NumField(); i++ {
				if f := v.Elem().Field(i); f.CanSet() && f.Kind() == reflect.String {
					f.SetString("x")
					break // the first string field only (SSHKey: its ID; with a Path compose-go writes invalid JSON)
				}
			}
		}
		if b, err := json.Marshal(v.Interface()); err == nil && len(b) > 0 {
			switch b[0] {
			case '"':
				return cnString, nil
			case 't', 'f':
				return cnBool, nil
			case '{', '[', 'n':
			default:
				return cnNumber, nil
			}
		}
	}
	switch t.Kind() {
	case reflect.Struct:
		return cnObject, t
	case reflect.Map:
		return cnMap, t.Elem()
	case reflect.Slice, reflect.Array:
		return cnList, t.Elem()
	case reflect.String:
		return cnString, nil
	case reflect.Bool:
		return cnBool, nil
	case reflect.Interface:
		return cnDynamic, nil
	}
	return cnNumber, nil
}

// p6czJSONKeys: the JSON keys of a struct and their field types, as encoding/json writes them.
func p6czJSONKeys(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	var embedded []reflect.Type
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if f.Anonymous && name == "" && ft.Kind() == reflect.Struct {
			embedded = append(embedded, ft)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	for _, et := range embedded {
		for k, v := range p6czJSONKeys(et) {
			if _, ok := out[k]; !ok {
				out[k] = v
			}
		}
	}
	return out
}

func p6czWalk(t *testing.T, n *contractNode, gt reflect.Type, path string, seen map[reflect.Type]int, bad *[]string) {
	kind, sub := p6czComposeKind(gt)
	if n.Kind == cnDynamic && kind == cnObject && seen[sub] > 0 {
		return // a recursive type is cut off as dynamic
	}
	want := kind
	if want == cnList && n.Kind == cnSet {
		want = cnSet
	}
	if n.Kind != want {
		*bad = append(*bad, path+": contract kind "+p6czKindName(n.Kind)+", compose-go "+p6czKindName(want)+" ("+gt.String()+")")
		return
	}
	switch kind {
	case cnList, cnMap:
		p6czWalk(t, n.Elem, sub, path+"[]", seen, bad)
	case cnObject:
		keys := p6czJSONKeys(sub)
		s := map[reflect.Type]int{}
		for k, v := range seen {
			s[k] = v
		}
		s[sub]++
		got := map[string]bool{}
		for _, a := range n.Attrs {
			got[a.Upstream] = true
			ft, ok := keys[a.Upstream]
			if !ok {
				continue // provider-computed field (e.g. version); the round trip below proves it holds no value
			}
			p6czWalk(t, a.Node, ft, path+"."+a.Upstream, s, bad)
		}
		for k := range keys {
			if !got[k] {
				*bad = append(*bad, path+"."+k+": compose-go writes it, the contract has no field")
			}
		}
	}
}

// TestContractComposeGoShapes: every field of the full compose project outputs has the kind compose-go's JSON gives
// it at the pinned version (ports published is a string, cpus a number, byte sizes and durations strings, ...).
func TestContractComposeGoShapes(t *testing.T) {
	for _, id := range []string{"compose:name", "compose:cli-config"} {
		it := contractMustItem(id)
		var node *contractNode
		for _, o := range it.Outputs {
			if o.Name == "project" {
				node = contractOutputNode(it, o)
			}
		}
		if node == nil {
			t.Fatalf("%s has no project output", id)
		}
		var bad []string
		p6czWalk(t, node, reflect.TypeOf(ctypes.Project{}), id+"/project", map[reflect.Type]int{}, &bad)
		sort.Strings(bad)
		if len(bad) > 0 {
			t.Errorf("%d mismatches between contract_gen.go and compose-go:\n%s", len(bad), strings.Join(bad, "\n"))
		}
		// Spot checks named in the Pass 6c finding.
		svc := node
		for _, a := range node.Attrs {
			if a.Upstream == "services" {
				svc = a.Node.Elem
			}
		}
		for _, a := range svc.Attrs {
			switch a.Upstream {
			case "ports":
				for _, p := range a.Node.Elem.Attrs {
					if p.Upstream == "published" && p.Node.Kind != cnString {
						t.Errorf("%s ports.published is %s, want string (compose-go ServicePortConfig.Published)", id, p6czKindName(p.Node.Kind))
					}
				}
			case "mem_limit", "shm_size":
				if a.Node.Kind != cnString {
					t.Errorf("%s %s is %s, want string (UnitBytes marshals as \"%%d\")", id, a.Upstream, p6czKindName(a.Node.Kind))
				}
			case "cpus":
				if a.Node.Kind != cnNumber {
					t.Errorf("%s cpus is %s, want number", id, p6czKindName(a.Node.Kind))
				}
			}
		}
	}
}

// TestContractComposeFixturesRoundTrip: every compose fixture in the repository, loaded the way the provider loads
// it, fits the project contract: no value is dropped and no key lacks a field.
func TestContractComposeFixturesRoundTrip(t *testing.T) {
	var files []string
	for _, g := range []string{"testdata/compose_hash/*.yml", "../../test/fixtures/compose/*.yml"} {
		m, _ := filepath.Glob(g)
		files = append(files, m...)
	}
	if len(files) < 3 {
		t.Fatalf("only %d compose fixtures found", len(files))
	}
	it := contractMustItem("compose:name")
	var node *contractNode
	for _, o := range it.Outputs {
		if o.Name == "project" {
			node = contractOutputNode(it, o)
		}
	}
	loaded := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		abs, _ := filepath.Abs(filepath.Dir(f))
		p, err := composeLoad(context.Background(), composeInputs{Content: string(b), WorkingDir: abs,
			Env: map[string]string{"PARITY_DIR": "/tmp/parity", "REGISTRY": "registry.example.com"}})
		if err != nil {
			t.Logf("%s: not loadable without its test environment (%v); skipped", filepath.Base(f), err)
			continue
		}
		loaded++
		v := composeProjectValue(p)
		var warns, unknown []string
		_ = contractNormalize(node, v, "project", func(s string) { warns = append(warns, s) })
		p6cUnknownKeys(node, v, "project", &unknown)
		sort.Strings(unknown)
		if len(warns) > 0 || len(unknown) > 0 {
			t.Errorf("%s: %d dropped %v; %d keys without a field %v", filepath.Base(f), len(warns), warns, len(unknown), unknown)
		}
	}
	if loaded < 3 {
		t.Fatalf("only %d of %d fixtures loaded", loaded, len(files))
	}
	t.Logf("%d of %d compose fixtures round-trip", loaded, len(files))
}

func p6czKindName(k contractNodeKind) string {
	return [...]string{"string", "number", "bool", "dynamic", "json", "list", "set", "map", "object"}[k]
}
