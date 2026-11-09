package provider

// Lane E unit tests: bake definition parsing and the docker_buildx_bake
// action (builds faked through bakeBuild / bakeConnect), plus a check that
// every Lane E data source fills its contract outputs without warnings.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	controlapi "github.com/moby/buildkit/api/services/control"
)

const bakeTestHCL = `
variable "TAG" {
  default = "latest"
}

variable "REPO" {
  default = "reg.io/${NS}"
}

variable "NS" {
  default = "team"
}

variable "PLATFORMS" {
  type    = list(string)
  default = ["linux/amd64"]
}

function "tag" {
  params = [name]
  result = "${REPO}/${name}:${TAG}"
}

group "default" {
  targets = ["app", "matrixed"]
}

target "_base" {
  args = {
    GO = "1.22"
    X  = "base"
  }
  cache-from = ["type=registry,ref=reg.io/cache"]
  platforms  = PLATFORMS
}

target "app" {
  inherits = ["_base"]
  context  = "app"
  args = {
    X = "app"
  }
  tags = [tag("app"), upper("x")]
}

target "matrixed" {
  name   = "m-${v}"
  matrix = {
    v = ["1", "2"]
  }
  dockerfile-inline = "FROM alpine:${v}"
  args = {
    V = v
  }
}
`

func TestBakeParseHCL(t *testing.T) {
	def, err := bakeParse([]bakeFile{{Name: "docker-bake.hcl", Data: []byte(bakeTestHCL)}}, map[string]string{"TAG": "v1", "PLATFORMS": "linux/amd64,linux/arm64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := def.resolve(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tg := range res.Targets {
		names = append(names, tg.Name)
	}
	if strings.Join(names, ",") != "app,m-1,m-2" {
		t.Fatalf("targets = %v", names)
	}
	app := res.Targets[0]
	if *app.Args["X"] != "app" || *app.Args["GO"] != "1.22" || strings.Join(app.Tags, ",") != "reg.io/team/app:v1,X" ||
		strings.Join(app.Platforms, ",") != "linux/amd64,linux/arm64" || *app.Context != "app" || *app.Dockerfile != "Dockerfile" ||
		len(app.CacheFrom) != 1 {
		t.Errorf("app = %+v", app.printMap())
	}
	m2 := res.Targets[2]
	if *m2.DockerfileInline != "FROM alpine:2" || *m2.Args["V"] != "2" || m2.Dockerfile != nil {
		t.Errorf("m-2 = %+v", m2.printMap())
	}
	if g := res.Groups["matrixed"]; strings.Join(g, ",") != "m-1,m-2" {
		t.Errorf("groups = %v", res.Groups)
	}
	var printed map[string]any
	if err := json.Unmarshal(res.printJSON(), &printed); err != nil {
		t.Fatal(err)
	}
	if _, ok := printed["target"].(map[string]any)["app"].(map[string]any)["cache-from"]; !ok {
		t.Errorf("print = %s", res.printJSON())
	}
	hcl := string(res.printHCL())
	if !strings.Contains(hcl, `target "m-1"`) || !strings.Contains(hcl, `group "default"`) {
		t.Errorf("hcl = %s", hcl)
	}
	// The printed HCL parses back to the same targets.
	def2, err := bakeParse([]bakeFile{{Name: "printed.hcl", Data: res.printHCL()}}, nil, nil)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, hcl)
	}
	res2, err := def2.resolve(nil, nil)
	if err != nil || len(res2.Targets) != 3 {
		t.Errorf("reparse: %v %d", err, len(res2.Targets))
	}
}

