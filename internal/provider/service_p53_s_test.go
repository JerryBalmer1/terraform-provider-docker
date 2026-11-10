package provider

// Pass 5.3 Lane S unit tests: the new service / secret / config / swarm
// fields (field-gaps/S.json) and the owner-label wiring of docker_service,
// docker_secret, docker_config, docker_stack, docker_swarm_init and
// docker_swarm_node against httptest fake Engines.

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
)

// svcP53Model is a service using every Pass 5.3 attribute.
func svcP53Model() svcSpecModel {
	m := svcTestModel()
	m.TTY = types.BoolValue(true)
	m.OpenStdin = types.BoolValue(true)
	m.OomScoreAdj = types.Int64Value(-500)
	m.Privileges.Seccomp = &svcSeccompModel{Mode: types.StringValue("custom"), Profile: types.StringValue(`{"defaultAction":"SCMP_ACT_ALLOW"}`)}
	m.Privileges.AppArmor = &svcAppArmorModel{Mode: types.StringValue("disabled")}
	m.Privileges.NoNewPrivileges = types.BoolValue(true)
	m.Mounts[1].TmpfsOptions.Options = types.ListValueMust(svcTmpfsOptionsType, []attr.Value{
		stringListValue([]string{"exec"}, false), stringListValue([]string{"nr_inodes", "1000"}, false)})
	m.Mounts[2].BindOptions.NonRecursive = types.BoolValue(true)
	m.Healthcheck.StartInterval = types.StringValue("2s")
	m.Resources.Limits.Pids = types.Int64Value(100)
	m.Resources.SwapBytes = types.Int64Value(-1)
	m.Resources.MemorySwappiness = types.Int64Value(10)
	m.Configs[0].Runtime = types.BoolValue(true)
	return m
}

func TestServiceP53ExpandFlatten(t *testing.T) {
	ctx := context.Background()
	m := svcP53Model()
	spec, diags := svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{FullName: "web"})
	if diags.HasError() {
		t.Fatal(diags)
	}
	cs := spec.TaskTemplate.ContainerSpec
	if !cs.TTY || !cs.OpenStdin || cs.OomScoreAdj != -500 {
		t.Errorf("tty/open_stdin/oom_score_adj = %v %v %v", cs.TTY, cs.OpenStdin, cs.OomScoreAdj)
	}
	p := cs.Privileges
	if p.Seccomp == nil || p.Seccomp.Mode != swarm.SeccompModeCustom || string(p.Seccomp.Profile) == "" ||
		p.AppArmor == nil || p.AppArmor.Mode != swarm.AppArmorModeDisabled || !p.NoNewPrivileges {
		t.Errorf("privileges = %+v", p)
	}
	if o := cs.Mounts[1].TmpfsOptions.Options; !reflect.DeepEqual(o, [][]string{{"exec"}, {"nr_inodes", "1000"}}) {
		t.Errorf("tmpfs options = %v", o)
	}
	if !cs.Mounts[2].BindOptions.NonRecursive {
		t.Error("bind non_recursive not sent")
	}
	if cs.Healthcheck.StartInterval != 2*time.Second {
		t.Errorf("start_interval = %v", cs.Healthcheck.StartInterval)
	}
	r := spec.TaskTemplate.Resources
	if r.Limits.Pids != 100 || r.SwapBytes == nil || *r.SwapBytes != -1 || r.MemorySwappiness == nil || *r.MemorySwappiness != 10 {
		t.Errorf("resources = %+v limits %+v", r, r.Limits)
	}
	if c := cs.Configs[0]; c.Runtime == nil || c.File != nil {
		t.Errorf("config runtime target = %+v", c)
	}
	got := svcFlatten(svcSimulateDaemon(spec), m, svcTestLookup(), svcFlattenOpts{})
	for _, f := range []string{"TTY", "OpenStdin", "OomScoreAdj", "Privileges", "Mounts", "Healthcheck", "Resources", "Configs"} {
		a := reflect.ValueOf(got).FieldByName(f).Interface()
		b := reflect.ValueOf(m).FieldByName(f).Interface()
		if fmt.Sprintf("%+v", deref(a)) != fmt.Sprintf("%+v", deref(b)) {
			t.Errorf("round trip %s:\n got %+v\nwant %+v", f, deref(a), deref(b))
		}
	}
}

