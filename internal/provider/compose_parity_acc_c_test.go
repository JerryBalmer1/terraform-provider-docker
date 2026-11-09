package provider

// TestAccComposeParity brings every compose fixture up twice on the
// harness daemon, once with the real `docker compose` CLI and once with
// docker_compose (local mode), and diffs what each created: containers,
// networks, volumes, their labels and com.docker.compose.config-hash
// values. Any difference fails.
//
// The provider never shells out; this TEST runs the CLI (os/exec in this
// _test.go file only) against DOCKER_HOST from test/harness/.env. It needs
// TF_ACC, DOCKER_HOST and TF_ACC_COMPOSE_CLI (the docker binary with the
// compose v2 plugin; harness-up writes it) and skips without them.
//
// Each sub-case first gets what it needs on the harness daemon
// (composeParityOverrides in p_compose_parity_test.go: fictional images,
// external networks / volumes, host files), and a fixture that cannot run on
// one Linux daemon at all (Windows client paths as bind sources, replicas
// sharing a host port or a pinned IP) runs as its replacement under
// test/fixtures/compose. Leftovers of the project are removed before and
// after each side. A fixture the CLI still cannot bring up is skipped with
// the CLI's error: there is no reference to compare against.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/client"
	"github.com/zclconf/go-cty/cty"
)

// composeParityIgnoredLabels differ by construction, not by behaviour: the
// CLI stamps its own version, the provider its ownership and spec digest.
var composeParityIgnoredLabels = []string{
	"com.docker.compose.version",
	labelManagedBy,
	composeLabelSpecHash,
}

func composeParityIgnored(k string) bool {
	for _, p := range composeParityIgnoredLabels {
		if k == p {
			return true
		}
	}
	return false
}

// composeParityCase is one project to compare.
type composeParityCase struct {
	Name     string
	Project  string
	Content  string
	Env      map[string]string
	Profiles []string
	Files    map[string]string
	Source   string              // the fixture file, for the log
	Setup    *composeParitySetup // created on the daemon first
}

// composeParityCases returns the testdata/compose_hash fixtures plus the
// local docker_compose projects of examples/resources/docker_compose.
func composeParityCases(t *testing.T) []composeParityCase {
	t.Helper()
	var out []composeParityCase
	for _, name := range composeSortedKeys(composeHashCompatFixtures) {
		fx := composeHashCompatFixtures[name]
		b, err := os.ReadFile(filepath.Join("testdata", "compose_hash", fx.File))
		if err != nil {
			t.Fatal(err)
		}
		project := fx.Project
		if project == "" {
			project = "parity" + strings.ReplaceAll(name, "_", "")
		}
		c := composeParityCase{Name: name, Project: project, Content: string(b), Env: fx.Env, Profiles: fx.Profiles, Files: fx.Files,
			Source: "internal/provider/testdata/compose_hash/" + fx.File}
		composeParityApplyOverride(t, &c)
		out = append(out, c)
	}
	return append(out, composeParityExampleCases(t)...)
}

// composeParityExampleCases reads the docker_compose resources of the
// example file whose mode is not swarm.
func composeParityExampleCases(t *testing.T) []composeParityCase {
	t.Helper()
	p := filepath.Join("..", "..", "examples", "resources", "docker_compose", "resource.tf")
	src, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, p, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags.Error())
	}
	var out []composeParityCase
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 || b.Labels[0] != "docker_compose" {
			continue
		}
		str := func(name string) string {
			a, ok := b.Body.Attributes[name]
			if !ok {
				return ""
			}
			v, d := a.Expr.Value(nil)
			if d.HasErrors() || v.Type() != cty.String {
				return ""
			}
			return v.AsString()
		}
		if str("mode") == "swarm" || str("content") == "" {
			continue
		}
		c := composeParityCase{Name: "example_" + b.Labels[1], Project: str("name"), Content: str("content"), Env: map[string]string{},
			Source: "examples/resources/docker_compose/resource.tf docker_compose." + b.Labels[1]}
		if a, ok := b.Body.Attributes["env"]; ok {
			if v, d := a.Expr.Value(nil); !d.HasErrors() && v.CanIterateElements() {
				for k, e := range v.AsValueMap() {
					c.Env[k] = e.AsString()
				}
			}
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		t.Fatalf("no local docker_compose project in %s", p)
	}
	return out
}

// composeParityObject is one created object, reduced to what is compared.
type composeParityObject map[string]string

