package provider

// Pass 5.3 Lane K unit tests: the compose service keys local mode used to
// ignore, lifecycle hooks, D5 (config-hash recreation), the owner label
// and pull_policy refresh.

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/blkiodev"
	"github.com/moby/moby/api/types/network"
)

// network0K is a network named like project ow's default network, owned
// by another configuration.
var network0K = network.Summary{ID: "foreign", Name: "ow_default", Driver: "bridge", Scope: "local",
	Labels: map[string]string{ownerLabelKey: "team-b"}}

const composeFieldsYAMLK = `
name: kf
services:
  db:
    image: busybox:1.36
    scale: 2
  web:
    image: busybox:1.36
    links: ["db:database"]
    external_links: ["outside:ext"]
    mac_address: "02:42:ac:11:00:02"
    umask: "0022"
    use_api_socket: true
    pull_policy: every_2h
    credential_spec:
      file: spec.json
    blkio_config:
      weight: 300
      weight_device: [{path: /dev/sda, weight: 400}]
      device_read_bps: [{path: /dev/sda, rate: 1mb}]
      device_write_iops: [{path: /dev/sdb, rate: 120}]
    volumes:
      - type: volume
        target: /data
        volume:
          labels: {tier: cache}
    environment:
      A: "1"
      GONE: null
    pre_start:
      - command: ["init"]
      - image: alpine:3.20
        command: ["chown"]
        per_replica: true
    post_start:
      - command: ["echo", "up"]
        user: root
    pre_stop:
      - command: ["echo", "down"]
  refresh:
    image: busybox:1.36
    pull_policy: refresh
    pull_refresh_after: 3d
`

