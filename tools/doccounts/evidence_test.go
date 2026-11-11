package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testFieldMap = `{"about": "x", "fields": {
  "engine:a": {"item": "i", "attr": "a"},
  "engine:b": {"item": "i", "skip": "why"},
  "compose:c": {"item": "i", "attr": "c"},
  "compose:d": {"item": "i", "partial": "half"},
  "bake:e": {"item": "i"}
}}`

func TestParseFieldMap(t *testing.T) {
	fc, err := ParseFieldMap([]byte(testFieldMap))
	if err != nil {
		t.Fatal(err)
	}
	if fc.Total != 5 || fc.Mapped != 2 || fc.Skipped != 1 || fc.Partial != 1 || fc.Gap != 1 {
		t.Errorf("totals: %+v", fc)
	}
	if c := fc.BySource["compose"]; c == nil || c.Mapped != 1 || c.Partial != 1 {
		t.Errorf("compose: %+v", c)
	}
	if _, err := ParseFieldMap([]byte(`{"fields": {}}`)); err == nil {
		t.Error("empty fieldmap: want an error")
	}
	if _, err := ParseFieldMap([]byte(`{"fields": {"noprefix": {}}}`)); err == nil {
		t.Error("key without source: want an error")
	}
}

const testComposeFixtures = `package provider

var composeHashCompatFixtures = map[string]composeHashCompatFixture{
	"a": {File: "a.yml"},
	"b": {File: "b.yml", Env: map[string]string{"X": "1"}},
	"c": {File: "c.yml"},
}
`

const testComposeExample = `
resource "docker_compose" "local" {
  name    = "shop"
  content = "services: {}"
}
resource "docker_compose" "swarm" {
  mode    = "swarm"
  content = "services: {}"
}
resource "docker_compose" "files" {
  files = ["a.yml"]
}
resource "docker_network" "n" {
  name = "n"
}
`

const testAccFile = `package provider

func TestAccOne(t *testing.T) {}
func TestAccTwo_basic(t *testing.T) {}
func TestUnit(t *testing.T) {}
func testAccHelper(t *testing.T) {}
`

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, data := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testSources(t *testing.T) EvidenceSources {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"fieldmap.json":                 testFieldMap,
		"prov/compose_hash_test.go":     testComposeFixtures,
		"prov/acc_test.go":              testAccFile,
		"prov/acc2_test.go":             "package provider\n\nfunc TestAccThree(t *testing.T) {}\n",
		"prov/notatest.go":              "package provider\n\nfunc TestAccNot(t *testing.T) {}\n",
		"examples/compose/resource.tf":  testComposeExample,
		"examples/a/a.tests.ps1":        "",
		"examples/b/c/c.tests.ps1":      "",
		"examples/b/c/resource.tf":      "",
		"examples/b/c/notes.tests.ps1x": "",
	})
	return EvidenceSources{
		FieldMap:           filepath.Join(root, "fieldmap.json"),
		AccDir:             filepath.Join(root, "prov"),
		ComposeFixtureFile: filepath.Join(root, "prov", "compose_hash_test.go"),
		ComposeFixtureVar:  "composeHashCompatFixtures",
		ComposeExample:     filepath.Join(root, "examples", "compose", "resource.tf"),
		Examples:           filepath.Join(root, "examples"),
		SkipPairwise:       true,
	}
}

func TestCollectEvidence(t *testing.T) {
	e, err := CollectEvidence(testSources(t))
	if err != nil {
		t.Fatal(err)
	}
	if e.AccTests != 3 {
		t.Errorf("acceptance tests: %d, want 3", e.AccTests)
	}
	if e.ComposeFixtures != 4 { // 3 fixtures + 1 non-swarm inline example
		t.Errorf("compose fixtures: %d, want 4", e.ComposeFixtures)
	}
	if e.ExampleTests != 2 {
		t.Errorf("example tests: %d, want 2", e.ExampleTests)
	}
	if e.Pairwise != nil {
		t.Errorf("pairwise ran with SkipPairwise: %v", e.Pairwise)
	}
	s := testSources(t)
	s.ComposeFixtureVar = "nope"
	if _, err := CollectEvidence(s); err == nil || !strings.Contains(err.Error(), "no var nope") {
		t.Errorf("missing fixture var: %v", err)
	}
}

func TestParsePairwiseOutput(t *testing.T) {
	out := "docker_container: 20 attributes, 60 values, 3 constraints, 900 pairs (4 excluded by constraints), 31 configs\n" +
		"docker_volume: 4 attributes, 9 values, 0 constraints, 20 pairs (0 excluded by constraints), 6 configs\r\n"
	got, err := ParsePairwiseOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if got["docker_container"] != 31 || got["docker_volume"] != 6 || len(got) != 2 {
		t.Errorf("got %v", got)
	}
	if _, err := ParsePairwiseOutput("nothing\n"); err == nil {
		t.Error("no summary line: want an error")
	}
}

