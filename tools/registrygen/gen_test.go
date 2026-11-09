package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConstructor(t *testing.T) {
	for _, c := range []struct{ kind, name, want string }{
		{"resource", "docker_container", "NewContainerResource"},
		{"data_source", "docker_container_logs", "NewContainerLogsDataSource"},
		{"action", "docker_swarm_rotate_ca", "NewSwarmRotateCaAction"},
		{"ephemeral_resource", "docker_registry_login", "NewRegistryLoginEphemeralResource"},
		{"list_resource", "docker_swarm_node", "NewSwarmNodeListResource"},
		{"function", "parse_image_ref", "NewParseImageRefFunction"},
	} {
		if got := Constructor(Type{Kind: c.kind, Name: c.name}); got != c.want {
			t.Errorf("%s %s: got %s, want %s", c.kind, c.name, got, c.want)
		}
	}
}

func TestTypesGroupsAndOrders(t *testing.T) {
	inv := &Inventory{Items: []Item{
		{ID: "compose:services", Kind: "resource", Name: "docker_compose"},
		{ID: "compose:name", Kind: "resource", Name: "docker_compose"},
		{ID: "engine:info", Kind: "data_source", Name: "docker_info"},
		{ID: "x:skip", Kind: "skip"},
		{ID: "dockerfile:RUN", Kind: "dockerfile_block", Name: "docker_dockerfile.run"},
		{ID: "engine:function-image-ref", Kind: "function", Name: "image_ref"},
	}}
	types, err := Types(inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 3 {
		t.Fatalf("want 3 types, got %+v", types)
	}
	if types[0].Name != "docker_compose" || len(types[0].Items) != 2 || types[0].Items[0].ID != "compose:name" {
		t.Errorf("compose not grouped/sorted: %+v", types[0])
	}
	if types[1].Kind != "data_source" || types[2].Kind != "function" {
		t.Errorf("kind order wrong: %+v", types)
	}
	if _, err := Types(&Inventory{Items: []Item{{ID: "a:b", Kind: "action"}}}); err == nil {
		t.Error("an item without a name must fail")
	}
}

func TestGenerate(t *testing.T) {
	types := []Type{{Kind: "resource", Name: "docker_x", Items: []Item{{ID: "engine:x"}}},
		{Kind: "function", Name: "y_z", Items: []Item{{ID: "engine:f"}}}}
	src, test, err := Generate(types)
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{"DO NOT EDIT", "NewXResource, // resource docker_x", "NewYZFunction, // function y_z", "func genDataSources()"} {
		if !strings.Contains(s, want) {
			t.Errorf("registry_gen.go lacks %q", want)
		}
	}
	if !strings.Contains(string(test), `"resource docker_x",`) {
		t.Error("registry_gen_test.go lacks the want entry")
	}
}

func TestWriteStubs(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "have.go"), []byte("package provider\n\nfunc NewHaveResource() {}\n"), 0o644))
	lanes := filepath.Join(dir, "lanes.json")
	must(os.WriteFile(lanes, []byte(`{"engine:have":"A","engine:new":"A","swarm:b":"B"}`), 0o644))
	types := []Type{
		{Kind: "resource", Name: "docker_have", Items: []Item{{ID: "engine:have"}}},
		{Kind: "data_source", Name: "docker_new", Items: []Item{{ID: "engine:new", Summary: "New thing."}}},
		{Kind: "action", Name: "docker_b", Items: []Item{{ID: "swarm:b"}}},
	}
	n, err := WriteStubs(types, dir, lanes)
	must(err)
	if n != 2 {
		t.Fatalf("want 2 stubs, got %d", n)
	}
	a, err := os.ReadFile(filepath.Join(dir, "stubs_pass5_a.go"))
	must(err)
	if !strings.Contains(string(a), "// STUB(pass5) engine:new\nfunc NewNewDataSource() datasource.DataSource {") ||
		strings.Contains(string(a), "NewHaveResource") {
		t.Errorf("unexpected lane A stubs:\n%s", a)
	}
	if _, err := os.Stat(filepath.Join(dir, "stubs_pass5_b.go")); err != nil {
		t.Error(err)
	}
	if _, err := WriteStubs([]Type{{Kind: "list_resource", Name: "docker_q", Items: []Item{{ID: "engine:new"}}}}, dir, lanes); err == nil {
		t.Error("a list resource without a constructor must fail")
	}
}