func composeFieldsPlanK(t *testing.T) *composeLocalPlan {
	t.Helper()
	d := composeMustLoad(t, composeInputs{Content: composeFieldsYAMLK})
	plan, err := composeLocalExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func composeFindK(t *testing.T, plan *composeLocalPlan, name string) composeLocalContainer {
	t.Helper()
	for _, c := range plan.Containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no container %s", name)
	return composeLocalContainer{}
}

func TestComposeLocalFieldsK(t *testing.T) {
	plan := composeFieldsPlanK(t)
	for _, w := range plan.Warnings {
		for _, k := range []string{"blkio", "credential_spec", "use_api_socket", "external_links", "hooks", "mac_address"} {
			if strings.Contains(w, k) {
				t.Errorf("unexpected warning %q", w)
			}
		}
	}
	web := composeFindK(t, plan, "kf-web-1")
	h := web.Host
	if h.BlkioWeight != 300 || !reflect.DeepEqual(h.BlkioWeightDevice, []*blkiodev.WeightDevice{{Path: "/dev/sda", Weight: 400}}) ||
		!reflect.DeepEqual(h.BlkioDeviceReadBps, []*blkiodev.ThrottleDevice{{Path: "/dev/sda", Rate: 1 << 20}}) ||
		!reflect.DeepEqual(h.BlkioDeviceWriteIOps, []*blkiodev.ThrottleDevice{{Path: "/dev/sdb", Rate: 120}}) {
		t.Errorf("blkio: %+v", h.Resources)
	}
	if h.Umask == nil || *h.Umask != 0o22 {
		t.Errorf("umask %v", h.Umask)
	}
	if !slices.Contains(h.SecurityOpt, "credentialspec=file://spec.json") {
		t.Errorf("security_opt %v", h.SecurityOpt)
	}
	wantLinks := []string{"kf-db-1:database", "kf-db-1:db-1", "kf-db-1:kf-db-1", "kf-db-2:database", "kf-db-2:db-1", "kf-db-2:kf-db-1", "outside:ext"}
	if !reflect.DeepEqual(h.Links, wantLinks) {
		t.Errorf("links %v", h.Links)
	}
	if len(web.Networks) == 0 || web.Networks[0].MacAddress != "02:42:ac:11:00:02" {
		t.Errorf("mac %+v", web.Networks)
	}
	// use_api_socket: socket bind, DOCKER_CONFIG, config.json copy.
	if !slices.Contains(h.Binds, composeAPISocketPath+":"+composeAPISocketPath) {
		t.Errorf("binds %v", h.Binds)
	}
	if !slices.Contains(web.Config.Env, "DOCKER_CONFIG="+composeAPISocketDir) {
		t.Errorf("env %v", web.Config.Env)
	}
	if len(web.Copies) != 1 || !web.Copies[0].APIAuth || web.Copies[0].Path != composeAPISocketConfig {
		t.Errorf("copies %+v", web.Copies)
	}
	// The config-hash is compose's: ServiceHash of the transformed service.
	d := composeMustLoad(t, composeInputs{Content: composeFieldsYAMLK})
	want, err := composeServiceConfigHash(composeWithAPISocket(d.Project.Services["web"]))
	if err != nil {
		t.Fatal(err)
	}
	if web.Config.Labels[composeLabelConfigHash] != want {
		t.Errorf("config-hash %s, want %s", web.Config.Labels[composeLabelConfigHash], want)
	}
	if web.CLIOnly != want {
		t.Errorf("CLIOnly %q: web has hooks and GONE: null", web.CLIOnly)
	}
	if db := composeFindK(t, plan, "kf-db-1"); db.CLIOnly != "" {
		t.Errorf("db has no CLI-only keys: %q", db.CLIOnly)
	}
	// Volume labels.
	var found bool
	for _, m := range h.Mounts {
		if m.Target == "/data" && m.VolumeOptions != nil && m.VolumeOptions.Labels["tier"] == "cache" {
			found = true
		}
	}
	if !found {
		t.Errorf("volume labels missing: %+v", h.Mounts)
	}
	// Pull refresh.
	if !web.PullRefresh || web.PullEvery != 2*time.Hour {
		t.Errorf("every_2h: %v %v", web.PullRefresh, web.PullEvery)
	}
	if r := composeFindK(t, plan, "kf-refresh-1"); !r.PullRefresh || r.PullEvery != 72*time.Hour {
		t.Errorf("refresh 3d: %v %v", r.PullRefresh, r.PullEvery)
	}
	// Hooks.
	if len(web.PreStart) != 2 || web.PreStart[0].PerReplica || !web.PreStart[1].PerReplica {
		t.Fatalf("pre_start %+v", web.PreStart)
	}
	s0, s1 := web.PreStart[0].Container, web.PreStart[1].Container
	if s0.Config.Image != "busybox:1.36" || !reflect.DeepEqual([]string(s0.Config.Cmd), []string{"init"}) || s1.Config.Image != "alpine:3.20" {
		t.Errorf("steps %v %v / %v", s0.Config.Image, s0.Config.Cmd, s1.Config.Image)
	}
	if s0.Config.Labels[composeLabelOneoff] != "True" || s0.Config.Labels[composeLabelConfigHash] != "" || s0.Host.RestartPolicy.Name != "no" {
		t.Errorf("step labels %v restart %v", s0.Config.Labels, s0.Host.RestartPolicy)
	}
	if len(web.PostStart) != 1 || web.PostStart[0].User != "root" || len(web.PreStop) != 1 {
		t.Errorf("post_start %+v pre_stop %+v", web.PostStart, web.PreStop)
	}
}

func TestComposeLocalFieldErrorsK(t *testing.T) {
	for name, yaml := range map[string]string{
		"credential_spec config": "name: e\nservices:\n  a:\n    image: x\n    credential_spec: {config: c}\nconfigs:\n  c: {content: x}\n",
		"mac conflict":           "name: e\nservices:\n  a:\n    image: x\n    mac_address: 02:42:ac:11:00:02\n    networks:\n      n: {mac_address: 02:42:ac:11:00:03}\nnetworks:\n  n: {}\n",
		"bad mac":                "name: e\nservices:\n  a:\n    image: x\n    mac_address: nope\n",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := composeLoad(context.Background(), composeInputs{Content: yaml, WorkingDir: t.TempDir()})
			if err != nil {
				return // compose-go rejected it already
			}
			if _, err := composeLocalExpand(p); err == nil {
				t.Error("want an error")
			}
		})
	}
}