// composeParitySnapshot lists everything labelled with project.
func composeParitySnapshot(ctx context.Context, c *client.Client, project string) (map[string]composeParityObject, error) {
	f := client.Filters{}.Add("label", composeLabelProject+"="+project)
	out := map[string]composeParityObject{}
	labels := func(o composeParityObject, l map[string]string) {
		for k, v := range l {
			if !composeParityIgnored(k) {
				o["label "+k] = v
			}
		}
	}
	cs, err := c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	// container:<id> references (network_mode / ipc / pid / volumes_from
	// of a service:<name> reference) name a container of this run; compare
	// the container's name, which both sides derive the same way.
	names := map[string]string{}
	for _, s := range cs.Items {
		if len(s.Names) > 0 {
			names[s.ID] = strings.TrimPrefix(s.Names[0], "/")
		}
	}
	ref := func(v string) string {
		for id, n := range names {
			v = strings.ReplaceAll(v, id, "<"+n+">")
		}
		return v
	}
	for _, s := range cs.Items {
		insp, err := c.ContainerInspect(ctx, s.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		ci := insp.Container
		o := composeParityObject{"image": ci.Config.Image}
		labels(o, ci.Config.Labels)
		env := append([]string(nil), ci.Config.Env...)
		sort.Strings(env)
		o["env"] = strings.Join(env, "\n")
		o["cmd"] = strings.Join(ci.Config.Cmd, " ")
		o["entrypoint"] = strings.Join(ci.Config.Entrypoint, " ")
		o["user"] = ci.Config.User
		o["working_dir"] = ci.Config.WorkingDir
		if hc := ci.HostConfig; hc != nil {
			o["restart"] = string(hc.RestartPolicy.Name)
			o["network_mode"] = ref(string(hc.NetworkMode))
			o["ipc_mode"] = ref(string(hc.IpcMode))
			o["pid_mode"] = ref(string(hc.PidMode))
			o["volumes_from"] = ref(strings.Join(hc.VolumesFrom, "; "))
			var ms []string
			for _, m := range hc.Mounts {
				ms = append(ms, fmt.Sprintf("%s %s->%s ro=%v", m.Type, m.Source, m.Target, m.ReadOnly))
			}
			sort.Strings(ms)
			o["mounts"] = strings.Join(ms, "; ")
			// compose sends plain bind and named-volume mounts as Binds.
			binds := append([]string(nil), hc.Binds...)
			sort.Strings(binds)
			o["binds"] = strings.Join(binds, "; ")
			var ps []string
			for port, bs := range hc.PortBindings {
				for _, b := range bs {
					ps = append(ps, fmt.Sprintf("%s:%s->%s", b.HostIP, b.HostPort, port))
				}
			}
			sort.Strings(ps)
			o["ports"] = strings.Join(ps, "; ")
		}
		if ns := ci.NetworkSettings; ns != nil {
			var nets []string
			for n, ep := range ns.Networks {
				al := append([]string(nil), ep.Aliases...)
				sort.Strings(al)
				nets = append(nets, n+" aliases="+strings.Join(al, ","))
			}
			sort.Strings(nets)
			o["networks"] = strings.Join(nets, "; ")
		}
		out["container "+strings.TrimPrefix(ci.Name, "/")] = o
	}
	ns, err := c.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	for _, n := range ns.Items {
		o := composeParityObject{"driver": n.Driver, "internal": fmt.Sprint(n.Internal), "attachable": fmt.Sprint(n.Attachable)}
		labels(o, n.Labels)
		out["network "+n.Name] = o
	}
	vs, err := c.VolumeList(ctx, client.VolumeListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	for _, v := range vs.Items {
		o := composeParityObject{"driver": v.Driver}
		labels(o, v.Labels)
		var opts []string
		for k, x := range v.Options {
			opts = append(opts, k+"="+x)
		}
		sort.Strings(opts)
		o["options"] = strings.Join(opts, ",")
		out["volume "+v.Name] = o
	}
	return out, nil
}

// composeParityDiff lists every difference between the CLI's objects and
// the provider's.
func composeParityDiff(cli, prov map[string]composeParityObject) []string {
	var out []string
	keys := map[string]bool{}
	for k := range cli {
		keys[k] = true
	}
	for k := range prov {
		keys[k] = true
	}
	for _, k := range composeSortedKeys(keys) {
		a, inCLI := cli[k]
		b, inProv := prov[k]
		switch {
		case !inProv:
			out = append(out, k+": created by the CLI, not by docker_compose")
			continue
		case !inCLI:
			out = append(out, k+": created by docker_compose, not by the CLI")
			continue
		}
		fields := map[string]bool{}
		for f := range a {
			fields[f] = true
		}
		for f := range b {
			fields[f] = true
		}
		for _, f := range composeSortedKeys(fields) {
			// The project directory and file paths are the same on both sides.
			if a[f] != b[f] {
				out = append(out, fmt.Sprintf("%s %s:\n    cli:      %q\n    provider: %q", k, f, a[f], b[f]))
			}
		}
	}
	return out
}

// composeParityCLI runs `docker compose` against the harness daemon.
func composeParityCLI(ctx context.Context, bin, dir string, c composeParityCase, args ...string) (string, error) {
	full := []string{"compose", "--project-name", c.Project, "--project-directory", dir, "--file", filepath.Join(dir, composeContentFilename)}
	for _, p := range c.Profiles {
		full = append(full, "--profile", p)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = dir
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "COMPOSE_") {
			env = append(env, e)
		}
	}
	for k, v := range c.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func composeParityHCL(dir string, c composeParityCase) string {
	env := ""
	for _, k := range composeSortedKeys(c.Env) {
		env += fmt.Sprintf("    %q = %q\n", k, c.Env[k])
	}
	profiles := ""
	for _, p := range c.Profiles {
		profiles += fmt.Sprintf("%q, ", p)
	}
	return fmt.Sprintf(`
resource "docker_compose" "parity" {
  name              = %q
  project_directory = %q
  files             = [%q]
  profiles          = [%s]
  env = {
%s  }
}
`, c.Project, filepath.ToSlash(dir), filepath.ToSlash(filepath.Join(dir, composeContentFilename)), profiles, env)
}

func TestAccComposeParity(t *testing.T) {
	testAccPreCheck(t)
	bin := os.Getenv("TF_ACC_COMPOSE_CLI")
	if bin == "" || os.Getenv("DOCKER_HOST") == "" {
		t.Skip("TF_ACC_COMPOSE_CLI and DOCKER_HOST are not set (source test/harness/.env); skipping the compose CLI parity test")
	}
	ctx := context.Background()
	if out, err := exec.CommandContext(ctx, bin, "compose", "version").CombinedOutput(); err != nil {
		t.Skipf("%s compose version failed (no compose v2 plugin?): %v: %s", bin, err, out)
	}
	c := testAccDockerClient(t)
	for _, tc := range composeParityCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, data := range tc.Files {
				p := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, composeContentFilename), []byte(tc.Content), 0o600); err != nil {
				t.Fatal(err)
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			t.Logf("fixture %s, project %s", tc.Source, tc.Project)
			clean := func() {
				if err := composeParityClean(context.Background(), c, tc.Project); err != nil {
					t.Log(err)
				}
			}
			down := func() {
				_, _ = composeParityCLI(context.Background(), bin, dir, tc, "down", "--volumes", "--remove-orphans")
				clean()
			}
			clean()
			t.Cleanup(clean)
			composeParityPrepare(cctx, t, c, tc.Name, tc.Setup)

			// 1. The CLI.
			out, err := composeParityCLI(cctx, bin, dir, tc, "up", "--detach", "--quiet-pull")
			if err != nil {
				down()
				t.Skipf("docker compose up failed, so there is no reference to compare with: %v\n%s", err, out)
			}
			cli, err := composeParitySnapshot(cctx, c, tc.Project)
			down()
			if err != nil {
				t.Fatal(err)
			}

			// 2. docker_compose.
			var prov map[string]composeParityObject
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: composeParityHCL(dir, tc),
					Check: func(*terraform.State) error {
						var err error
						prov, err = composeParitySnapshot(cctx, c, tc.Project)
						return err
					},
				}},
			})
			if prov == nil {
				t.Fatal("docker_compose did not create the project (see the step error above)")
			}
			for _, d := range composeParityDiff(cli, prov) {
				t.Error(d)
			}
		})
	}
}

