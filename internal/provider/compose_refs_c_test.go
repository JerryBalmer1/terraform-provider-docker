package provider

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// composeRefsYAML: web with an unresolved variable, sidecar sharing web's
// network namespace and volumes read-only (restart: true dependency through
// network_mode), reader sharing web's volumes only (restart: false).
const composeRefsYAML = `
name: refs
services:
  web:
    image: nginx:%s
    environment: {A: "1", UNSET: null, BARE_UNSET: ~}
  sidecar:
    image: busybox
    network_mode: service:web
    volumes_from: [web:ro]
  reader:
    image: busybox
    volumes_from: [web, "container:outside:ro"]
`

// TestComposeLocalRefsC checks the pure resolution against compose v5.1.4
// resolveServiceReferences: volumes_from "<svc>[:mode]" -> "<ID>",
// "container:<c>[:mode]" -> "<c>", "service:<svc>" -> "container:<ID>";
// the config-hash is ServiceHash of that resolved service.
func TestComposeLocalRefsC(t *testing.T) {
	d := composeMustLoad(t, composeInputs{Content: fmt.Sprintf(composeRefsYAML, "1.27")})
	plan, err := composeLocalExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]composeLocalContainer{}
	for _, c := range plan.Containers {
		by[c.Service] = c
	}
	web, side, reader := by["web"], by["sidecar"], by["reader"]
	if web.Refs != nil {
		t.Errorf("web refs %+v", web.Refs)
	}
	if !reflect.DeepEqual(web.UnsetEnv, []string{"BARE_UNSET", "UNSET"}) || !reflect.DeepEqual(web.Config.Env, []string{"A=1"}) {
		t.Errorf("web env %v unset %v", web.Config.Env, web.UnsetEnv)
	}
	if side.Refs == nil || !reflect.DeepEqual(side.Refs.Targets, map[string]string{"web": "refs-web-1"}) {
		t.Fatalf("sidecar refs %+v", side.Refs)
	}
	// The plan keeps names (the spec hash does not depend on IDs).
	if side.Host.NetworkMode != "container:refs-web-1" || !reflect.DeepEqual(side.Host.VolumesFrom, []string{"refs-web-1:ro"}) {
		t.Errorf("sidecar plan host %q %v", side.Host.NetworkMode, side.Host.VolumesFrom)
	}
	ids := func(id string) func(string) (string, bool) {
		return func(n string) (string, bool) { return id, n == "refs-web-1" }
	}
	rs, err := side.Refs.resolve(ids("abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if rs.NetworkMode != "container:abc123" || !reflect.DeepEqual(rs.VolumesFrom, []string{"abc123"}) {
		t.Errorf("resolved sidecar %q %v", rs.NetworkMode, rs.VolumesFrom)
	}
	if side.Refs.Service.NetworkMode != "service:web" || side.Refs.Service.VolumesFrom[0] != "web:ro" {
		t.Error("resolve changed the planned service")
	}
	// The hash is ServiceHash of the hand-resolved service.
	manual := d.Project.Services["sidecar"]
	manual.NetworkMode = "container:abc123"
	manual.VolumesFrom = []string{"abc123"}
	want, _ := composeServiceConfigHash(manual)
	h1, _ := side.Refs.configHash(ids("abc123"))
	h2, _ := side.Refs.configHash(ids("def456"))
	if h1 != want || h1 == h2 {
		t.Errorf("hash %s want %s (other id %s)", h1, want, h2)
	}
	if unresolved := side.Config.Labels[composeLabelConfigHash]; unresolved == h1 {
		t.Error("plan label is already resolved")
	}
	if _, err := side.Refs.configHash(func(string) (string, bool) { return "", false }); err == nil {
		t.Error("missing target: no error")
	}
	host := side.Refs.host(side.Host, map[string]string{"refs-web-1": "abc123"})
	if host.NetworkMode != "container:abc123" || !reflect.DeepEqual(host.VolumesFrom, []string{"abc123:ro"}) {
		t.Errorf("sidecar host %q %v", host.NetworkMode, host.VolumesFrom)
	}
	if side.Host.NetworkMode != "container:refs-web-1" {
		t.Error("host changed the plan")
	}
	// reader: service and named-container volumes_from.
	rr, err := reader.Refs.resolve(ids("abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rr.VolumesFrom, []string{"abc123", "outside"}) {
		t.Errorf("resolved reader %v", rr.VolumesFrom)
	}
	if h := reader.Refs.host(reader.Host, map[string]string{"refs-web-1": "abc123"}); !reflect.DeepEqual(h.VolumesFrom, []string{"abc123", "outside:ro"}) {
		t.Errorf("reader host %v", h.VolumesFrom)
	}
	// Bare keys join the env, sorted, never twice.
	if got := composeEnvWithUnset([]string{"B=2", "A=1"}, []string{"A", "C"}); !reflect.DeepEqual(got, []string{"A=1", "B=2", "C"}) {
		t.Errorf("env %v", got)
	}
}

// TestComposeFakeLocalRefsC runs the references through the resource on
// the fake Engine: IDs in the create request, the resolved config-hash, the
// bare unset variable, and recreation of dependants when web is replaced
// out of band (refresh drift) or by a change (apply).
func TestComposeFakeLocalRefsC(t *testing.T) {
	f, host := newComposeFakeEngine(t)
	dir := t.TempDir()
	cfg := func(tag string) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_compose" "refs" {
  project_directory = %q
  content     = <<-EOT
%s
EOT
}
`, filepath.ToSlash(dir), fmt.Sprintf(composeRefsYAML, tag)))
	}
	// expected returns the config-hash compose would write for svc with
	// web's container ID webID.
	expected := func(tag, svc, webID string) string {
		d := composeMustLoad(t, composeInputs{WorkingDir: dir, Content: fmt.Sprintf(composeRefsYAML, tag)})
		plan, err := composeLocalExpand(d.Project)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range plan.Containers {
			if c.Service == svc {
				h, err := c.Refs.configHash(func(string) (string, bool) { return webID, true })
				if err != nil {
					t.Fatal(err)
				}
				return h
			}
		}
		t.Fatalf("no %s", svc)
		return ""
	}
	check := func(tag string, creates map[string]int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			web, side, reader := f.byName("refs-web-1"), f.byName("refs-sidecar-1"), f.byName("refs-reader-1")
			if web == nil || side == nil || reader == nil {
				return fmt.Errorf("containers missing")
			}
			if !slices.Contains(web.Config.Env, "UNSET") || !slices.Contains(web.Config.Env, "BARE_UNSET") || !slices.Contains(web.Config.Env, "A=1") {
				return fmt.Errorf("web env %v", web.Config.Env)
			}
			if string(side.Host.NetworkMode) != "container:"+web.ID || !reflect.DeepEqual(side.Host.VolumesFrom, []string{web.ID + ":ro"}) {
				return fmt.Errorf("sidecar host %q %v (web %s)", side.Host.NetworkMode, side.Host.VolumesFrom, web.ID)
			}
			if !reflect.DeepEqual(reader.Host.VolumesFrom, []string{web.ID, "outside:ro"}) {
				return fmt.Errorf("reader volumes_from %v", reader.Host.VolumesFrom)
			}
			for _, c := range []*composeFakeContainer{side, reader} {
				svc := c.Config.Labels[composeLabelService]
				if got, want := c.Config.Labels[composeLabelConfigHash], expected(tag, svc, web.ID); got != want {
					return fmt.Errorf("%s config-hash %s, want %s", c.Name, got, want)
				}
			}
			for name, n := range creates {
				if f.creates[name] != n {
					return fmt.Errorf("creates %v, want %v", f.creates, creates)
				}
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("1.27"),
				Check:  check("1.27", map[string]int{"refs-web-1": 1, "refs-sidecar-1": 1, "refs-reader-1": 1}),
			},
			{
				// Re-applying the same configuration changes nothing.
				Config: cfg("1.27"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.refs", plancheck.ResourceActionNoop),
				}},
				Check: check("1.27", map[string]int{"refs-web-1": 1, "refs-sidecar-1": 1, "refs-reader-1": 1}),
			},
			{
				// web replaced out of band (same name and labels, new ID):
				// both dependants refer to a container that is gone, so
				// refresh reports drift and apply recreates them, not web.
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					web := f.byName("refs-web-1")
					delete(f.containers, web.ID)
					web.ID = f.id("c")
					f.containers[web.ID] = web
				},
				Config: cfg("1.27"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.refs", plancheck.ResourceActionUpdate),
				}},
				Check: check("1.27", map[string]int{"refs-web-1": 1, "refs-sidecar-1": 2, "refs-reader-1": 2}),
			},
			{
				// A change to web recreates it, then both dependants
				// (reader's volumes_from dependency has restart: false; it
				// is recreated for its config-hash, like compose).
				Config: cfg("1.28"),
				Check:  check("1.28", map[string]int{"refs-web-1": 2, "refs-sidecar-1": 3, "refs-reader-1": 3}),
			},
		},
	})
}