func TestEvidenceBlockCheckWrite(t *testing.T) {
	r := testReg(t)
	e, err := CollectEvidence(testSources(t))
	if err != nil {
		t.Fatal(err)
	}
	e.Pairwise = map[string]int{"docker_volume": 6, "docker_container": 31}
	blk := EvidenceBlock(r, e)
	for _, s := range []string{"| 3 |", "| 4 |", "| 2 |", "37 (container 31, volume 6)", "5: 2 mapped to an attribute, 1 skipped with a reason, 1 partial, 1 gaps", "| Registered types | 10: 2 resources"} {
		if !strings.Contains(blk, s) {
			t.Errorf("block lacks %q:\n%s", s, blk)
		}
	}
	doc := "# x\r\n" + evBeginLine + "\r\nold\r\n" + evEndMarker + "\r\ntail\r\n"
	if p := CheckEvidence(doc, r, e, true); len(p) != 1 || !strings.Contains(p[0].Msg, "stale") {
		t.Errorf("stale: %v", p)
	}
	out, ok := WriteEvidence(doc, r, e)
	if !ok || !strings.Contains(out, "\r\ntail\r\n") || strings.Contains(out, "old") {
		t.Fatalf("write: ok=%v\n%s", ok, out)
	}
	if p := CheckEvidence(out, r, e, true); len(p) != 0 {
		t.Errorf("after write: %v", p)
	}
	e.AccTests++
	if p := CheckEvidence(out, r, e, true); len(p) != 1 {
		t.Errorf("drift after a new acceptance test: %v", p)
	}
	if p := CheckEvidence("# x\n", r, e, true); len(p) != 1 || !strings.Contains(p[0].Msg, "no generated evidence block") {
		t.Errorf("missing block: %v", p)
	}
	if p := CheckEvidence("# x\n", r, e, false); len(p) != 0 {
		t.Errorf("missing, not required: %v", p)
	}
	if p := CheckEvidence(evBeginLine+"\n", r, e, true); len(p) != 1 || !strings.Contains(p[0].Msg, "no end marker") {
		t.Errorf("unterminated: %v", p)
	}
	// Counts inside the evidence block are not re-checked as "<n> <kind>" phrases.
	if p := Check(out, r, false); len(p) != 0 {
		t.Errorf("Check over the evidence block: %v", p)
	}
	if _, ok := WriteEvidence("# x\n", r, e); ok {
		t.Error("write without a block: want ok=false")
	}
}

func TestRunEvidence(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "registry_gen.go")
	doc := filepath.Join(dir, "README.md")
	writeFiles(t, dir, map[string]string{
		"registry_gen.go": testRegistry,
		"README.md":       "x\n" + beginLine + "\n" + endMarker + "\n\n" + evBeginLine + "\n" + evEndMarker + "\n",
	})
	src := testSources(t)
	if rc := run(reg, []string{doc}, []string{doc}, []string{doc}, src, false); rc != 1 {
		t.Errorf("stale: rc %d, want 1", rc)
	}
	if rc := run(reg, []string{doc}, []string{doc}, []string{doc}, src, true); rc != 0 {
		t.Errorf("write: rc %d, want 0", rc)
	}
	if rc := run(reg, []string{doc}, []string{doc}, []string{doc}, src, false); rc != 0 {
		t.Errorf("check after write: rc %d, want 0", rc)
	}
	writeFiles(t, src.Examples, map[string]string{"new/new.tests.ps1": ""})
	if rc := run(reg, []string{doc}, []string{doc}, []string{doc}, src, false); rc != 1 {
		t.Errorf("after a new example test: rc %d, want 1", rc)
	}
	src.FieldMap = filepath.Join(dir, "missing.json")
	if rc := run(reg, []string{doc}, []string{doc}, []string{doc}, src, false); rc != 2 {
		t.Errorf("missing fieldmap: rc %d, want 2", rc)
	}
}

// TestRepoSources parses the real repository files (all but pairwise, which
// builds the provider): a renamed file or variable fails here first.
func TestRepoSources(t *testing.T) {
	s := DefaultSources
	for _, p := range []*string{&s.FieldMap, &s.AccDir, &s.ComposeFixtureFile, &s.ComposeExample, &s.Examples} {
		*p = filepath.Join("..", "..", *p)
	}
	s.SkipPairwise = true
	e, err := CollectEvidence(s)
	if err != nil {
		t.Fatal(err)
	}
	if e.Fields.Total == 0 || e.AccTests == 0 || e.ComposeFixtures == 0 || e.ExampleTests == 0 {
		t.Errorf("zero count from the repository: %+v", e)
	}
}