// composeOwnerProviderK is a provider block with owner set (or not).
func composeOwnerProviderK(host, owner string) string {
	if owner == "" {
		return fmt.Sprintf("provider \"docker\" {\n  host = %q\n}\n", host)
	}
	return fmt.Sprintf("provider \"docker\" {\n  host  = %q\n  owner = %q\n}\n", host, owner)
}

const composeHooksYAMLK = `
name: hk
services:
  web:
    image: busybox:1.36
    command: ["sleep", "1d"]
    volumes: ["data:/data"]
    environment:
      A: "1"%s
    pre_start:
      - command: ["init"]
    post_start:
      - command: ["echo", "up"]
        user: root
        environment: {H: "1"}
    pre_stop:
      - command: ["echo", "down"]
volumes:
  data: {}
`

// TestComposeFakeHooksOwnerD5K drives docker_compose against the fake
// Engine: hooks in compose order, the owner label on every object, D5
// (adding `KEY: null` plans an update and recreates the container), and
// pre_stop on destroy.
func TestComposeFakeHooksOwnerD5K(t *testing.T) {
	f, host := newComposeFakeEngine(t)
	f.images["busybox:1.36"] = true
	dir := t.TempDir()
	cfg := func(extraEnv string) string {
		return composeOwnerProviderK(host, "team-a") + fmt.Sprintf(`
resource "docker_compose" "hk" {
  project_directory = %q
  content           = <<-EOT
%s
EOT
}
`, filepath.ToSlash(dir), fmt.Sprintf(composeHooksYAMLK, extraEnv))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					web := f.byName("hk-web-1")
					if web == nil {
						return fmt.Errorf("no hk-web-1")
					}
					want := []string{"create hk-web-1", "create hk-web-pre_start-1", "start hk-web-pre_start-1", "remove hk-web-pre_start-1",
						"start hk-web-1", "exec hk-web-1 echo up"}
					if !reflect.DeepEqual(f.events, want) {
						return fmt.Errorf("events %q, want %q", f.events, want)
					}
					if len(f.execs) != 1 || f.execs[0].Opts.User != "root" || !slices.Contains(f.execs[0].Opts.Env, "H=1") {
						return fmt.Errorf("post_start exec %+v", f.execs)
					}
					if web.Config.Labels[ownerLabelKey] != "team-a" {
						return fmt.Errorf("container labels %v", web.Config.Labels)
					}
					for _, n := range f.networks {
						if n.Labels[composeLabelProject] == "hk" && n.Labels[ownerLabelKey] != "team-a" {
							return fmt.Errorf("network %s labels %v", n.Name, n.Labels)
						}
					}
					if v := f.volumes["hk_data"]; v == nil || v.Labels[ownerLabelKey] != "team-a" {
						return fmt.Errorf("volume %+v", v)
					}
					// The owner label is not part of the config-hash.
					d := composeMustLoad(t, composeInputs{WorkingDir: dir, Content: fmt.Sprintf(composeHooksYAMLK, "")})
					if h, _ := composeServiceConfigHash(d.Project.Services["web"]); web.Config.Labels[composeLabelConfigHash] != h {
						return fmt.Errorf("config-hash %s, want %s", web.Config.Labels[composeLabelConfigHash], h)
					}
					f.events = nil
					return nil
				},
			},
			{
				// D5: only the CLI config-hash sees `GONE: null`; the plan
				// shows an update and the container is recreated (pre_stop
				// first), with GONE sent as a bare key.
				Config: cfg("\n      GONE: null"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.hk", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if f.creates["hk-web-1"] != 2 {
						return fmt.Errorf("creates %v", f.creates)
					}
					if len(f.events) == 0 || f.events[0] != "exec hk-web-1 echo down" {
						return fmt.Errorf("pre_stop must run first: %q", f.events)
					}
					if web := f.byName("hk-web-1"); web == nil || !slices.Contains(web.Config.Env, "GONE") {
						return fmt.Errorf("bare GONE missing")
					}
					f.events = nil
					return nil
				},
			},
			{
				Config: cfg("\n      GONE: null"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.hk", plancheck.ResourceActionNoop),
				}},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.events) == 0 || f.events[0] != "exec hk-web-1 echo down" {
				return fmt.Errorf("destroy must run pre_stop first: %q", f.events)
			}
			return nil
		},
	})
}