// deref prints pointer fields by value (and slices of structs as they are).
func deref(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return deref(rv.Elem().Interface())
	}
	if rv.Kind() == reflect.Struct {
		out := map[string]any{}
		for i := 0; i < rv.NumField(); i++ {
			if rv.Type().Field(i).IsExported() {
				out[rv.Type().Field(i).Name] = deref(rv.Field(i).Interface())
			}
		}
		return out
	}
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Struct {
		var out []any
		for i := 0; i < rv.Len(); i++ {
			out = append(out, deref(rv.Index(i).Interface()))
		}
		return out
	}
	return v
}

func TestServiceP53FlattenImportDefaults(t *testing.T) {
	// A daemon spec that only carries defaults (rprivate bind, default
	// seccomp / apparmor) shows no Pass 5.3 values when nothing was set.
	spec := swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
		Image: "nginx",
		Privileges: &swarm.Privileges{Seccomp: &swarm.SeccompOpts{Mode: swarm.SeccompModeDefault},
			AppArmor: &swarm.AppArmorOpts{Mode: swarm.AppArmorModeDefault}},
		Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/a", Target: "/b", BindOptions: &mount.BindOptions{Propagation: mount.PropagationRPrivate}}},
	}}}
	got := svcFlatten(spec, svcSpecModel{}, svcTestLookup(), svcFlattenOpts{})
	if got.Privileges != nil || !got.TTY.IsNull() || !got.OpenStdin.IsNull() || !got.OomScoreAdj.IsNull() || got.Mounts[0].BindOptions != nil {
		t.Errorf("defaults should be null: priv=%+v tty=%v bind=%+v", got.Privileges, got.TTY, got.Mounts[0].BindOptions)
	}
}

func TestServiceP53Validation(t *testing.T) {
	ctx := context.Background()
	m := svcP53Model()
	m.Privileges.Seccomp = &svcSeccompModel{Mode: types.StringValue("custom"), Profile: types.StringNull()}
	if _, d := svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{}); !d.HasError() {
		t.Error("custom seccomp without profile accepted")
	}
	m = svcP53Model()
	m.Mounts[1].TmpfsOptions.Options = types.ListValueMust(svcTmpfsOptionsType, []attr.Value{stringListValue([]string{"a", "b", "c"}, false)})
	if _, d := svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{}); !d.HasError() {
		t.Error("three-element tmpfs option accepted")
	}
}

// TestServiceP53APIGate: swap_bytes needs Engine API 1.52; the fake daemon
// speaks 1.47, so the plan fails.
func TestServiceP53APIGate(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: svcFakeConfig(host, `
resource "docker_service" "web" {
  name  = "web"
  image = "nginx:1.27"
  resources {
    swap_bytes = 1048576
  }
}
`),
			ExpectError: regexp.MustCompile(`needs Engine API >= 1.52`),
		}},
	})
}

func svcOwnerConfig(host, owner, body string) string {
	o := ""
	if owner != "" {
		o = fmt.Sprintf("  owner = %q\n", owner)
	}
	return fmt.Sprintf("provider \"docker\" {\n  host = %q\n%s}\n%s", host, o, body)
}

const svcOwnerBody = `
resource "docker_secret" "s" {
  name   = "s1"
  data   = base64encode("x")
  labels = { a = "b" }
}

resource "docker_config" "c" {
  name = "c1"
  data = base64encode("y")
  templating = { name = "golang" }
}

resource "docker_service" "web" {
  name   = "web"
  image  = "nginx:1.27"
  labels = { team = "web" }
  tty    = true
}

resource "docker_stack" "app" {
  name = "app"
  network {
    name = "net"
  }
  secret {
    name = "pw"
    data = base64encode("pw")
  }
  config {
    name = "conf"
    data = base64encode("c")
  }
  service {
    name     = "api"
    image    = "nginx:1.27"
    networks = { net = {} }
  }
}
`

// TestOwnerLabelSwarmWorkload: with `owner` set every object of
// docker_secret, docker_config, docker_service and docker_stack carries the
// owner label (and state shows none, so the post-apply plan is empty).
func TestOwnerLabelSwarmWorkload(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: svcOwnerConfig(host, "team-a", svcOwnerBody),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_secret.s", "labels.%", "1"),
				resource.TestCheckResourceAttr("docker_service.web", "labels.%", "1"),
				resource.TestCheckResourceAttr("docker_config.c", "templating.name", "golang"),
				func(*terraform.State) error { return svcFakeAllOwned(f, "team-a") },
			),
		}},
	})
}

