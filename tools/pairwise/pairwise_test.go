package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// ---- fixtures ----

// testSchema is a small resource schema: plain attributes, a single block
// and a list block.
func testSchema() *resourceSchema {
	attr := func(name string, t tftypes.Type) *schemaNode {
		return &schemaNode{Name: name, Kind: kindAttr, Type: t, Optional: true}
	}
	return &resourceSchema{Type: "docker_thing", Children: map[string]*schemaNode{
		"id":     {Name: "id", Kind: kindAttr, Type: tftypes.String, Computed: true},
		"name":   {Name: "name", Kind: kindAttr, Type: tftypes.String, Required: true},
		"a":      attr("a", tftypes.String),
		"b":      attr("b", tftypes.Number),
		"c":      attr("c", tftypes.Bool),
		"d":      attr("d", tftypes.String),
		"e":      attr("e", tftypes.String),
		"labels": attr("labels", tftypes.Map{ElementType: tftypes.String}),
		"hc": {Name: "hc", Kind: kindBlock, Nesting: nestSingle, Children: map[string]*schemaNode{
			"interval": attr("interval", tftypes.String),
			"test":     attr("test", tftypes.List{ElementType: tftypes.String}),
		}},
		"ports": {Name: "ports", Kind: kindBlock, Nesting: nestList, Children: map[string]*schemaNode{
			"internal": {Name: "internal", Kind: kindAttr, Type: tftypes.Number, Required: true},
			"external": attr("external", tftypes.Number),
		}},
	}}
}

const testInventory = `{"items": [{"id": "engine:thing", "outputs": [
  {"name": "inspect", "category": "structured", "fields": [
    {"name": "id", "upstream": "Id"},
    {"name": "config", "upstream": "Config", "fields": [
      {"name": "labels", "upstream": "Labels"},
      {"name": "healthcheck", "upstream": "Healthcheck"}
    ]},
    {"name": "host_config", "upstream": "HostConfig", "fields": [
      {"name": "memory", "upstream": "Memory"},
      {"name": "port_bindings", "upstream": "PortBindings"}
    ]}
  ]}]}]}`

func testContract(t *testing.T) *contract {
	t.Helper()
	c, err := parseContract([]byte(testInventory))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func compileJSON(t *testing.T, js string) (*compiledModel, error) {
	t.Helper()
	m, err := parseModel([]byte(js), "test")
	if err != nil {
		return nil, err
	}
	return compileModel(m, testSchema(), testContract(t))
}

func mustCompile(t *testing.T, js string) *compiledModel {
	t.Helper()
	cm, err := compileJSON(t, js)
	if err != nil {
		t.Fatal(err)
	}
	return cm
}

const inspectHdr = `"type": "docker_thing", "seed": 7, "inspect": {"item": "engine:thing", "endpoint": "/things/{id}", "id_attr": "id"}`

func noInspect(vals string) string {
	return `{"match": "none", "no_inspect_reason": "test", "values": ` + vals + `}`
}

// checkCovering fails unless every feasible pair is in some row and no row
// violates a constraint.
func checkCovering(t *testing.T, cm *compiledModel, rows [][]int) {
	t.Helper()
	n := len(cm.Factors)
	for _, r := range rows {
		if cm.violates(r) {
			t.Errorf("row %v violates a constraint", cm.labels(r))
		}
	}
	for fi := 0; fi < n; fi++ {
		for fj := fi + 1; fj < n; fj++ {
			for vi := range cm.Factors[fi].Values {
				for vj := range cm.Factors[fj].Values {
					probe := emptyRow(n)
					probe[fi], probe[fj] = vi, vj
					feasible := cm.completable(probe)
					covered := false
					for _, r := range rows {
						if r[fi] == vi && r[fj] == vj {
							covered = true
							break
						}
					}
					if feasible && !covered {
						t.Errorf("pair %s=%s, %s=%s not covered", cm.Factors[fi].Path, cm.Factors[fi].Values[vi].Label, cm.Factors[fj].Path, cm.Factors[fj].Values[vj].Label)
					}
					if !feasible && covered {
						t.Errorf("infeasible pair %s=%s, %s=%s covered", cm.Factors[fi].Path, cm.Factors[fi].Values[vi].Label, cm.Factors[fj].Path, cm.Factors[fj].Values[vj].Label)
					}
				}
			}
		}
	}
}

// ---- covering array ----

func TestCoverSmallModelSizes(t *testing.T) {
	cases := []struct {
		name  string
		attrs string
		want  int
	}{
		// 3x3: every combination is a pair, exactly 9 rows.
		{"two factors 3x3", `"a": ` + noInspect(`["x","y","z"]`) + `, "b": ` + noInspect(`[1,2,3]`), 9},
		// 2^3: the optimal pairwise array has 4 rows.
		{"three booleans", `"c": ` + noInspect(`[true,false]`) + `, "d": ` + noInspect(`["p","q"]`) + `, "e": ` + noInspect(`["r","s"]`), 4},
		// 3 x 2 x 2: lower bound 6 (3x2), reached.
		{"3x2x2", `"a": ` + noInspect(`["x","y","z"]`) + `, "c": ` + noInspect(`[true,false]`) + `, "d": ` + noInspect(`["p","q"]`), 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cm := mustCompile(t, `{`+inspectHdr+`, "attributes": {`+tc.attrs+`}}`)
			rows, st, err := cover(cm)
			if err != nil {
				t.Fatal(err)
			}
			checkCovering(t, cm, rows)
			if len(rows) != tc.want {
				t.Errorf("rows = %d, want %d (pairs %d)", len(rows), tc.want, st.Pairs)
			}
		})
	}
}

