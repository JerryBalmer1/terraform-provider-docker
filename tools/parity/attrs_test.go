package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JerryBalmer1/terraform-provider-docker/tools/parity/paritydata"
)

func sp(s string) *string { return &s }

// allMappedRenames writes a renames.json that maps every checked-in
// kreuzwerker attribute (applying the type renames) and returns its path.
func allMappedRenames(t *testing.T, dir string) string {
	t.Helper()
	attrs, err := paritydata.KreuzwerkerAttrs()
	if err != nil {
		t.Fatal(err)
	}
	var doc RenamesDoc
	doc.TypeRenames = append(doc.TypeRenames, TypeRename{KreuzwerkerType: "data.docker_logs", OurType: "data.docker_container_logs"})
	for _, a := range attrs.Attributes {
		typ := a.Type
		if typ == "data.docker_logs" {
			typ = "data.docker_container_logs"
		}
		doc.Renames = append(doc.Renames, RenameRow{Type: typ, KreuzwerkerAttr: sp(a.Path), OurAttr: sp(a.Path), Transform: "identity"})
	}
	b, _ := json.Marshal(doc)
	p := filepath.Join(dir, "renames.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKreuzwerkerAttrsEmbedded(t *testing.T) {
	d, err := paritydata.KreuzwerkerAttrs()
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "v4.6.0" || d.Commit == "" {
		t.Errorf("version %q commit %q", d.Version, d.Commit)
	}
	// Every kreuzwerker type of the static list has attributes, and nothing else does.
	types := map[string]bool{}
	for _, a := range d.Attributes {
		types[a.Type] = true
		if a.Path == "" || a.Kind == "" {
			t.Errorf("incomplete attribute %+v", a)
		}
	}
	prefix := map[Kind]string{Resource: "", DataSource: "data.", Action: "action."}
	for _, e := range kreuzwerker {
		if !types[prefix[e.Kind]+e.Name] {
			t.Errorf("%s %s has no attributes in %s", e.Kind, e.Name, paritydata.KreuzwerkerAttrsFile)
		}
		delete(types, prefix[e.Kind]+e.Name)
	}
	if len(types) > 0 {
		t.Errorf("attributes of types not in paritydata.Kreuzwerker: %v", types)
	}
}

func TestCheckAttrs(t *testing.T) {
	attrs := []paritydata.Attr{
		{Type: "docker_container", Path: "ports.internal", Kind: "Number"},
		{Type: "docker_container", Path: "shm_size", Kind: "Number"},
		{Type: "docker_container", Path: "timeouts", Kind: "Block"},
		{Type: "docker_container", Path: "unlisted_attr", Kind: "String"},
		{Type: "docker_container", Path: "blank", Kind: "String"},
		{Type: "docker_container", Path: "never", Kind: "String"},
		{Type: "data.docker_logs", Path: "follow", Kind: "Boolean"},
		{Type: "action.docker_exec", Path: "tty", Kind: "Boolean"},
	}
	var doc RenamesDoc
	doc.TypeRenames = append(doc.TypeRenames, TypeRename{KreuzwerkerType: "data.docker_logs", OurType: "data.docker_container_logs"})
	doc.Renames = []RenameRow{
		{Type: "docker_container", KreuzwerkerAttr: sp("ports.internal"), OurAttr: sp("ports.container_port"), Transform: "rename"},
		{Type: "docker_container", KreuzwerkerAttr: sp("shm_size"), Transform: "dropped", Reason: "gap: not in our schema; if added, name it `shm_size`."},
		{Type: "docker_container", KreuzwerkerAttr: sp("timeouts"), Transform: "dropped", Reason: "dropped: SDKv2 timeouts block not provided."},
		{Type: "docker_container", KreuzwerkerAttr: sp("blank"), Transform: "dropped", Reason: "  "},
		{Type: "docker_container", KreuzwerkerAttr: sp("never"), Transform: "dropped", Reason: "won't implement: Windows only."},
		{Type: "data.docker_container_logs", KreuzwerkerAttr: sp("follow"), OurAttr: sp("follow"), Transform: "identity"},
		{Type: "action.docker_exec", KreuzwerkerAttr: sp("tty"), Transform: "dropped", Reason: "gap: if added, name it `tty`."},
		{Type: "docker_container", PreviousAttr: sp("x"), OurAttr: sp("y"), Transform: "rename"}, // our own rename: ignored
	}
	probs, counts := CheckAttrs(attrs, doc)
	var got []string
	for _, p := range probs {
		got = append(got, p.Kind+" "+p.Type+" "+p.Path)
	}
	want := []string{
		"gap action.docker_exec tty",
		"no reason docker_container blank",
		"gap docker_container shm_size",
		"unlisted docker_container unlisted_attr",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if counts["mapped"] != 2 || counts["dropped"] != 2 {
		t.Errorf("counts = %v, want 2 mapped (ports.internal, logs follow), 2 dropped (timeouts, won't implement)", counts)
	}

	var b bytes.Buffer
	PrintAttrProblems(&b, probs)
	out := b.String()
	for _, s := range []string{
		"2 open gaps", "  action.docker_exec\n    tty: gap:", "  docker_container\n    shm_size: gap:",
		"1 kreuzwerker attributes with no renames.json row", "    unlisted_attr\n",
		"1 dropped attributes with no reason", "    blank\n",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func TestRunAttrCheck(t *testing.T) {
	dir := t.TempDir()
	ok := allMappedRenames(t, dir)
	var doc RenamesDoc
	b, _ := os.ReadFile(ok)
	_ = json.Unmarshal(b, &doc)
	doc.Renames[0].OurAttr = nil
	doc.Renames[0].Reason = "gap: if added, name it `x`."
	gap := filepath.Join(dir, "gap.json")
	b, _ = json.Marshal(doc)
	os.WriteFile(gap, b, 0o644)

	var out, errb bytes.Buffer
	if code := run([]string{"-renames", ok}, &out, &errb); code != 0 {
		t.Fatalf("all mapped = %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "dropped with a reason, 0 open") {
		t.Errorf("summary missing:\n%s", out.String())
	}
	errb.Reset()
	if code := run([]string{"-renames", gap}, &out, &errb); code != 1 {
		t.Errorf("one gap = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "1 open gaps") || !strings.Contains(errb.String(), doc.Renames[0].Type) {
		t.Errorf("gap not reported by type:\n%s", errb.String())
	}
	if code := run([]string{"-renames", gap, "-types-only"}, &out, &errb); code != 0 {
		t.Errorf("-types-only = %d, want 0", code)
	}
	if code := run([]string{"-renames", filepath.Join(dir, "none.json")}, &out, &errb); code != 2 {
		t.Errorf("missing renames = %d, want 2", code)
	}
}

func TestExtractAttrs(t *testing.T) {
	docs := filepath.Join(t.TempDir(), "docs")
	for _, d := range []string{"resources", "data-sources", "actions"} {
		os.MkdirAll(filepath.Join(docs, d), 0o755)
	}
	os.WriteFile(filepath.Join(docs, "resources", "thing.md"), []byte(strings.Join([]string{
		"# docker_thing", "", "- `ignored` (String) before the schema", "", "## Schema", "", "### Required", "",
		"- `name` (String) The name.", "- `ports` (Block List) Ports. (see [below](#x))", "",
		"<a id=\"x\"></a>", "### Nested Schema for `ports`", "", "Required:", "", "- `internal` (Number) Port.", "",
		"## Import", "", "- `after` (String) not schema",
	}, "\r\n")), 0o644)
	os.WriteFile(filepath.Join(docs, "data-sources", "logs.md"), []byte("## Schema\n\n- `follow` (Boolean) F.\n"), 0o644)
	os.WriteFile(filepath.Join(docs, "actions", "exec.md"), []byte("## Schema\n\n- `tty` (Boolean) T.\n"), 0o644)
	os.WriteFile(filepath.Join(docs, "actions", "notes.txt"), []byte("- `x` (String)\n"), 0o644)
	attrs, err := ExtractAttrs(docs)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range attrs {
		got = append(got, a.Type+" "+a.Path+" "+a.Kind)
	}
	want := "docker_thing name String\ndocker_thing ports Block List\ndocker_thing ports.internal Number\ndata.docker_logs follow Boolean\naction.docker_exec tty Boolean"
	if strings.Join(got, "\n") != want {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}

	// -extract writes the file the check embeds.
	out := filepath.Join(t.TempDir(), "attrs.json")
	var o, e bytes.Buffer
	if code := run([]string{"-extract", filepath.Dir(docs), "-out", out, "-commit", "abc"}, &o, &e); code != 0 {
		t.Fatalf("-extract = %d: %s", code, e.String())
	}
	var d paritydata.AttrsDoc
	b, _ := os.ReadFile(out)
	if err := json.Unmarshal(b, &d); err != nil || len(d.Attributes) != 5 || d.Commit != "abc" || d.Version != "v4.6.0" {
		t.Errorf("written doc = %+v (%v)", d, err)
	}
	if code := run([]string{"-extract", filepath.Join(t.TempDir(), "missing")}, &o, &e); code != 2 {
		t.Errorf("-extract of a missing checkout = %d, want 2", code)
	}
}

// The checked-in list equals a fresh extraction when the kreuzwerker clone
// is available (TPD_KREUZWERKER_DIR); otherwise skipped.
func TestKreuzwerkerAttrsFresh(t *testing.T) {
	dir := os.Getenv("TPD_KREUZWERKER_DIR")
	if dir == "" {
		t.Skip("TPD_KREUZWERKER_DIR not set")
	}
	attrs, err := ExtractAttrs(filepath.Join(dir, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := paritydata.KreuzwerkerAttrs()
	a, _ := json.Marshal(attrs)
	b, _ := json.Marshal(d.Attributes)
	if !bytes.Equal(a, b) {
		t.Errorf("%s is stale: run go run ./tools/parity -extract %s", paritydata.KreuzwerkerAttrsFile, dir)
	}
}

// The real renames.json lists every kreuzwerker attribute (mapped, dropped
// or gap); open gaps are reported by `go run ./tools/parity`, not here.
func TestRealRenamesListsEveryAttr(t *testing.T) {
	doc, err := LoadRenames(filepath.Join("..", "..", "reference", "_gen", "renames.json"))
	if err != nil {
		t.Skipf("renames.json: %v", err)
	}
	attrs, _ := paritydata.KreuzwerkerAttrs()
	probs, _ := CheckAttrs(attrs.Attributes, doc)
	for _, p := range probs {
		if p.Kind != "gap" {
			t.Errorf("%s %s %s", p.Kind, p.Type, p.Path)
		}
	}
}
