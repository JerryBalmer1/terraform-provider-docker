package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, path, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	root := t.TempDir()
	writeFile(t, root, "reference/_gen/fieldmap.d/S.json", `{"about":"x","fields":{
		"engine:A/B":{"skip":"daemon drops it"},
		"engine:A/C":{"item":"engine:service","partial":"only on create"},
		"engine:A/D":{"item":"engine:service","attr":"d"}}}`)
	writeFile(t, root, "internal/provider/x.go", "package p\n// PARTIAL(pass5) buildx:bake policy is not evaluated\n// Pass 5.1: PARTIAL(pass5) status markers\n")
	writeFile(t, root, "internal/provider/x_test.go", "package p\n// PARTIAL(pass5) ignored:in-tests note\n")
	writeFile(t, root, "reference/inventory.json", `{"items":[{"id":"engine:attach","kind":"skip","skip_reason":"stream"},{"id":"engine:x","kind":"resource"}]}`)
	writeFile(t, root, "internal/provider/a_acc_test.go", "package p\nfunc TestAccA(t *testing.T) {}\n")
	writeFile(t, root, "reference/_gen/deferred.d/acceptance.json", `{"tests":[{"test":"TestAccA","file":"internal/provider/a_acc_test.go","reason":"Windows only"}]}`)
	writeFile(t, root, "examples/data-sources/docker_x/data-source.tf", "data \"docker_x\" \"a\" {}\n")
	writeFile(t, root, "examples/resources/docker_y/resource.tf", "resource \"docker_y\" \"a\" {}\n")
	writeFile(t, root, "reference/_gen/deferred.d/examples.json", `{"examples":[
		{"example":"data-sources/docker_x","reason":"needs a GPU"},
		{"example":"resources/docker_y","part":"resource.docker_y.remote","reason":"needs an external host"}]}`)
	return root
}

func TestCollect(t *testing.T) {
	d, err := Collect(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"field_skip": 1, "field_partial": 1, "partial_marker": 1, "inventory_skip": 1, "acceptance": 1, "example": 2}
	for k, n := range want {
		if d.Totals[k] != n {
			t.Errorf("%s = %d, want %d (items %+v)", k, d.Totals[k], n, d.Items)
		}
	}
	if d.Items[0].Kind != "field_skip" || d.Items[len(d.Items)-1].Kind != "example" {
		t.Errorf("items not in kind order: %+v", d.Items)
	}
	for _, it := range d.Items {
		if it.Kind == "field_partial" && it.Reason != "engine:service: only on create" {
			t.Errorf("partial reason %q", it.Reason)
		}
		if it.Kind == "partial_marker" && (it.ID != "buildx:bake" || it.Source != "internal/provider/x.go") {
			t.Errorf("marker %+v", it)
		}
		if it.Kind == "example" && it.ID != "data-sources/docker_x" && it.ID != "resources/docker_y (resource.docker_y.remote)" {
			t.Errorf("example item %+v", it)
		}
	}
}

func TestAcceptanceMissingTest(t *testing.T) {
	root := fixture(t)
	writeFile(t, root, "reference/_gen/deferred.d/acceptance.json", `{"tests":[{"test":"TestAccGone","file":"internal/provider/a_acc_test.go","reason":"r"}]}`)
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "TestAccGone") {
		t.Fatalf("want an error naming TestAccGone, got %v", err)
	}
}

func TestExampleMissingFolder(t *testing.T) {
	root := fixture(t)
	writeFile(t, root, "reference/_gen/deferred.d/examples.json", `{"examples":[{"example":"resources/docker_gone","reason":"r"}]}`)
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "resources/docker_gone") {
		t.Fatalf("want an error naming resources/docker_gone, got %v", err)
	}
}

func TestExampleNoReason(t *testing.T) {
	root := fixture(t)
	writeFile(t, root, "reference/_gen/deferred.d/examples.json", `{"examples":[{"example":"resources/docker_y","reason":""}]}`)
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "no reason") {
		t.Fatalf("want a no-reason error, got %v", err)
	}
}

func TestNoReason(t *testing.T) {
	if _, err := newDoc([]Item{{Kind: "field_skip", ID: "x", Source: "s"}}); err == nil {
		t.Fatal("want error for an empty reason")
	}
}

func TestDiff(t *testing.T) {
	old, _ := newDoc([]Item{{Kind: "field_skip", ID: "a", Reason: "r", Source: "s"}, {Kind: "field_skip", ID: "b", Reason: "r", Source: "s"}})
	cur, _ := newDoc([]Item{{Kind: "field_skip", ID: "b", Reason: "r2", Source: "s"}, {Kind: "acceptance", ID: "c", Reason: "r", Source: "s"}})
	got := Diff(old, cur)
	for _, w := range []string{"+ acceptance c (s)", "- field_skip a (s)", "1 added, 1 removed, 1 reasons changed"} {
		if !strings.Contains(got, w) {
			t.Errorf("diff missing %q:\n%s", w, got)
		}
	}
	if !strings.Contains(Diff(nil, cur), "no earlier version: 2 added") {
		t.Errorf("nil base: %s", Diff(nil, cur))
	}
}

func TestRunWriteThenCheck(t *testing.T) {
	root := fixture(t)
	if code := run(root, "reference/_gen/deferred.json", "", true); code != 0 {
		t.Fatalf("write exit %d", code)
	}
	if code := run(root, "reference/_gen/deferred.json", "", false); code != 0 {
		t.Fatalf("check after write exit %d", code)
	}
	writeFile(t, root, "reference/_gen/fieldmap.d/S.json", `{"about":"x","fields":{}}`)
	if code := run(root, "reference/_gen/deferred.json", "", false); code != 1 {
		t.Fatalf("stale check exit %d, want 1", code)
	}
}
