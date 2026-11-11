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

// Pass 6c Lane F: kind diagnostic from reference/_gen/deferred.d/diagnostics.json.
func TestDiagnosticsP6cF(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "reference/inventory.json", `{"items":[]}`)
	writeFile(t, root, "internal/provider/client.go", "package p\n")
	writeFile(t, root, "reference/_gen/deferred.d/diagnostics.json", `{"diagnostics":[
		{"id":"tls-through-http-proxy","file":"internal/provider/client.go","reason":"handshake after CONNECT"}]}`)
	d, err := Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals["diagnostic"] != 1 || len(d.Items) != 1 {
		t.Fatalf("totals %v items %v", d.Totals, d.Items)
	}
	it := d.Items[0]
	if it.Kind != "diagnostic" || it.ID != "tls-through-http-proxy" || it.Source != "internal/provider/client.go" || it.Reason == "" {
		t.Errorf("item %+v", it)
	}
	writeFile(t, root, "reference/_gen/deferred.d/diagnostics.json", `{"diagnostics":[{"id":"x","file":"internal/provider/gone.go","reason":"r"}]}`)
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing file: err = %v", err)
	}
	writeFile(t, root, "reference/_gen/deferred.d/diagnostics.json", `{"diagnostics":[{"id":"x","file":"internal/provider/client.go"}]}`)
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "needs id and reason") {
		t.Errorf("missing reason: err = %v", err)
	}
}

// Pass 6d Lane D: kind known_issue from reference/_gen/deferred.d/known_issues.json.
func knownIssueFixture(t *testing.T) string {
	root := t.TempDir()
	writeFile(t, root, "reference/inventory.json", `{"items":[{"id":"compose:services","kind":"resource"}]}`)
	writeFile(t, root, "internal/provider/compose_hooks.go", "package p\n")
	writeFile(t, root, "docs/resources/compose.md", "## Known issues\n\n`compose-pre-start-per-replica`\n")
	writeFile(t, root, "CHANGELOG.md", "### Known issues\n\n- compose-pre-start-per-replica\n")
	writeFile(t, root, "reference/_gen/deferred.d/known_issues.json", `{"issues":[{"id":"compose-pre-start-per-replica",
		"item":"compose:services","file":"internal/provider/compose_hooks.go","reason":"differs from compose",
		"reproduce":"services:\n  web:\n    image: nginx","docs":["docs/resources/compose.md"],"changelog":true}]}`)
	return root
}

func TestKnownIssuesP6dD(t *testing.T) {
	root := knownIssueFixture(t)
	d, err := Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals["known_issue"] != 1 || len(d.Items) != 1 {
		t.Fatalf("totals %v items %v", d.Totals, d.Items)
	}
	it := d.Items[0]
	if it.Kind != "known_issue" || it.ID != "compose-pre-start-per-replica" || it.Source != "internal/provider/compose_hooks.go" ||
		it.Reason != "compose:services: differs from compose" || !strings.Contains(it.Reproduce, "image: nginx") {
		t.Errorf("item %+v", it)
	}
	b, err := d.Marshal()
	if err != nil || !strings.Contains(string(b), `"reproduce": "services:\n  web:`) {
		t.Errorf("marshal: %v\n%s", err, b)
	}
	// Items of other kinds carry no reproduce key.
	other, _ := newDoc([]Item{{Kind: "field_skip", ID: "x", Reason: "r", Source: "s"}})
	if b, _ := other.Marshal(); strings.Contains(string(b), "reproduce") {
		t.Errorf("reproduce on a field_skip item:\n%s", b)
	}
}

func TestKnownIssuesMissingFileP6dD(t *testing.T) {
	root := knownIssueFixture(t)
	if err := os.Remove(filepath.Join(root, "internal", "provider", "compose_hooks.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing code file: err = %v", err)
	}
	root = knownIssueFixture(t)
	if err := os.Remove(filepath.Join(root, "docs", "resources", "compose.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "docs/resources/compose.md") {
		t.Errorf("missing docs page: err = %v", err)
	}
}

func TestKnownIssuesMissingFieldP6dD(t *testing.T) {
	for _, field := range []string{"id", "item", "file", "reason", "reproduce", "docs"} {
		root := knownIssueFixture(t)
		row := map[string]string{"id": `"compose-pre-start-per-replica"`, "item": `"compose:services"`,
			"file": `"internal/provider/compose_hooks.go"`, "reason": `"r"`, "reproduce": `"x"`, "docs": `["docs/resources/compose.md"]`}
		delete(row, field)
		var parts []string
		for k, v := range row {
			parts = append(parts, `"`+k+`":`+v)
		}
		writeFile(t, root, "reference/_gen/deferred.d/known_issues.json", `{"issues":[{`+strings.Join(parts, ",")+`}]}`)
		if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "every row needs") {
			t.Errorf("without %s: err = %v", field, err)
		}
	}
}

func TestKnownIssuesCrossChecksP6dD(t *testing.T) {
	root := knownIssueFixture(t)
	writeFile(t, root, "reference/inventory.json", `{"items":[]}`)
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "not in reference/inventory.json") {
		t.Errorf("unknown item: err = %v", err)
	}
	root = knownIssueFixture(t)
	writeFile(t, root, "CHANGELOG.md", "### Known issues\n")
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "not mentioned in CHANGELOG.md") {
		t.Errorf("changelog without the id: err = %v", err)
	}
	root = knownIssueFixture(t)
	writeFile(t, root, "docs/resources/compose.md", "no section\n")
	if _, err := Collect(root); err == nil || !strings.Contains(err.Error(), "not mentioned in docs/resources/compose.md") {
		t.Errorf("docs page without the id: err = %v", err)
	}
}
