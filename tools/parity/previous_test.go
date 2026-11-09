package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JerryBalmer1/terraform-provider-docker/tools/parity/paritydata"
)

func strp(s string) *string { return &s }

func TestParseReferenceDir(t *testing.T) {
	dir := t.TempDir()
	page := "# ephemeral.docker_registry_token\n\n## Usage\n\n| `x` | `string` | y |\n\n## Arguments and attributes\n\n" +
		"| Name | Type | Status |\n|---|---|---|\n| `registry` | `string` | required |\n| `token` | `object` | computed |\n\n" +
		"### `token`\n\n| `token` | `string` | computed |\n\n## Example\n\n| `ignored` | `string` | x |\n"
	if err := os.WriteFile(filepath.Join(dir, "ephemeral.docker_registry_token.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ParseReferenceDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []paritydata.Attr{{Type: "ephemeral.docker_registry_token", Path: "registry", Kind: "string"},
		{Type: "ephemeral.docker_registry_token", Path: "token", Kind: "object"},
		{Type: "ephemeral.docker_registry_token", Path: "token.token", Kind: "string"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestCheckPrevious(t *testing.T) {
	const typ = "ephemeral.docker_registry_token"
	prev := []paritydata.Attr{{Type: typ, Path: "token", Kind: "string"}, {Type: typ, Path: "registry", Kind: "string"},
		{Type: "data.docker_logs", Path: "logs_list_string", Kind: "list(string)"}, {Type: "docker_gone", Path: "a", Kind: "string"}}
	cur := []paritydata.Attr{{Type: typ, Path: "token", Kind: "object"}, {Type: typ, Path: "registry", Kind: "string"},
		{Type: "data.docker_container_logs", Path: "log_entries", Kind: "list(object)"}}
	var doc RenamesDoc
	doc.TypeRenames = append(doc.TypeRenames, TypeRename{KreuzwerkerType: "data.docker_logs", PreviousType: "data.docker_logs", OurType: "data.docker_container_logs"})
	doc.Renames = []RenameRow{{Type: "data.docker_container_logs", PreviousAttr: strp("logs_list_string"), OurAttr: strp("log_entries.message")}}

	// The shape change of token has no row: reported; the renamed type's attribute has one; a gone type is reported once.
	probs := CheckPrevious(prev, cur, doc)
	if len(probs) != 2 || probs[0].Type != "docker_gone" || probs[1].Path != "token" || probs[1].Reason != "string -> object" {
		t.Fatalf("problems = %+v", probs)
	}
	// With the row (as renames_post.py adds it from renames_overrides.json) the check passes.
	doc.Renames = append(doc.Renames, RenameRow{Type: typ, PreviousAttr: strp("token"), OurAttr: strp("token.token")})
	if probs := CheckPrevious(prev[:3], cur, doc); len(probs) != 0 {
		t.Fatalf("problems = %+v", probs)
	}
}

// The checked-in snapshot and the real renames.json: every pre-Pass 5 attribute that changed has a row,
// including ephemeral.docker_registry_token.token (string -> object).
func TestPreviousSnapshotHasTokenRow(t *testing.T) {
	prev, err := paritydata.PreviousAttrs()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range prev.Attributes {
		if a.Type == "ephemeral.docker_registry_token" && a.Path == "token" && a.Kind == "string" {
			found = true
		}
	}
	if !found {
		t.Fatal("snapshot lacks ephemeral.docker_registry_token.token (string)")
	}
	doc, err := LoadRenames(findRepoFile(filepath.Join("reference", "_gen", "renames.json")))
	if err != nil {
		t.Skip("renames.json not found:", err)
	}
	ok := false
	for _, r := range doc.Renames {
		if r.Type == "ephemeral.docker_registry_token" && r.PreviousAttr != nil && *r.PreviousAttr == "token" && r.OurAttr != nil && *r.OurAttr == "token.token" {
			ok = true
		}
	}
	if !ok {
		t.Error("renames.json has no ephemeral.docker_registry_token token -> token.token row")
	}
}