func TestCoverConstraints(t *testing.T) {
	js := `{` + inspectHdr + `, "attributes": {
	  "a": ` + noInspect(`["x","y","z"]`) + `,
	  "b": ` + noInspect(`[null,1,2]`) + `,
	  "c": ` + noInspect(`[true,false]`) + `,
	  "d": ` + noInspect(`["p","q"]`) + `
	}, "constraints": [
	  {"when": {"a": ["x"]}, "forbid": {"b": [1, 2]}, "reason": "x takes no b", "daemon_error": "conflicting options: x and b"},
	  {"when": {"c": [true]}, "requires": {"d": ["q"]}, "reason": "c needs q"}
	]}`
	cm := mustCompile(t, js)
	if len(cm.Clauses) != 2 {
		t.Fatalf("clauses = %d", len(cm.Clauses))
	}
	rows, st, err := cover(cm)
	if err != nil {
		t.Fatal(err)
	}
	// a=x with b=1, a=x with b=2, c=true with d=p.
	if st.Excluded != 3 {
		t.Errorf("excluded = %d, want 3", st.Excluded)
	}
	checkCovering(t, cm, rows)
	for _, r := range rows {
		l := cm.labelMap(r)
		if l["a"] == "x" && l["b"] != "unset" {
			t.Errorf("forbid violated: %v", l)
		}
		if l["c"] == "true" && l["d"] != "q" {
			t.Errorf("requires violated: %v", l)
		}
	}
}

func TestCoverAllConstrainedAway(t *testing.T) {
	js := `{` + inspectHdr + `, "attributes": {"c": ` + noInspect(`[true,false]`) + `, "d": ` + noInspect(`["p","q"]`) + `},
	  "constraints": [{"when": {"c": [true, false]}, "forbid": {"d": ["p", "q"]}, "reason": "nothing works"}]}`
	cm := mustCompile(t, js)
	if _, _, err := cover(cm); err == nil || !strings.Contains(err.Error(), "exclude every configuration") {
		t.Fatalf("err = %v", err)
	}
}

// ---- model validation against the schema and the contract ----

