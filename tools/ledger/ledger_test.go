package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture: a throwaway tree with two units (a reads a.txt, b reads b.txt), a harness file and an
// always-in file; the repo listing falls back to a walk (no git).
func fixture(t *testing.T) (root string, units []Unit, global func() []string) {
	t.Helper()
	root = t.TempDir()
	for f, c := range map[string]string{
		"a.txt":                           "a",
		"b.txt":                           "b",
		"go.mod":                          "module x\n",
		"test/harness/docker-compose.yml": "services: {}\n",
		"test/harness/.ledger/unit.json":  "{}",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	units = []Unit{{Key: "a", Files: []string{"a.txt"}}, {Key: "b", Files: []string{"b.txt"}}}
	global = func() []string {
		r, err := loadRepo(root)
		if err != nil {
			t.Fatal(err)
		}
		return r.globalFiles()
	}
	return root, units, global
}

func write(t *testing.T, root, f, c string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(f)), []byte(c), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runs(ds []Decision) map[string]bool {
	m := map[string]bool{}
	for _, d := range ds {
		m[d.Key] = d.Run
	}
	return m
}

// passAll records a pass for every unit the plan runs.
func passAll(led *Ledger, ds []Decision) {
	var res []Result
	for _, d := range ds {
		if d.Run {
			res = append(res, Result{Key: d.Key, Status: "pass", DurationMs: 5})
		}
	}
	record(led, ds, res, "c0ffee", "2026-11-11T00:00:00Z")
}

func TestUnchangedSkips(t *testing.T) {
	root, units, global := fixture(t)
	led := &Ledger{Gate: "unit"}
	ds := plan(root, units, global(), nil, led, false)
	if r := runs(ds); !r["a"] || !r["b"] {
		t.Fatalf("first run must run everything: %v", r)
	}
	passAll(led, ds)
	ds = plan(root, units, global(), nil, led, false)
	for _, d := range ds {
		if d.Run || d.Reason != "skipped: unchanged since c0ffee" {
			t.Fatalf("%s: want skipped, got run=%v %q", d.Key, d.Run, d.Reason)
		}
	}
}

func TestChangedInputRuns(t *testing.T) {
	root, units, global := fixture(t)
	led := &Ledger{}
	passAll(led, plan(root, units, global(), nil, led, false))
	write(t, root, "a.txt", "a2")
	r := runs(plan(root, units, global(), nil, led, false))
	if !r["a"] || r["b"] {
		t.Fatalf("only a must run: %v", r)
	}
}

func TestCRLFOnlyChangeSkips(t *testing.T) {
	root, units, global := fixture(t)
	write(t, root, "a.txt", "x\ny\n")
	led := &Ledger{}
	passAll(led, plan(root, units, global(), nil, led, false))
	write(t, root, "a.txt", "x\r\ny\r\n")
	if r := runs(plan(root, units, global(), nil, led, false)); r["a"] {
		t.Fatal("a CRLF-only change (autocrlf checkout) must not run the unit")
	}
}

func TestAlwaysInChangeRunsEverything(t *testing.T) {
	root, units, global := fixture(t)
	led := &Ledger{}
	passAll(led, plan(root, units, global(), nil, led, false))
	write(t, root, "go.mod", "module x\n\ngo 1.27\n")
	if r := runs(plan(root, units, global(), nil, led, false)); !r["a"] || !r["b"] {
		t.Fatalf("an always-in change must run every unit: %v", r)
	}
}

func TestHarnessChangeInvalidatesAll(t *testing.T) {
	root, units, global := fixture(t)
	led := &Ledger{}
	passAll(led, plan(root, units, global(), nil, led, false))
	// The ledger itself and git-ignored scratch never count.
	write(t, root, "test/harness/.ledger/unit.json", `{"rows":[]}`)
	if r := runs(plan(root, units, global(), nil, led, false)); r["a"] || r["b"] {
		t.Fatalf("a ledger write must not invalidate: %v", r)
	}
	write(t, root, "test/harness/docker-compose.yml", "services: {x: {}}\n")
	if r := runs(plan(root, units, global(), nil, led, false)); !r["a"] || !r["b"] {
		t.Fatalf("a harness change must run every unit: %v", r)
	}
	// a new file under build/ too
	passAll(led, plan(root, units, global(), nil, led, false))
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "build/x.ps1", "x")
	if r := runs(plan(root, units, global(), nil, led, false)); !r["a"] || !r["b"] {
		t.Fatalf("a build/ change must run every unit: %v", r)
	}
}

func TestEnvChangeRuns(t *testing.T) {
	root, units, global := fixture(t)
	led := &Ledger{}
	passAll(led, plan(root, units, global(), []string{"TPD_ACC_HUB=1"}, led, false))
	if r := runs(plan(root, units, global(), nil, led, false)); !r["a"] {
		t.Fatal("a different acceptance environment must run the unit")
	}
}

func TestFullIgnoresLedger(t *testing.T) {
	root, units, global := fixture(t)
	led := &Ledger{}
	passAll(led, plan(root, units, global(), nil, led, false))
	ds := plan(root, units, global(), nil, led, true)
	for _, d := range ds {
		if !d.Run || !strings.Contains(d.Reason, "full") {
			t.Fatalf("%s: -Full must run it: %v %q", d.Key, d.Run, d.Reason)
		}
	}
}