func TestBakeParseJSONMergeAndSet(t *testing.T) {
	js := `{"variable":{"V":{"default":"1"}},"target":{"app":{"tags":["a:${V}"],"labels":{"a":"1"},"platforms":["linux/amd64"]}}}`
	over := `target "app" {
  labels = { b = "2" }
  tags   = ["b:2"]
  output = ["type=local,dest=out"]
}`
	def, err := bakeParse([]bakeFile{{Name: "docker-bake.json", Data: []byte(js)}, {Name: "override.hcl", Data: []byte(over)}}, map[string]string{"V": "9"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := def.resolve([]string{"app"}, map[string]string{"*.platform": "linux/arm64", "app.args.K": "v", "app.no-cache": "true", "a*.cache-from": "x;y"})
	if err != nil {
		t.Fatal(err)
	}
	app := res.Targets[0]
	if strings.Join(app.Tags, ",") != "b:2" || *app.Labels["a"] != "1" || *app.Labels["b"] != "2" || strings.Join(app.Platforms, ",") != "linux/arm64" ||
		*app.Args["K"] != "v" || app.NoCache == nil || !*app.NoCache || strings.Join(app.CacheFrom, ",") != "x,y" || len(app.Outputs) != 1 {
		t.Errorf("app = %+v", app.printMap())
	}
	// Without the override the JSON variable interpolates.
	def, _ = bakeParse([]bakeFile{{Name: "x.json", Data: []byte(js)}}, nil, nil)
	res, _ = def.resolve(nil, nil) // the only target is the default
	if res == nil || res.Targets[0].Tags[0] != "a:1" {
		t.Errorf("only-target default: %+v", res)
	}
}

func TestBakeParseErrors(t *testing.T) {
	cases := map[string]struct {
		hcl     string
		vars    map[string]string
		targets []string
		set     map[string]string
	}{
		"unknown variable":  {hcl: `target "a" { tags = [NOPE] }`},
		"undeclared":        {hcl: `target "a" {}`, vars: map[string]string{"X": "1"}},
		"inherits cycle":    {hcl: `target "a" { inherits = ["b"] }` + "\n" + `target "b" { inherits = ["a"] }`, targets: []string{"a"}},
		"missing parent":    {hcl: `target "a" { inherits = ["zz"] }`, targets: []string{"a"}},
		"group cycle":       {hcl: `group "g" { targets = ["h"] }` + "\n" + `group "h" { targets = ["g"] }`, targets: []string{"g"}},
		"unknown target":    {hcl: `target "a" {}` + "\n" + `target "b" {}`, targets: []string{"c"}},
		"no default":        {hcl: `target "a" {}` + "\n" + `target "b" {}`},
		"bad attribute":     {hcl: `target "a" { colour = "red" }`},
		"name w/o matrix":   {hcl: `target "a" { name = "b" }`},
		"duplicate matrix":  {hcl: `target "a" { name = "x" ` + "\n" + `matrix = { v = ["1", "2"] } }`},
		"bad matrix name":   {hcl: `target "a" { name = "x y ${v}"` + "\n" + `matrix = { v = ["1"] } }`},
		"typed override":    {hcl: `variable "N" { type = number }` + "\n" + `target "a" {}`, vars: map[string]string{"N": "abc"}},
		"syntax":            {hcl: `target "a" {`},
		"set without attr":  {hcl: `target "a" {}`, set: map[string]string{"a": "x"}},
		"set args w/o key":  {hcl: `target "a" {}`, set: map[string]string{"a.args": "x"}},
		"bad bool":          {hcl: `target "a" { pull = "maybe" }`},
		"matrix not a list": {hcl: `target "a" { name = "a-${v}"` + "\n" + `matrix = { v = "1" } }`},
	}
	for name, c := range cases {
		def, err := bakeParse([]bakeFile{{Name: "b.hcl", Data: []byte(c.hcl)}}, c.vars, nil)
		if err == nil {
			_, err = def.resolve(c.targets, c.set)
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestBakeOverrideValues(t *testing.T) {
	hcl := `variable "L" { type = list(number) }
variable "M" { type = map(string) }
variable "B" { type = bool }
variable "S" {}
target "a" {
  args = { L = join(",", L), M = M.k, B = B ? "yes" : "no", S = S }
}`
	def, err := bakeParse([]bakeFile{{Name: "b.hcl", Data: []byte(hcl)}}, map[string]string{"L": "1,2", "M_JSON": `{"k":"v"}`, "B": "true", "S": "plain"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := def.resolve([]string{"a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := res.Targets[0].Args
	if *a["L"] != "1,2" || *a["M"] != "v" || *a["B"] != "yes" || *a["S"] != "plain" {
		t.Errorf("args = %v %v %v %v", *a["L"], *a["M"], *a["B"], *a["S"])
	}
	for _, f := range []string{"sha256", "md5", "base64encode", "basename", "sanitize", "urlencode", "try", "upper"} {
		if _, ok := bakeFunctions()[f]; !ok {
			t.Errorf("function %s missing", f)
		}
	}
}

func TestBakeRequest(t *testing.T) {
	wd := t.TempDir()
	ctx := "app"
	df := "build/Dockerfile"
	shm := "64m"
	yes := true
	tg := &bakeTarget{Name: "app", Context: &ctx, Dockerfile: &df, ShmSize: &shm, Pull: &yes,
		Args: map[string]*string{"A": &ctx, "N": nil}, Tags: []string{"a:1"},
		Secrets: []string{"id=tok,src=tok.txt", "type=env,id=gh,env=GH_TOKEN", "id=plain"}, SSH: []string{"default", "key=~/.ssh/id,k2"},
		Outputs: []string{"type=local,dest=out", "outdir", "type=registry"}, Contexts: map[string]string{"base": "docker-image://alpine", "src": "../src"},
		Attest: []string{"type=sbom"}, Annotations: []string{"k=v"}, Entitlements: []string{"network.host"}}
	req, extra, err := bakeRequest(tg, wd, bakeGlobals{push: true, provenance: "false", noCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if req.ContextDir != filepath.Join(wd, "app") || req.Dockerfile != df || req.ShmSize != 64<<20 || !req.Pull || !req.NoCache || !req.Outputs.Push ||
		req.BuildArgs["A"] != "app" || len(req.BuildArgs) != 1 {
		t.Errorf("req = %+v", req)
	}
	if req.Secrets[0].Src != filepath.Join(wd, "tok.txt") || req.Secrets[1].Env != "GH_TOKEN" || req.Secrets[2].Env != "plain" {
		t.Errorf("secrets = %+v", req.Secrets)
	}
	if req.SSH[0].ID != "default" || len(req.SSH[1].Paths) != 2 {
		t.Errorf("ssh = %+v", req.SSH)
	}
	if req.Outputs.Raw[0] != "type=local,dest="+filepath.Join(wd, "out") || req.Outputs.Raw[1] != "type=local,dest="+filepath.Join(wd, "outdir") {
		t.Errorf("outputs = %v", req.Outputs.Raw)
	}
	if extra.Contexts["src"] != filepath.Join(wd, "../src") || extra.Contexts["base"] != "docker-image://alpine" || extra.Annotations[0] != "k=v" {
		t.Errorf("extra = %+v", extra)
	}
	if !strings.Contains(strings.Join(req.Attestations.Extra, " "), "type=provenance,disabled=true") {
		t.Errorf("attest = %v", req.Attestations.Extra)
	}
	bad := "1q"
	if _, _, err := bakeRequest(&bakeTarget{Name: "x", ShmSize: &bad}, wd, bakeGlobals{}); err == nil {
		t.Error("bad shm-size accepted")
	}
	if _, _, err := bakeRequest(&bakeTarget{Name: "x", Secrets: []string{"type=vault,id=a"}}, wd, bakeGlobals{}); err == nil {
		t.Error("bad secret type accepted")
	}
}

func TestBuildxBakeAction(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker-bake.hcl"), []byte(bakeTestHCL), 0o600)
	var built []string
	oldB, oldC := bakeBuild, bakeConnect
	t.Cleanup(func() { bakeBuild, bakeConnect = oldB, oldC })
	bakeConnect = func(_ context.Context, _ *providerData, ref seamBuilderRef) (*bkConn, error) {
		return &bkConn{Node: bkNode{Builder: ref.Name, Name: ref.Name + "0"}}, nil
	}
	bakeBuild = func(_ context.Context, _ *providerData, conn *bkConn, req seamBuildRequest, _ *bkExtra) (*seamBuildResult, error) {
		built = append(built, strings.Join(req.Outputs.Names, "+"))
		if strings.Contains(req.DockerfileInline, "alpine:2") {
			return nil, fmt.Errorf("simulated failure")
		}
		return &seamBuildResult{ImageID: "sha256:cfg", Digest: "sha256:dig", Platforms: req.Platforms,
			Metadata: map[string]any{"containerimage.digest": "sha256:dig", "buildx.build.ref": conn.Node.Builder + "/" + conn.Node.Name + "/r1"}}, nil
	}
	a := NewBuildxBakeAction()
	resultPath := filepath.Join(dir, "out", "result.json")
	res := actInvokePD(t, pd, a, map[string]tftypes.Value{
		"working_dir": p3eStr(dir), "targets": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("app")}),
		"builder": p3eStr("ci"), "push": tftypes.NewValue(tftypes.Bool, true), "result_path": p3eStr(resultPath),
		"variables": p3eStrMap(map[string]string{"TAG": "v2"}),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	b, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	json.Unmarshal(b, &obj)
	if fmt.Sprint(obj["targets"]) != "[app]" || !strings.Contains(string(b), "reg.io/team/app@sha256:dig") || !strings.Contains(string(b), `"ci/ci0/r1"`) ||
		obj["bake_json_sha256"] == nil || !strings.Contains(fmt.Sprint(obj["bake_hcl"]), `target "app"`) || obj["buildx_version"] != contractBuildxVersion {
		t.Errorf("result = %s", b)
	}
	if strings.Join(built, ",") != "reg.io/team/app:v2+X" {
		t.Errorf("built = %v", built)
	}

	// The default group: m-2 fails, so the action fails and names it.
	res = actInvokePD(t, pd, a, map[string]tftypes.Value{"working_dir": p3eStr(dir)})
	if !res.Diags.HasError() || !strings.Contains(res.errText(), `target "m-2"`) {
		t.Errorf("failure: %s", res.errText())
	}
	// Errors before building.
	for name, cfg := range map[string]map[string]tftypes.Value{
		"no definition":  {"working_dir": p3eStr(t.TempDir())},
		"bad content":    {"content": p3eStr(`target "a" {`)},
		"unknown target": {"content": p3eStr(`target "a" {}`), "targets": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("zz")})},
		"missing file":   {"files": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr(filepath.Join(dir, "nope.hcl"))})},
	} {
		if r := actInvokePD(t, pd, a, cfg); !r.Diags.HasError() {
			t.Errorf("%s: no error", name)
		}
	}
	if d := actValidate(t, a, map[string]tftypes.Value{"set": p3eStrMap(map[string]string{"nodot": "x"}), "content": p3eStr("  ")}); d.ErrorsCount() != 2 {
		t.Errorf("validate: %v", d)
	}
}

func TestBuildxContractNoWarnings(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	bk, _ := newBKFake(t)
	bk.records = bkTestRecords()
	for _, c := range []struct {
		d   datasource.DataSource
		cfg map[string]tftypes.Value
	}{
		{NewBuildxBuilderDataSource(), map[string]tftypes.Value{"name": p3eStr("ci")}},
		{NewBuildxBuildersDataSource(), nil},
		{NewBuildxDiskUsageDataSource(), nil},
		{NewBuildxHistoryDataSource(), map[string]tftypes.Value{"builder": p3eStr("ci")}},
		{NewBuildxHistoryRecordsDataSource(), map[string]tftypes.Value{"builder": p3eStr("ci")}},
	} {
		if _, diags := p3eReadDS(t, c.d, pd, c.cfg); diags.HasError() || diags.WarningsCount() > 0 {
			t.Errorf("%T: %v", c.d, diags)
		}
	}
	_ = controlapi.BuildHistoryEventType_COMPLETE
}

// ---------------------------------------------------------------------------
// Acceptance tests (dind harness). None needs a swarm manager.

func TestAccBuildxDataSources_default(t *testing.T) {
	testAccPreCheck(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "docker_buildx_builder" "d" {}
data "docker_buildx_builders" "all" {}
data "docker_buildx_disk_usage" "du" {}
data "docker_buildx_history_records" "h" {
  limit = 5
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.docker_buildx_builder.d", "driver", "docker"),
				resource.TestCheckResourceAttrSet("data.docker_buildx_builder.d", "buildkit_version"),
				resource.TestCheckResourceAttrSet("data.docker_buildx_builder.d", "platforms.0"),
				resource.TestCheckResourceAttr("data.docker_buildx_builders.all", "names.0", "default"),
				resource.TestCheckResourceAttrSet("data.docker_buildx_disk_usage.du", "summary.total"),
				resource.TestCheckResourceAttrSet("data.docker_buildx_history_records.h", "content_sha256"),
			),
		}},
	})
}

func TestAccBuildxBake_basic(t *testing.T) {
	testAccPreCheck(t)
	dir := t.TempDir()
	out := filepath.ToSlash(filepath.Join(dir, "result.json"))
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   actAccVersionChecks,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "terraform_data" "t" {
  input = "1"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_buildx_bake.b]
    }
  }
}

action "docker_buildx_bake" "b" {
  config {
    content = <<-EOT
      target "default" {
        dockerfile-inline = "FROM %s\nRUN echo baked > /baked"
        tags              = ["tfacc-bake:latest"]
      }
    EOT
    load        = true
    result_path = %q
  }
}
`, actAccImage, out),
			Check: func(*terraform.State) error {
				b, err := os.ReadFile(out)
				if err != nil {
					return err
				}
				if !strings.Contains(string(b), "tfacc-bake:latest") {
					return fmt.Errorf("result: %s", b)
				}
				return nil
			},
		}, {
			// The bake run left a build record on the default builder.
			Config: `
data "docker_buildx_history" "last" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.docker_buildx_history.last", "ref"),
				resource.TestCheckResourceAttr("data.docker_buildx_history.last", "record.status", "Completed"),
			),
		}},
	})
}