func TestModelErrors(t *testing.T) {
	cases := []struct {
		name, attrs, extra, want string
	}{
		{"unknown attribute", `"nope": ` + noInspect(`[1,2]`), "", `docker_thing has no attribute or block "nope"`},
		{"unknown nested path", `"hc.nope": ` + noInspect(`["1","2"]`), "", `docker_thing.hc has no attribute or block "nope"`},
		{"unknown key in block value", `"hc": ` + noInspect(`[null, {"interval": "1s", "bogus": 1}]`), "", `no attribute or block "bogus"`},
		{"computed only", `"id": ` + noInspect(`["1","2"]`), "", "computed only"},
		{"wrong type", `"b": ` + noInspect(`[1, "two"]`), "", "want a number"},
		{"wrong list element", `"hc.test": ` + noInspect(`[null, ["CMD", 1]]`), "", "want a string"},
		{"one value", `"a": ` + noInspect(`["x"]`), "", "at least 2 values"},
		{"unknown inspect field", `"b": {"inspect": "host_config.nope", "values": [1, 2]}`, "", `the contract has no field "nope"`},
		{"inspect missing", `"b": {"values": [1, 2]}`, "", "needs an inspect path"},
		{"none without reason", `"b": {"match": "none", "values": [1, 2]}`, "", "needs no_inspect_reason"},
		{"bad match", `"b": {"inspect": "host_config.memory", "match": "fuzzy", "values": [1, 2]}`, "", "unknown match"},
		{"constraint unknown value", `"a": ` + noInspect(`["x","y"]`) + `, "b": ` + noInspect(`[1,2]`), `, "constraints": [{"when": {"a": ["w"]}, "forbid": {"b": [1]}, "reason": "r"}]`, `a has no value "w"`},
		{"constraint unknown factor", `"a": ` + noInspect(`["x","y"]`), `, "constraints": [{"when": {"z": ["x"]}, "forbid": {"a": ["x"]}, "reason": "r"}]`, `"z" is not a factor`},
		{"constraint without reason", `"a": ` + noInspect(`["x","y"]`) + `, "b": ` + noInspect(`[1,2]`), `, "constraints": [{"when": {"a": ["x"]}, "forbid": {"b": [1]}}]`, "no reason"},
		{"overlapping factors", `"hc": ` + noInspect(`[null, {"interval": "1s"}]`) + `, "hc.interval": ` + noInspect(`["1s","2s"]`), "", "overlap"},
		{"duplicate label", `"a": ` + noInspect(`["x", {"$set": "y", "$label": "x"}]`), "", "duplicate value label"},
		{"unknown value key", `"a": ` + noInspect(`["x", {"$sett": "y"}]`), "", "unknown value key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileJSON(t, `{`+inspectHdr+`, "attributes": {`+tc.attrs+`}`+tc.extra+`}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if _, err := parseModel([]byte(`{`+inspectHdr+`, "attributes": {"a": `+noInspect(`["x","y"]`)+`}, "extra": 1}`), "t"); err == nil {
		t.Error("an unknown top-level model key must fail")
	}
}

func TestResolveInspect(t *testing.T) {
	fields, err := testContract(t).fields("engine:thing", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	up, err := resolveInspect(fields, "host_config.memory")
	if err != nil || strings.Join(up, ".") != "HostConfig.Memory" {
		t.Fatalf("up = %v, %v", up, err)
	}
	// Below the listed levels: matched by normalized name at compare time.
	up, err = resolveInspect(fields, "config.healthcheck.start_period")
	if err != nil || strings.Join(up, ".") != "Config.Healthcheck.?start_period" {
		t.Fatalf("up = %v, %v", up, err)
	}
	doc := map[string]any{"Config": map[string]any{"Healthcheck": map[string]any{"StartPeriod": 2.0}}}
	if v, ok := lookupUpstream(doc, up); !ok || v != 2.0 {
		t.Fatalf("lookup = %v %v", v, ok)
	}
}

// ---- rendering ----

func TestRenderConfig(t *testing.T) {
	js := `{` + inspectHdr + `, "fixed": {"name": {"$hcl": "local.prefix"}},
	  "prelude": "resource \"terraform_data\" \"aux\" {}",
	  "attributes": {
	    "a": ` + noInspect(`["has \"quotes\" and ${x}", "plain"]`) + `,
	    "hc.interval": ` + noInspect(`["10s", "20s"]`) + `,
	    "ports": ` + noInspect(`[null, [{"internal": 80, "external": 8080}, {"internal": 53}]]`) + `,
	    "labels": ` + noInspect(`[null, {"k.1": "v"}]`) + `
	  }}`
	cm := mustCompile(t, js)
	row := []int{0, 0, 1, 1} // factors sorted: a, hc.interval, labels, ports
	b, err := renderConfig(cm, row, "pw-thing-01", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`source = "JerryBalmer1/docker"`,
		`provider "docker" {}`,
		`prefix = "pw-thing-01"`,
		`resource "terraform_data" "aux" {}`,
		`resource "docker_thing" "pw" {`,
		`a      = "has \"quotes\" and $${x}"`,
		`name   = local.prefix`,
		"  hc {\n    interval = \"10s\"\n  }",
		"  ports {\n    external = 8080\n    internal = 80\n  }\n\n  ports {\n    internal = 53\n  }",
		`"k.1" = "v"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config lacks %q:\n%s", want, got)
		}
	}
}

// ---- the real models ----

func repoPath(p ...string) string { return filepath.Join(append([]string{"..", ".."}, p...)...) }

func loadRealModels(t *testing.T) map[string]*compiledModel {
	t.Helper()
	ss, err := loadProviderSchemas(context.Background(), defaultProvider, allTypes)
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadContract(repoPath("reference", "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*compiledModel{}
	for _, typ := range allTypes {
		m, err := loadModel(repoPath("reference", "_gen", "pairwise", typ+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if m.Type != typ {
			t.Fatalf("%s.json has type %s", typ, m.Type)
		}
		cm, err := compileModel(m, ss[typ], c)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		out[typ] = cm
	}
	return out
}

func TestRealModelsCoverEveryPair(t *testing.T) {
	for typ, cm := range loadRealModels(t) {
		t.Run(typ, func(t *testing.T) {
			if n := len(cm.Factors); n < 4 || n > 15 {
				t.Errorf("%d attributes; keep the model between 4 and 15", n)
			}
			rows, st, err := cover(cm)
			if err != nil {
				t.Fatal(err)
			}
			checkCovering(t, cm, rows)
			t.Logf("%s: %d attributes, %d pairs (%d excluded), %d configs", typ, len(cm.Factors), st.Pairs, st.Excluded, len(rows))
		})
	}
}

func TestGenerateDeterministic(t *testing.T) {
	models := loadRealModels(t)
	gen := func(dir string) map[string][]byte {
		for _, typ := range allTypes {
			rows, _, err := cover(models[typ])
			if err != nil {
				t.Fatal(err)
			}
			if err := writeConfigs(dir, models[typ], rows); err != nil {
				t.Fatal(err)
			}
		}
		files := map[string][]byte{}
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				b, _ := os.ReadFile(p)
				rel, _ := filepath.Rel(dir, p)
				files[rel] = b
			}
			return nil
		})
		return files
	}
	a, b := gen(t.TempDir()), gen(t.TempDir())
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("file counts %d vs %d", len(a), len(b))
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			t.Errorf("%s differs between two runs", k)
		}
	}
	if _, ok := a[filepath.Join("docker_container", "01", "main.tf")]; !ok {
		t.Error("no docker_container/01/main.tf")
	}
}

// ---- comparison ----

func TestCheckAll(t *testing.T) {
	doc := map[string]any{
		"HostConfig": map[string]any{
			"Memory":       67108864.0,
			"Init":         nil,
			"PortBindings": map[string]any{"80/tcp": []any{map[string]any{"HostIp": "", "HostPort": "18080"}}},
		},
		"Config": map[string]any{"Labels": map[string]any{"a": "1", "owner": "tf"}, "Env": []any{"PATH=/bin", "A=1"}},
	}
	checks := []expectCheck{
		{Attr: "memory", Upstream: []string{"HostConfig", "Memory"}, Match: "equal", Expected: 67108864.0},
		{Attr: "labels", Upstream: []string{"Config", "Labels"}, Match: "subset", Expected: map[string]any{"a": "1"}},
		{Attr: "env", Upstream: []string{"Config", "Env"}, Match: "subset", Expected: []any{"A=1"}},
		{Attr: "ports", Upstream: []string{"HostConfig", "PortBindings"}, Match: "subset", Expected: map[string]any{"80/tcp": []any{map[string]any{"HostPort": "18080"}}}},
		{Attr: "init", Upstream: []string{"HostConfig", "Init"}, Match: "equal", Expected: false},
		// mismatches:
		{Attr: "memory2", Label: "128m", Upstream: []string{"HostConfig", "Memory"}, Match: "equal", Expected: 134217728.0},
		{Attr: "labels2", Upstream: []string{"Config", "Labels"}, Match: "subset", Expected: map[string]any{"b": "2"}},
		{Attr: "absent", Upstream: []string{"Config", "User"}, Match: "equal", Expected: "nobody"},
	}
	mm := checkAll(checks, doc)
	var got []string
	for _, m := range mm {
		got = append(got, m.Attr+"="+m.Observed)
	}
	want := []string{"memory2=67108864", `labels2={"a":"1","owner":"tf"}`, "absent=<absent>"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("mismatches = %v, want %v", got, want)
	}
}

func TestCompareDirAgainstFakeEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/abc123/json" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"Id": "abc123", "HostConfig": {"Memory": 67108864, "CpuShares": 0}}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	e := expectFile{Type: "docker_container", Config: "pw-container-01", Address: "docker_container.pw", Endpoint: "/containers/{id}/json", IDAttr: "id",
		Checks: []expectCheck{
			{Attr: "memory", Upstream: []string{"HostConfig", "Memory"}, Match: "equal", Expected: 67108864.0},
			{Attr: "cpu_shares", Label: "512", Upstream: []string{"HostConfig", "CpuShares"}, Match: "equal", Expected: 512.0},
		}}
	if err := writeJSON(filepath.Join(dir, "expect.json"), e); err != nil {
		t.Fatal(err)
	}
	state := `{"values": {"root_module": {"resources": [{"address": "docker_container.pw", "values": {"id": "abc123"}}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := compareDir(context.Background(), dir, "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "abc123" || res.Checked != 2 || len(res.Mismatches) != 1 || res.Mismatches[0].Attr != "cpu_shares" {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "compare.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := compareDir(context.Background(), dir, "unix:///var/run/docker.sock"); err == nil {
		t.Fatal("a non-tcp DOCKER_HOST must fail")
	}
}

func TestSummarizePlan(t *testing.T) {
	plan := `{"resource_changes": [
	  {"address": "docker_container.pw", "change": {"actions": ["update"],
	    "before": {"name": "x", "memory": 1, "labels": {"a": "1"}},
	    "after": {"name": "x", "memory": 2, "labels": {"a": "1", "b": "2"}},
	    "after_unknown": {"inspect": true}}},
	  {"address": "docker_network.aux", "change": {"actions": ["no-op"], "before": {}, "after": {}}},
	  {"address": "docker_volume.v", "change": {"actions": ["delete", "create"], "before": {"driver": "local"}, "after": {"driver": "x"},
	    "replace_paths": [["driver"]]}}
	]}`
	var p any
	if err := json.Unmarshal([]byte(plan), &p); err != nil {
		t.Fatal(err)
	}
	ps := summarizePlan(p)
	if ps.Empty || len(ps.Changes) != 2 {
		t.Fatalf("ps = %+v", ps)
	}
	if got := strings.Join(ps.Changes[0].Paths, ","); got != "inspect (known after apply),labels.b,memory" {
		t.Errorf("paths = %s", got)
	}
	if got := strings.Join(ps.Changes[1].Paths, ","); got != "(replace) driver,driver" {
		t.Errorf("paths = %s", got)
	}
	if !summarizePlan(map[string]any{}).Empty {
		t.Error("no resource changes must be empty")
	}
}

func TestReport(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, run runFile, cmp *compareResult, ps *planSummary) {
		d := filepath.Join(dir, "docker_volume", name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = writeJSON(filepath.Join(d, "expect.json"), expectFile{Type: "docker_volume", Config: "pw-volume-" + name, Assignment: map[string]string{"driver": "local"}})
		_ = writeJSON(filepath.Join(d, "run.json"), run)
		if cmp != nil {
			_ = writeJSON(filepath.Join(d, "compare.json"), cmp)
		}
		if ps != nil {
			_ = writeJSON(filepath.Join(d, "plan-summary.json"), ps)
		}
	}
	mk("01", runFile{Applied: true, Destroyed: true}, &compareResult{Mismatches: []mismatch{}}, nil)
	mk("02", runFile{Applied: true, PlanExitCode: 2, Destroyed: true}, &compareResult{Mismatches: []mismatch{{Attr: "labels", Expected: "a", Observed: "b"}}},
		&planSummary{Changes: []planChange{{Address: "docker_volume.pw", Actions: []string{"update"}, Paths: []string{"labels.a"}}}})
	mk("03", runFile{Applied: false, ApplyError: "Error: boom\nmore", Destroyed: true}, nil, nil)
	rep, err := buildReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	tot := rep.Totals["docker_volume"]
	if tot.Configs != 3 || tot.Applied != 2 || tot.Clean != 1 || tot.Findings != 2 || tot.Destroyed != 3 {
		t.Fatalf("totals = %+v", tot)
	}
	if f := rep.Configs[1].Findings; len(f) != 2 || !strings.Contains(f[0], "inspect mismatch labels") || !strings.Contains(f[1], "non-empty second plan: docker_volume.pw update: labels.a") {
		t.Errorf("findings 02 = %v", f)
	}
	if f := rep.Configs[2].Findings; len(f) != 1 || f[0] != "apply failed: Error: boom | more" {
		t.Errorf("findings 03 = %v", f)
	}
	lines := rep.summaryLines()
	if !strings.HasPrefix(lines[0], "SUMMARY docker_volume: configs 3, applied 2, clean 1, findings 2") {
		t.Errorf("summary = %v", lines)
	}
}

func TestHCLString(t *testing.T) {
	for in, want := range map[string]string{
		`a`:         `"a"`,
		`a"b`:       `"a\"b"`,
		`${x} %{y}`: `"$${x} %%{y}"`,
		`$x 100%`:   `"$x 100%"`,
		"l1\nl2\\":  `"l1\nl2\\"`,
	} {
		if got := hclString(in); got != want {
			t.Errorf("hclString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRunModes(t *testing.T) {
	if err := run([]string{}); err == nil {
		t.Error("no mode must fail")
	}
	if err := run([]string{"-gen", "-compare"}); err == nil {
		t.Error("two modes must fail")
	}
	if err := run([]string{"-gen"}); err == nil {
		t.Error("-gen without -out must fail")
	}
}
