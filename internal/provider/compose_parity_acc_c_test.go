package provider

// TestAccComposeParity brings every compose fixture up twice on the
// harness daemon, once with the real `docker compose` CLI and once with
// docker_compose (local mode), and diffs what each created: containers,
// networks, volumes, their labels and com.docker.compose.config-hash
// values. Any difference fails, except the entries of
// composeParityKnownDiffs (each one documented in reference/CONTRACT.md,
// "Known differences from upstream"); an entry that no longer matches a
// difference in the cases it names fails too (stale).
//
// Two fields are compared through a normalisation, not literally:
//   - com.docker.compose.depends_on is a set: compose v5.7.0 writes it in Go
//     map order (composeParitySetLabels);
//   - com.docker.compose.config-hash of a container with service references
//     (service:<x>, volumes_from) hashes the target's container ID, which
//     differs between the two runs: each side's label must equal the hash
//     of the service resolved against that side's own IDs, and is then
//     compared as "<resolved ...>" (composeParitySnapshot).
//
// The provider never shells out; this TEST runs the CLI (os/exec in this
// _test.go file only) against DOCKER_HOST from test/harness/.env. It needs
// TF_ACC, DOCKER_HOST and TF_ACC_COMPOSE_CLI (harness-up installs the
// docker compose release reference/SOURCES.md pins into test/harness/.bin and
// points it there; a docker binary with the compose plugin works too) and
// skips without them.
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
	"slices"
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

// composeParitySetLabels are labels whose value is a comma-separated set.
// com.docker.compose.depends_on: docker compose v5.7.0 builds it by ranging
// over the service.DependsOn map (pkg/compose/depends_on_label.go:30-36,
// encodeDependsOnLabel: `for dep, d := range dependsOn`, called from
// create.go:319), so
// its order is Go's random map order, different per container and per run;
// docker_compose sorts it. Only the set of entries is compared.
var composeParitySetLabels = []string{"label " + composeLabelDependsOn}

// composeParitySetEqual compares two comma-separated sets.
func composeParitySetEqual(a, b string) bool {
	as, bs := strings.Split(a, ","), strings.Split(b, ",")
	sort.Strings(as)
	sort.Strings(bs)
	return strings.Join(as, ",") == strings.Join(bs, ",")
}

// composeParityKnownDiff is one documented, accepted difference between the
// CLI and docker_compose. Match must be as narrow as the difference.
type composeParityKnownDiff struct {
	Field  string   // the snapshot field
	Cases  []string // sub-cases where it must occur (stale check)
	Reason string
	Source string // the upstream code that produces the CLI's value
	Match  func(cli, prov string) bool
}

// composeParityKnownDiffs: reference/CONTRACT.md "Known differences from
// upstream" lists the same entries. Keep this table to that list.
var composeParityKnownDiffs = []composeParityKnownDiff{{
	Field: "volumes_from",
	Cases: []string{"unit_expand"},
	Reason: `volumes_from "<service>:ro" / "container:<c>:ro": docker compose drops the access mode and sends the bare container ` +
		`(the volumes are mounted read-write); docker_compose keeps ":ro" / ":rw" as the compose spec documents it`,
	Source: "docker/compose v5.7.0 pkg/compose/service_containers.go:79-97 resolveVolumeFrom (service.VolumesFrom[i] = spec[1] / " +
		"dependencies.sorted()[0].ID), called from executor_ops.go:93, sent as-is by pkg/compose/create.go:453",
	Match: composeParityModeSuffixOnly,
}}

// composeParityModeSuffixOnly reports whether prov is cli with an access
// mode (":ro" or ":rw") added to one or more entries, and nothing else.
func composeParityModeSuffixOnly(cli, prov string) bool {
	cs, ps := strings.Split(cli, "; "), strings.Split(prov, "; ")
	if len(cs) != len(ps) || cli == prov {
		return false
	}
	for i := range cs {
		if ps[i] != cs[i] && ps[i] != cs[i]+":ro" && ps[i] != cs[i]+":rw" {
			return false
		}
	}
	return true
}

// composeParityDifference is one field (or object) that differs.
type composeParityDifference struct {
	Object, Field, CLI, Prov string
	Missing                  string // set when the object exists on one side only
}

func (d composeParityDifference) String() string {
	if d.Missing != "" {
		return d.Object + ": " + d.Missing
	}
	return fmt.Sprintf("%s %s:\n    cli:      %q\n    provider: %q", d.Object, d.Field, d.CLI, d.Prov)
}