// TestComposeFakeOwnerK: no label without `owner`; a foreign owner on a
// network the project would create fails the create.
func TestComposeFakeOwnerK(t *testing.T) {
	ctx := context.Background()
	f, host := newComposeFakeEngine(t)
	f.images["busybox:1.36"] = true
	c, err := newDockerClient(endpointConfig{Host: host, APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	d := composeMustLoad(t, composeInputs{Content: "name: ow\nservices:\n  a:\n    image: busybox:1.36\n"})
	plan, err := composeLocalExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	none := &providerData{Client: c}
	if err := composeLocalApply(ctx, none, plan, composeLocalApplyOpts{}, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if _, ok := f.byName("ow-a-1").Config.Labels[ownerLabelKey]; ok {
		t.Error("owner unset must not label the container")
	}
	for _, n := range f.networks {
		if _, ok := n.Labels[ownerLabelKey]; ok {
			t.Errorf("owner unset must not label network %s", n.Name)
		}
	}
	f.mu.Unlock()
	if err := composeLocalDestroy(ctx, none, "ow", false, plan); err != nil {
		t.Fatal(err)
	}
	// A network of the same name owned by team-b.
	f.mu.Lock()
	f.networks["foreign"] = &network0K
	f.mu.Unlock()
	on := &providerData{Client: c, Owner: "team-a"}
	err = composeLocalApply(ctx, on, plan, composeLocalApplyOpts{}, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "owned by \"team-b\"") {
		t.Fatalf("want the owner conflict, got %v", err)
	}
	// The project-level check (Create).
	if err := composeOwnerCreateCheck(on, "ow", []map[string]string{{ownerLabelKey: "team-b"}}); err == nil {
		t.Error("project owned by team-b must fail")
	}
	if err := composeOwnerCreateCheck(on, "ow", []map[string]string{{ownerLabelKey: "team-a"}, {}}); err != nil {
		t.Error(err)
	}
}

// TestComposeFakeRunOwnerK: docker_compose_run's one-off container is
// owner-labelled inject-only (no conflict check), and the label does not
// change its config-hash.
func TestComposeFakeRunOwnerK(t *testing.T) {
	ctx := context.Background()
	for _, owner := range []string{"", "team-a"} {
		f, host := newComposeFakeEngine(t)
		f.images["busybox:1.36"] = true
		c, err := newDockerClient(endpointConfig{Host: host, APIVersion: "1.47"})
		if err != nil {
			t.Fatal(err)
		}
		d := composeMustLoad(t, composeInputs{Content: "name: rn\nservices:\n  a:\n    image: busybox:1.36\n"})
		plan, err := composeLocalExpand(d.Project)
		if err != nil {
			t.Fatal(err)
		}
		dc := plan.Containers[0]
		cfg := *dc.Config
		cfg.Labels = mergeLabels(cfg.Labels, map[string]string{composeLabelOneoff: "True"})
		dc.Config = &cfg
		dc.Name = "rn-a-run-x"
		dc.Networks = nil // the fake has no project network here
		pd := &providerData{Client: c, Owner: owner}
		if _, err := composeLocalCreate(ctx, pd, dc, nil); err != nil {
			t.Fatal(err)
		}
		got := f.byName("rn-a-run-x").Config.Labels
		if v, ok := got[ownerLabelKey]; (owner == "") == ok || v != owner {
			t.Errorf("owner %q: label %q present=%v", owner, v, ok)
		}
		if got[composeLabelConfigHash] != dc.Config.Labels[composeLabelConfigHash] {
			t.Errorf("owner %q changed the config-hash", owner)
		}
	}
}

// TestComposeFakePullRefreshK: pull_policy every_<d> pulls when the image
// is older than the period and recreates a container whose image changed.
func TestComposeFakePullRefreshK(t *testing.T) {
	ctx := context.Background()
	f, host := newComposeFakeEngine(t)
	c, err := newDockerClient(endpointConfig{Host: host, APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	pd := &providerData{Client: c}
	d := composeMustLoad(t, composeInputs{Content: "name: pr\nservices:\n  a:\n    image: busybox:1.36\n    pull_policy: every_1h\n"})
	plan, err := composeLocalExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	f.images["busybox:1.36"] = true
	f.tagTime["busybox:1.36"] = time.Now().Add(-10 * time.Minute)
	if err := composeLocalApply(ctx, pd, plan, composeLocalApplyOpts{}, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if f.pulls["busybox:1.36"] != 0 {
		t.Errorf("fresh image pulled: %v", f.pulls)
	}
	ex, _ := composeLocalList(ctx, pd, "pr")
	if dr := composeLocalPullDrift(ctx, pd, plan, ex, time.Now()); len(dr) != 0 {
		t.Errorf("drift %v", dr)
	}
	// Two hours later the period is over: refresh reports drift, apply
	// pulls and, since the image changed, recreates the container.
	f.tagTime["busybox:1.36"] = time.Now().Add(-2 * time.Hour)
	f.nextID["busybox:1.36"] = "sha256:" + strings.Repeat("c", 64)
	if dr := composeLocalPullDrift(ctx, pd, plan, ex, time.Now()); len(dr) != 1 {
		t.Errorf("drift %v", dr)
	}
	if err := composeLocalApply(ctx, pd, plan, composeLocalApplyOpts{}, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if f.pulls["busybox:1.36"] != 1 || f.creates["pr-a-1"] != 2 {
		t.Errorf("pulls %v creates %v", f.pulls, f.creates)
	}
	// Due again but the image is the same: pulled, not recreated.
	f.tagTime["busybox:1.36"] = time.Now().Add(-2 * time.Hour)
	if err := composeLocalApply(ctx, pd, plan, composeLocalApplyOpts{}, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if f.pulls["busybox:1.36"] != 2 || f.creates["pr-a-1"] != 2 {
		t.Errorf("pulls %v creates %v", f.pulls, f.creates)
	}
}

// TestComposeFakePreStartFailK: a failing pre_start step fails the service
// before its container starts; post_start failing fails the apply.
func TestComposeFakeHookFailuresK(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct{ yaml, want string }{
		"pre_start":  {"    pre_start:\n      - command: [\"fail\"]\n", "pre_start[0]"},
		"post_start": {"    post_start:\n      - command: [\"fail\"]\n", "post_start[0] hook exited with status 3"},
	} {
		t.Run(name, func(t *testing.T) {
			f, host := newComposeFakeEngine(t)
			f.images["busybox:1.36"] = true
			c, err := newDockerClient(endpointConfig{Host: host, APIVersion: "1.47"})
			if err != nil {
				t.Fatal(err)
			}
			d := composeMustLoad(t, composeInputs{Content: "name: hf\nservices:\n  a:\n    image: busybox:1.36\n" + tc.yaml})
			plan, err := composeLocalExpand(d.Project)
			if err != nil {
				t.Fatal(err)
			}
			err = composeLocalApply(ctx, &providerData{Client: c}, plan, composeLocalApplyOpts{}, func(string, string) {})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if name == "pre_start" {
				if a := f.byName("hf-a-1"); a == nil || a.Started != 0 {
					t.Error("the service container must not start")
				}
				if f.byName("hf-a-pre_start-1") != nil {
					t.Error("the step container must be removed")
				}
			}
		})
	}
}

func TestComposeBuildRequestK(t *testing.T) {
	dir := t.TempDir()
	yaml := "name: bk\nservices:\n  base:\n    build: {context: .}\n  app:\n    build:\n      context: .\n      additional_contexts:\n        b: service:base\n        img: docker-image://alpine:3.20\n      entitlements: [network.host]\n      privileged: true\n"
	d := composeMustLoad(t, composeInputs{Content: yaml, WorkingDir: dir})
	req, err := composeBuildRequest(d.Project, d.Project.Services["app"], "bk-app")
	if err != nil {
		t.Fatal(err)
	}
	if req.Contexts["b"] != "docker-image://bk-base" || req.Contexts["img"] != "docker-image://alpine:3.20" {
		t.Errorf("contexts %v", req.Contexts)
	}
	if !reflect.DeepEqual(req.Entitlements, []string{"network.host", "security.insecure"}) {
		t.Errorf("entitlements %v", req.Entitlements)
	}
}