func TestFailureWritesNoPassRow(t *testing.T) {
	root, units, global := fixture(t)
	led := &Ledger{}
	passAll(led, plan(root, units, global(), nil, led, false))
	write(t, root, "a.txt", "a2")
	ds := plan(root, units, global(), nil, led, false)
	ran, skipped, failed := record(led, ds, []Result{{Key: "a", Status: "fail"}}, "beef", "t")
	if ran != 1 || skipped != 1 || failed != 1 {
		t.Fatalf("counts: ran %d skipped %d failed %d", ran, skipped, failed)
	}
	if r := led.find("a"); r == nil || r.Status != "fail" || r.PassedAt != "" {
		t.Fatalf("a failed unit must have a fail row, not a pass row: %+v", r)
	}
	// It runs again even though its inputs are unchanged since the failure.
	if r := runs(plan(root, units, global(), nil, led, false)); !r["a"] || r["b"] {
		t.Fatalf("a recorded failure must run again: %v", r)
	}
	// A unit planned to run with no result (crash, harness error) loses its row.
	write(t, root, "b.txt", "b2")
	ds = plan(root, units, global(), nil, led, false)
	record(led, ds, nil, "beef", "t")
	if led.find("b") != nil || led.find("a") != nil {
		t.Fatalf("units without a result must have no row: %+v", led.Rows)
	}
}

func TestUnknownInputsAlwaysRun(t *testing.T) {
	root, units, global := fixture(t)
	units[1].Unknown = "runs other programs"
	led := &Ledger{}
	ds := plan(root, units, global(), nil, led, false)
	passAll(led, ds)
	if led.find("b") != nil {
		t.Fatal("an unknown-input unit must never get a pass row")
	}
	ds = plan(root, units, global(), nil, led, false)
	r := runs(ds)
	if r["a"] || !r["b"] {
		t.Fatalf("unknown must run, known unchanged must skip: %v", r)
	}
	for _, d := range ds {
		if d.Key == "b" && !strings.HasPrefix(d.Reason, "inputs unknown:") {
			t.Fatalf("reason: %q", d.Reason)
		}
	}
}

func TestLedgerSortedAndStable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "g.json")
	l := &Ledger{Gate: "g", Rows: []Row{{Key: "z", Status: "pass"}, {Key: "a", Status: "pass"}}}
	if err := l.save(p); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p)
	l2, _ := loadLedger(p, "g")
	if err := l2.save(p); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(p)
	if string(b1) != string(b2) || strings.Index(string(b1), `"a"`) > strings.Index(string(b1), `"z"`) {
		t.Fatalf("ledger not sorted/stable:\n%s", b1)
	}
}

// The provider graph: a type's code enters a test's inputs only through a name in its
// configuration, never through the registry.
func TestProviderGraphBarrier(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"internal/provider/registry_gen.go": "package provider\n\nfunc genResources() []any {\n\treturn []any{\n\t\tNewVolumeResource, // resource docker_volume\n\t\tNewNetworkResource, // resource docker_network\n\t}\n}\n",
		"internal/provider/provider.go":     "package provider\n\ntype p struct{}\n\nfunc New() any { return &p{} }\n\nfunc (x *p) Resources() []any { return genResources() }\n",
		"internal/provider/volume.go":       "package provider\n\nfunc NewVolumeResource() any { return helper() }\n",
		"internal/provider/network.go":      "package provider\n\nfunc NewNetworkResource() any { return nil }\n",
		"internal/provider/helper.go":       "package provider\n\nfunc helper() any { return nil }\n",
		"internal/provider/v_test.go":       "package provider\n\nfunc TestAccVolume(t any) { _ = New; _ = `resource \"docker_volume\" \"x\" {}` }\n\nfunc TestAccAll(t any) { _ = genResources() }\n\nfunc TestAccDyn(t any) { _ = New; _ = \"docker_%s\" }\n",
	}
	for f, c := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		write(t, root, f, c)
	}
	r, err := loadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	g, err := loadProvGraph(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	us := testAccUnits(r, g, nil, nil)
	byKey := map[string]Unit{}
	for _, u := range us {
		byKey[u.Key] = u
	}
	has := func(u Unit, f string) bool {
		for _, x := range u.Files {
			if x == f {
				return true
			}
		}
		return false
	}
	v := byKey["TestAccVolume"]
	if !has(v, "internal/provider/volume.go") || !has(v, "internal/provider/helper.go") || has(v, "internal/provider/network.go") || v.Unknown != "" {
		t.Fatalf("TestAccVolume inputs: %v (unknown %q)", v.Files, v.Unknown)
	}
	if a := byKey["TestAccAll"]; !has(a, "internal/provider/network.go") || !has(a, "internal/provider/volume.go") {
		t.Fatalf("a test walking the registry depends on every type: %v", a.Files)
	}
	if d := byKey["TestAccDyn"]; d.Unknown == "" {
		t.Fatal("a run-time type name must make the unit unknown")
	}
}