// known returns the index of the composeParityKnownDiffs entry d matches,
// or -1.
func (d composeParityDifference) known() int {
	if d.Missing != "" {
		return -1
	}
	for i, k := range composeParityKnownDiffs {
		if k.Field == d.Field && k.Match(d.CLI, d.Prov) {
			return i
		}
	}
	return -1
}

// composeParitySnapshot lists everything labelled with project. refs are
// the planned references by container name (composeParityRefs), used to
// check a referencing container's config-hash against its own IDs.
func composeParitySnapshot(ctx context.Context, c *client.Client, project string, refs map[string]*composeLocalRefs) (map[string]composeParityObject, error) {
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
	byName := func(n string) (string, bool) {
		for id, name := range names {
			if name == n {
				return id, true
			}
		}
		return "", false
	}
	for _, s := range cs.Items {
		insp, err := c.ContainerInspect(ctx, s.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		ci := insp.Container
		o := composeParityObject{"image": ci.Config.Image}
		labels(o, ci.Config.Labels)
		if r := refs[strings.TrimPrefix(ci.Name, "/")]; r != nil {
			k := "label " + composeLabelConfigHash
			if want, err := r.configHash(byName); err == nil && o[k] == want {
				o[k] = "<resolved against " + strings.Join(r.names(), ", ") + ">"
			}
		}
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
// the provider's, as text.
func composeParityDiff(cli, prov map[string]composeParityObject) []string {
	var out []string
	for _, d := range composeParityDiffs(cli, prov) {
		out = append(out, d.String())
	}
	return out
}

// composeParityDiffs lists every difference between the CLI's objects and
// the provider's.
func composeParityDiffs(cli, prov map[string]composeParityObject) []composeParityDifference {
	var out []composeParityDifference
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
			out = append(out, composeParityDifference{Object: k, Missing: "created by the CLI, not by docker_compose"})
			continue
		case !inCLI:
			out = append(out, composeParityDifference{Object: k, Missing: "created by docker_compose, not by the CLI"})
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
			if a[f] == b[f] {
				continue
			}
			if slices.Contains(composeParitySetLabels, f) && composeParitySetEqual(a[f], b[f]) {
				continue
			}
			out = append(out, composeParityDifference{Object: k, Field: f, CLI: a[f], Prov: b[f]})
		}
	}
	return out
}

// composeParityPrefix is the argument that selects compose on bin: none for
// the standalone docker-compose release binary harness-up installs into
// test/harness/.bin (the pinned version), "compose" for a docker CLI with the
// compose plugin.
func composeParityPrefix(bin string) []string {
	if strings.HasPrefix(strings.ToLower(filepath.Base(bin)), "docker-compose") {
		return nil
	}
	return []string{"compose"}
}

// composeParityCLI runs `docker compose` against the harness daemon.
func composeParityCLI(ctx context.Context, bin, dir string, c composeParityCase, args ...string) (string, error) {
	full := append(composeParityPrefix(bin), "--project-name", c.Project, "--project-directory", dir, "--file", filepath.Join(dir, composeContentFilename))
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
	if out, err := exec.CommandContext(ctx, bin, append(composeParityPrefix(bin), "version")...).CombinedOutput(); err != nil {
		t.Skipf("%s compose version failed (no compose v2 plugin?): %v: %s", bin, err, out)
	}
	c := testAccDockerClient(t)
	// known[i][case]: composeParityKnownDiffs[i] matched in that case.
	known := make([]map[string]bool, len(composeParityKnownDiffs))
	for i := range known {
		known[i] = map[string]bool{}
	}
	ran := map[string]bool{} // cases compared to the end
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
			refs := composeParityRefs(t, dir, tc)
			cli, err := composeParitySnapshot(cctx, c, tc.Project, refs)
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
						prov, err = composeParitySnapshot(cctx, c, tc.Project, refs)
						return err
					},
				}},
			})
			if prov == nil {
				t.Fatal("docker_compose did not create the project (see the step error above)")
			}
			for _, d := range composeParityDiffs(cli, prov) {
				if i := d.known(); i >= 0 {
					known[i][tc.Name] = true
					t.Logf("known difference (reference/CONTRACT.md, Known differences from upstream): %s\n    reason: %s\n    source: %s",
						d, composeParityKnownDiffs[i].Reason, composeParityKnownDiffs[i].Source)
					continue
				}
				t.Error(d)
			}
			ran[tc.Name] = true
		})
	}
	for i, k := range composeParityKnownDiffs {
		for _, name := range k.Cases {
			if ran[name] && !known[i][name] {
				t.Errorf("stale known difference %s (%s): no longer observed in %s; remove it here and in reference/CONTRACT.md", k.Field, k.Reason, name)
			}
		}
	}
}