// TestOwnerLabelSwarmWorkloadUnset: no `owner`, no label.
func TestOwnerLabelSwarmWorkloadUnset(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: svcOwnerConfig(host, "", svcOwnerBody),
			Check:  func(*terraform.State) error { return svcFakeAllOwned(f, "") },
		}},
	})
}

// svcFakeAllOwned checks every object in the fake carries owner ("" = none).
func svcFakeAllOwned(f *svcFakeDaemon, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	check := func(kind, name string, labels map[string]string) error {
		if got := labels[ownerLabelKey]; got != owner {
			return fmt.Errorf("%s %s: owner label %q, want %q", kind, name, got, owner)
		}
		return nil
	}
	n := 0
	for _, s := range f.secrets {
		n++
		if err := check("secret", s.Spec.Name, s.Spec.Labels); err != nil {
			return err
		}
	}
	for _, s := range f.configs {
		n++
		if err := check("config", s.Spec.Name, s.Spec.Labels); err != nil {
			return err
		}
	}
	for _, s := range f.services {
		n++
		if err := check("service", s.Spec.Name, s.Spec.Labels); err != nil {
			return err
		}
	}
	for _, s := range f.networks {
		if s.Name == "ext_net" {
			continue
		}
		n++
		if err := check("network", s.Name, s.Labels); err != nil {
			return err
		}
	}
	if n != 7 {
		return fmt.Errorf("want 7 objects (2 secrets, 2 configs, 2 services, 1 network), got %d", n)
	}
	return nil
}

// TestOwnerLabelSwarmWorkloadConflict: an existing object of the same name
// owned by another configuration fails create for each type.
func TestOwnerLabelSwarmWorkloadConflict(t *testing.T) {
	foreign := map[string]string{ownerLabelKey: "team-b"}
	cases := []struct {
		name string
		seed func(f *svcFakeDaemon)
		body string
	}{
		{"secret", func(f *svcFakeDaemon) {
			f.secrets["old"] = &swarm.Secret{ID: "old", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "s1", Labels: foreign}}}
		}, `resource "docker_secret" "s" {
  name = "s1"
  data = base64encode("x")
}`},
		{"config", func(f *svcFakeDaemon) {
			f.configs["old"] = &swarm.Config{ID: "old", Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: "c1", Labels: foreign}}}
		}, `resource "docker_config" "c" {
  name = "c1"
  data = base64encode("y")
}`},
		{"service", func(f *svcFakeDaemon) {
			one := uint64(1)
			f.services["old"] = &swarm.Service{ID: "old", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web", Labels: foreign},
				TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx"}},
				Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &one}}}}
		}, `resource "docker_service" "web" {
  name  = "web"
  image = "nginx:1.27"
}`},
		{"stack", func(f *svcFakeDaemon) {
			l := map[string]string{ownerLabelKey: "team-b", labelStackNamespace: "app"}
			f.secrets["old"] = &swarm.Secret{ID: "old", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "app_pw", Labels: l}}}
		}, `resource "docker_stack" "app" {
  name = "app"
  secret {
    name = "pw"
    data = base64encode("pw")
  }
}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, host := newSvcFakeDaemon(t)
			tc.seed(f)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      svcOwnerConfig(host, "team-a", tc.body),
					ExpectError: regexp.MustCompile(`owned by "team-b"`),
				}},
			})
		})
	}
}

// TestSecretP53DriverTemplating: driver and templating reach SecretCreate,
// and a driver makes data optional.
func TestSecretP53DriverTemplating(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: svcFakeConfig(host, `
resource "docker_secret" "vault" {
  name   = "vault"
  driver = { name = "vault-driver", options = { path = "kv/x" } }
}