// TestComposeParityDiffC checks the comparison itself (no daemon).
func TestComposeParityDiffC(t *testing.T) {
	cli := map[string]composeParityObject{
		"container p-web-1": {"label " + composeLabelConfigHash: "aaa", "image": "nginx"},
		"network p_default": {"driver": "bridge"},
	}
	prov := map[string]composeParityObject{
		"container p-web-1": {"label " + composeLabelConfigHash: "bbb", "image": "nginx"},
		"volume p_data":     {"driver": "local"},
	}
	d := composeParityDiff(cli, prov)
	if len(d) != 3 || !strings.Contains(d[0], "config-hash") || !strings.Contains(d[1], "network p_default: created by the CLI") ||
		!strings.Contains(d[2], "volume p_data: created by docker_compose") {
		t.Errorf("diff %q", d)
	}
	if len(composeParityDiff(cli, cli)) != 0 {
		t.Error("identical snapshots differ")
	}
	if !composeParityIgnored("com.docker.compose.version") || composeParityIgnored(composeLabelConfigHash) {
		t.Error("ignored labels")
	}
	cases := composeParityCases(t)
	if len(cases) < len(composeHashCompatFixtures)+1 {
		t.Errorf("%d parity cases", len(cases))
	}
	for _, c := range cases {
		if c.Project == "" || c.Content == "" {
			t.Errorf("case %s: empty project or content", c.Name)
		}
	}
}