// composeParityRefs loads tc the way the docker_compose step does (files,
// project_directory, env, profiles) and returns each planned container's
// service references by container name.
func composeParityRefs(t *testing.T, dir string, tc composeParityCase) map[string]*composeLocalRefs {
	t.Helper()
	p, err := composeLoad(context.Background(), composeInputs{
		Files: []string{filepath.Join(dir, composeContentFilename)}, WorkingDir: dir,
		ProjectName: tc.Project, Env: tc.Env, Profiles: tc.Profiles,
	})
	if err != nil {
		t.Fatalf("loading %s: %v", tc.Source, err)
	}
	plan, err := composeLocalExpand(p)
	if err != nil {
		t.Fatalf("expanding %s: %v", tc.Source, err)
	}
	out := map[string]*composeLocalRefs{}
	for _, dc := range plan.Containers {
		if dc.Refs != nil {
			out[dc.Name] = dc.Refs
		}
	}
	return out
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

	// depends_on is compared as a set: the same entries in another order
	// are equal, a different set is not.
	dep := "label " + composeLabelDependsOn
	a := map[string]composeParityObject{"container w": {dep: "db:service_healthy:false,cache:service_started:false"}}
	b := map[string]composeParityObject{"container w": {dep: "cache:service_started:false,db:service_healthy:false"}}
	if d := composeParityDiff(a, b); len(d) != 0 {
		t.Errorf("depends_on order is a difference: %q", d)
	}
	b["container w"][dep] = "cache:service_started:false,db:service_started:false"
	if d := composeParityDiff(a, b); len(d) != 1 || !strings.Contains(d[0], composeLabelDependsOn) {
		t.Errorf("depends_on set change not reported: %q", d)
	}
	// Other labels stay order-sensitive.
	if d := composeParityDiff(map[string]composeParityObject{"x": {"label l": "a,b"}}, map[string]composeParityObject{"x": {"label l": "b,a"}}); len(d) != 1 {
		t.Errorf("plain label compared as a set: %q", d)
	}

	// The allowlist: one entry, the volumes_from access mode suffix only.
	if len(composeParityKnownDiffs) != 1 || composeParityKnownDiffs[0].Field != "volumes_from" {
		t.Fatalf("known differences %+v", composeParityKnownDiffs)
	}
	for _, tc := range []struct {
		cli, prov string
		known     bool
	}{
		{"<proj-web-1>", "<proj-web-1>:ro", true},
		{"<proj-web-1>; other", "<proj-web-1>:ro; other:rw", true},
		{"<proj-web-1>", "<proj-web-1>", false},           // not a difference
		{"<proj-web-1>", "proj-web-1:ro", false},          // a name, not the container
		{"<proj-web-1>", "<proj-web-2>:ro", false},        // another container
		{"<proj-web-1>", "<proj-web-1>:z", false},         // not an access mode
		{"<proj-web-1>", "<proj-web-1>:ro; extra", false}, // an extra entry
	} {
		d := composeParityDifference{Object: "container proj-sidecar-1", Field: "volumes_from", CLI: tc.cli, Prov: tc.prov}
		if got := d.known() >= 0; got != tc.known {
			t.Errorf("%q vs %q: known %v, want %v", tc.cli, tc.prov, got, tc.known)
		}
	}
	if (composeParityDifference{Object: "container x", Field: "network_mode", CLI: "a", Prov: "a:ro"}).known() >= 0 {
		t.Error("the allowlist matches another field")
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
	// unit_expand's sidecar has references (the parity run normalises its
	// config-hash through them).
	dir := t.TempDir()
	for _, c := range cases {
		if c.Name != "unit_expand" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, composeContentFilename), []byte(c.Content), 0o600); err != nil {
			t.Fatal(err)
		}
		if r := composeParityRefs(t, dir, c)["proj-sidecar-1"]; r == nil || r.Targets["web"] != "proj-web-1" {
			t.Errorf("unit_expand sidecar refs %+v", r)
		}
	}
}