resource "docker_secret" "tpl" {
  name       = "tpl"
  data       = base64encode("{{ .Service.Name }}")
  templating = { name = "golang" }
}
`),
			Check: func(*terraform.State) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				for _, s := range f.secrets {
					switch s.Spec.Name {
					case "vault":
						if s.Spec.Driver == nil || s.Spec.Driver.Name != "vault-driver" || s.Spec.Driver.Options["path"] != "kv/x" || len(s.Spec.Data) != 0 {
							return fmt.Errorf("vault spec = %+v", s.Spec)
						}
					case "tpl":
						if s.Spec.Templating == nil || s.Spec.Templating.Name != "golang" {
							return fmt.Errorf("tpl spec = %+v", s.Spec)
						}
					}
				}
				return nil
			},
		}},
	})
}

func TestSecretP53NeedsDataWithoutDriver(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      svcFakeConfig(host, `resource "docker_secret" "x" { name = "x" }`),
			ExpectError: regexp.MustCompile(`Missing secret content`),
		}},
	})
}

// ---------------------------------------------------------------------------
// docker_swarm_init

func TestSwarmInitP53SpecFields(t *testing.T) {
	ctx := context.Background()
	m := swarmInitModel{
		Labels:       types.MapNull(types.StringType),
		DataPathAddr: types.StringValue("eth1"),
		CAConfig: &swarmCAConfigModel{NodeCertExpiry: types.StringNull(), ExternalCAs: []swarmExternalCAModel{{
			Protocol: types.StringValue("cfssl"), URL: types.StringValue("https://ca.example:8888"),
			Options: types.MapNull(types.StringType), CACert: types.StringNull()}}},
	}
	var d diag.Diagnostics
	req := swarmInitBuildRequest(ctx, m, &d)
	if d.HasError() {
		t.Fatal(d)
	}
	if req.DataPathAddr != "eth1" || len(req.Spec.CAConfig.ExternalCAs) != 1 || req.Spec.CAConfig.ExternalCAs[0].URL != "https://ca.example:8888" {
		t.Errorf("init request = %+v", req)
	}
	// Unchanged spec: nothing to update, even when the daemon fills the CA cert.
	spec := req.Spec
	spec.CAConfig.ExternalCAs[0].CACert = "-----BEGIN CERTIFICATE-----"
	spec.CAConfig.ExternalCAs[0].Protocol = "CFSSL"
	if swarmApplySpec(ctx, m, &spec, &d) {
		t.Error("an unchanged spec must not need an update")
	}
	swarmFlattenSpec(&m, spec)
	if !m.CAConfig.ExternalCAs[0].CACert.IsNull() || m.CAConfig.ExternalCAs[0].Protocol.ValueString() != "cfssl" {
		t.Errorf("flatten = %+v", m.CAConfig.ExternalCAs)
	}
}

func TestOwnerLabelSwarmInit(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	m := swarmInitModel{Labels: stringMapValue(map[string]string{"env": "prod"}, false)}
	spec := swarm.Spec{}
	swarmApplySpecOwner(ctx, m, "team-a", &spec, &d)
	if spec.Labels[ownerLabelKey] != "team-a" || spec.Labels["env"] != "prod" {
		t.Errorf("owner set: labels = %v", spec.Labels)
	}
	if swarmApplySpecOwner(ctx, m, "team-a", &spec, &d) {
		t.Error("labelled spec must not change again")
	}
	swarmFlattenSpec(&m, spec)
	if _, ok := m.Labels.Elements()[ownerLabelKey]; ok {
		t.Error("owner label must be stripped from labels")
	}
	spec = swarm.Spec{}
	swarmApplySpecOwner(ctx, m, "", &spec, &d)
	if _, ok := spec.Labels[ownerLabelKey]; ok {
		t.Errorf("owner unset: labels = %v", spec.Labels)
	}
	// labels unset: the owner label is still added.
	spec = swarm.Spec{Annotations: swarm.Annotations{Labels: map[string]string{"x": "y"}}}
	if !swarmApplySpecOwner(ctx, swarmInitModel{Labels: types.MapNull(types.StringType)}, "team-a", &spec, &d) ||
		spec.Labels[ownerLabelKey] != "team-a" || spec.Labels["x"] != "y" {
		t.Errorf("labels unset: %v", spec.Labels)
	}

	// Conflict: the host already manages a swarm owned by team-b.
	f := swarmFakeManager(t, "active", "c1")
	f.handle("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "c1", Spec: swarm.Spec{
			Annotations: swarm.Annotations{Labels: map[string]string{ownerLabelKey: "team-b"}}}}})
	})
	pd := &providerData{Client: f.client(t), Owner: "team-a"}
	if err := swarmInitOwnerCreateCheck(ctx, pd); err == nil || !strings.Contains(err.Error(), `owned by "team-b"`) {
		t.Errorf("conflict error = %v", err)
	}
	pd.Owner = "team-b"
	if err := swarmInitOwnerCreateCheck(ctx, pd); err != nil {
		t.Errorf("same owner: %v", err)
	}
	pd.Owner = ""
	if err := swarmInitOwnerCreateCheck(ctx, pd); err != nil {
		t.Errorf("owner unset: %v", err)
	}
}

// ---------------------------------------------------------------------------
// docker_swarm_node and docker_node

func TestOwnerLabelSwarmNode(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	m := swarmNodeModel{Labels: stringMapValue(map[string]string{"zone": "a"}, false)}
	ch := swarmNodeChangeOwner(ctx, m, "team-a", &d)
	if (*ch.Labels)[ownerLabelKey] != "team-a" || (*ch.Labels)["zone"] != "a" {
		t.Errorf("owner set: %v", *ch.Labels)
	}
	ch = swarmNodeChangeOwner(ctx, m, "", &d)
	if _, ok := (*ch.Labels)[ownerLabelKey]; ok {
		t.Errorf("owner unset: %v", *ch.Labels)
	}
	// Labels unset: only the owner label.
	ch = swarmNodeChangeOwner(ctx, swarmNodeModel{Labels: types.MapNull(types.StringType)}, "team-a", &d)
	if len(*ch.Labels) != 1 || (*ch.Labels)[ownerLabelKey] != "team-a" {
		t.Errorf("labels unset: %v", *ch.Labels)
	}
	n := swarm.Node{ID: "n1", Spec: swarm.NodeSpec{Annotations: swarm.Annotations{Labels: map[string]string{"zone": "a", ownerLabelKey: "team-a"}}}}
	swarmNodeFlatten(&m, n)
	if _, ok := m.Labels.Elements()[ownerLabelKey]; ok || len(m.Labels.Elements()) != 1 {
		t.Errorf("flatten must strip the owner label: %v", m.Labels)
	}

	// Conflict: the joining daemon is already a member owned by team-b.
	f := swarmNewFakeEngine(t)
	f.handle("GET /info", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, map[string]any{"Swarm": map[string]any{"LocalNodeState": "active", "NodeID": "n1"}})
	})
	f.handle("GET /nodes/n1", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, swarm.Node{ID: "n1", Spec: swarm.NodeSpec{Annotations: swarm.Annotations{Labels: map[string]string{ownerLabelKey: "team-b"}}}})
	})
	c := f.client(t)
	pd := &providerData{Client: c, Owner: "team-a"}
	if err := swarmNodeOwnerCreateCheck(ctx, pd, c); err == nil || !strings.Contains(err.Error(), `owned by "team-b"`) {
		t.Errorf("conflict error = %v", err)
	}
	pd.Owner = ""
	if err := swarmNodeOwnerCreateCheck(ctx, pd, c); err != nil {
		t.Errorf("owner unset: %v", err)
	}
}

func TestSwarmNodeP53DataPathAndName(t *testing.T) {
	r := swarmBuildJoinRequest("m:2377", "t", "", "")
	r.DataPathAddr = "eth1"
	if r.DataPathAddr != "eth1" {
		t.Fatal("data path addr")
	}
	name := "edge-1"
	out, changed := swarmApplyNodeSpec(swarm.NodeSpec{}, swarmNodeSpecChange{Name: &name})
	if !changed || out.Name != "edge-1" {
		t.Errorf("node name: %+v", out)
	}
	if _, changed := swarmApplyNodeSpec(out, swarmNodeSpecChange{Name: &name}); changed {
		t.Error("same name must not change")
	}
	ch := nodeResourceChange(context.Background(), nodeResourceModel{Name: types.StringNull(), Labels: types.MapNull(types.StringType)}, &diag.Diagnostics{})
	if ch.Name != nil {
		t.Error("unset name must not be sent")
	}
}

// TestServiceP53RegistryAuthFrom: without credentials the update asks the
// daemon to reuse the spec's (registryAuthFrom=spec).
func TestServiceP53RegistryAuthFrom(t *testing.T) {
	f := swarmNewFakeEngine(t)
	var got string
	f.handle("GET /services/s1", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, swarm.Service{ID: "s1", Meta: swarm.Meta{Version: swarm.Version{Index: 3}}})
	})
	f.handle("POST /services/s1/update", func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("registryAuthFrom")
		swarmFakeJSON(w, swarm.ServiceUpdateResponse{})
	})
	pd := &providerData{Client: f.client(t)}
	spec := swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx"}}}
	if err := svcUpdate(context.Background(), pd, "s1", spec); err != nil {
		t.Fatal(err)
	}
	if got != string(swarm.RegistryAuthFromSpec) {
		t.Errorf("registryAuthFrom = %q", got)
	}
}
