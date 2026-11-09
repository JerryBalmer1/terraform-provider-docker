package provider

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
)

func svcTestLookup() *svcLookup {
	return &svcLookup{
		networks: []namedObject{{ID: "net111", Name: "frontend"}, {ID: "net222", Name: "app_frontend"}, {ID: "net333", Name: "backend"}},
		secrets:  []namedObject{{ID: "sec111", Name: "db_password"}, {ID: "sec222", Name: "app_db_password"}},
		configs:  []namedObject{{ID: "cfg111", Name: "nginx_conf"}},
	}
}

func svcTestModel() svcSpecModel {
	str := types.StringValue
	i64 := types.Int64Value
	return svcSpecModel{
		Name:     str("web"),
		Image:    str("nginx:1.27"),
		Mode:     str("replicated"),
		Replicas: i64(3),
		Env:      stringMapValue(map[string]string{"A": "1", "B": "two=2"}, false),
		Args:     stringListValue([]string{"-g", "daemon off;"}, false),
		Command:  stringListValue([]string{"nginx"}, false),
		Networks: svcNetworksValue(types.MapNull(types.ObjectType{AttrTypes: svcNetworkAttachAttrTypes}), map[string]svcNetworkAttachModel{
			"frontend": svcNetworkAttachment(nil, nil), "net333": svcNetworkAttachment([]string{"api"}, map[string]string{"com.example.opt": "1"}),
		}),
		Labels:          stringMapValue(map[string]string{"team": "web"}, false),
		ContainerLabels: types.MapNull(types.StringType),
		Mounts: []svcMountModel{
			{Type: str("volume"), Source: str("data"), Target: str("/data"), ReadOnly: types.BoolValue(false),
				VolumeOptions: &svcVolumeOptionsModel{NoCopy: types.BoolValue(true), Labels: types.MapNull(types.StringType),
					DriverConfig: &svcDriverConfigModel{Name: str("local"), Options: stringMapValue(map[string]string{"type": "nfs"}, false)}}},
			{Type: str("tmpfs"), Source: types.StringNull(), Target: str("/tmp"), ReadOnly: types.BoolValue(true),
				TmpfsOptions: &svcTmpfsOptionsModel{SizeBytes: i64(1 << 20), Mode: i64(0o700)}},
			{Type: str("bind"), Source: str("/srv/data"), Target: str("/srv"), ReadOnly: types.BoolValue(false),
				BindOptions: &svcBindOptionsModel{Propagation: str("rslave")}},
		},
		EndpointSpec: &svcEndpointModel{Mode: str("vip"), Ports: []svcPortModel{
			{Name: str("http"), TargetPort: i64(80), PublishedPort: i64(8080), Protocol: str("tcp"), PublishMode: str("ingress")},
			{Name: types.StringNull(), TargetPort: i64(53), PublishedPort: types.Int64Null(), Protocol: str("udp"), PublishMode: str("host")},
		}},
		Secrets: []svcSecretRefModel{
			{SecretID: str("db_password"), SecretName: types.StringNull(), FileName: types.StringNull(), UID: str("0"), GID: str("0"), Mode: i64(0o444)},
			{SecretID: str("sec111"), SecretName: str("db_password"), FileName: str("pw"), UID: str("1000"), GID: str("1000"), Mode: i64(0o400)},
		},
		Configs: []svcConfigRefModel{
			{ConfigID: str("nginx_conf"), ConfigName: types.StringNull(), FileName: str("/etc/nginx/nginx.conf"), UID: str("0"), GID: str("0"), Mode: i64(0o444)},
		},
		UpdateConfig: &svcUpdateModel{Parallelism: i64(2), Delay: str("10s"), FailureAction: str("rollback"), Monitor: str("1m"),
			MaxFailureRatio: types.Float64Value(0.1), Order: str("start-first")},
		RollbackConfig: &svcUpdateModel{Parallelism: i64(1), Delay: types.StringNull(), FailureAction: str("pause"), Monitor: types.StringNull(),
			MaxFailureRatio: types.Float64Null(), Order: str("stop-first")},
		RestartPolicy: &svcRestartModel{Condition: str("on-failure"), Delay: str("5s"), MaxAttempts: i64(3), Window: str("2m")},
		Placement: &svcPlacementModel{Constraints: stringListValue([]string{"node.role==worker"}, false),
			Preferences: stringListValue([]string{"spread=node.labels.zone"}, false), MaxReplicas: i64(2),
			Platforms: []svcPlatformModel{{Architecture: str("amd64"), OS: str("linux")}}},
		Resources: &svcResourcesModel{
			Limits: &svcResourceAmountModel{NanoCPUs: i64(500000000), MemoryBytes: i64(268435456)},
			Reservations: &svcReservationModel{NanoCPUs: types.Int64Null(), MemoryBytes: i64(134217728), GenericResources: []svcGenericResourceModel{
				{NamedResourceSpec: &svcNamedResourceModel{Kind: str("GPU"), Value: str("uuid-1")}},
				{DiscreteResourceSpec: &svcDiscreteResourceModel{Kind: str("SSD"), Value: i64(2)}},
			}},
		},
		Healthcheck: &svcHealthModel{Test: stringListValue([]string{"CMD", "curl", "-f", "http://localhost/"}, false),
			Interval: str("30s"), Timeout: str("5s"), StartPeriod: types.StringNull(), Retries: i64(3)},
		Logging:        &svcLoggingModel{Driver: str("json-file"), Options: stringMapValue(map[string]string{"max-size": "10m"}, false)},
		ConvergeConfig: &svcConvergeModel{Timeout: str("3m"), Delay: str("5s")},

		MaxConcurrent:    types.Int64Null(),
		TotalCompletions: types.Int64Null(),
		Hostname:         str("{{.Task.Name}}"),
		User:             str("101:101"),
		Dir:              str("/srv"),
		Hosts:            stringListValue([]string{"db.local:10.0.0.5", "v6.local:fd00::1"}, false),
		DNSConfig: &svcDNSModel{Nameservers: stringListValue([]string{"10.0.0.2"}, false), Search: types.ListNull(types.StringType),
			Options: stringListValue([]string{"ndots:2"}, false)},
		CapAdd:          stringSetValue([]string{"NET_ADMIN"}, false),
		CapDrop:         types.SetNull(types.StringType),
		Sysctls:         stringMapValue(map[string]string{"net.core.somaxconn": "1024"}, false),
		Ulimits:         []svcUlimitModel{{Name: str("nofile"), Soft: i64(1024), Hard: i64(2048)}},
		StopGracePeriod: str("30s"),
		ForceUpdate:     i64(2),

		Groups:     stringListValue([]string{"audio"}, false),
		Init:       types.BoolValue(true),
		Isolation:  types.StringNull(),
		ReadOnly:   types.BoolValue(true),
		StopSignal: str("SIGQUIT"),
		Runtime:    types.StringNull(),
		Privileges: &svcPrivilegesModel{SELinuxContext: &svcSELinuxContextModel{Disable: types.BoolNull(), User: types.StringNull(),
			Role: types.StringNull(), Type: str("svirt_lxc_net_t"), Level: str("s0:c100,c200")}},
	}
}

// svcP2Nulls sets every Pass 2 attribute of m to its typed null.
func svcP2Nulls(m *svcSpecModel) {
	m.MaxConcurrent, m.TotalCompletions, m.ForceUpdate = types.Int64Null(), types.Int64Null(), types.Int64Null()
	m.Hostname, m.User, m.Dir, m.StopGracePeriod = types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull()
	m.Hosts = types.ListNull(types.StringType)
	m.CapAdd, m.CapDrop = types.SetNull(types.StringType), types.SetNull(types.StringType)
	m.Sysctls = types.MapNull(types.StringType)
	m.DNSConfig, m.Ulimits = nil, nil
	m.Groups = types.ListNull(types.StringType)
	m.Init, m.ReadOnly = types.BoolNull(), types.BoolNull()
	m.Isolation, m.StopSignal, m.Runtime = types.StringNull(), types.StringNull(), types.StringNull()
	m.Privileges = nil
}

// svcSimulateDaemon mimics what the daemon and client do to a submitted
// spec: digest pinning, platforms, MaxAttempts always set.
func svcSimulateDaemon(spec swarm.ServiceSpec) swarm.ServiceSpec {
	spec.TaskTemplate.ContainerSpec.Image = "nginx:1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if spec.TaskTemplate.Placement == nil {
		spec.TaskTemplate.Placement = &swarm.Placement{}
	}
	spec.TaskTemplate.Placement.Platforms = []swarm.Platform{{OS: "linux", Architecture: "amd64"}}
	if rp := spec.TaskTemplate.RestartPolicy; rp != nil && rp.MaxAttempts == nil {
		zero := uint64(0)
		rp.MaxAttempts = &zero
	}
	return spec
}

func TestServiceExpandFlattenRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := svcTestModel()
	lk := svcTestLookup()
	spec, diags := svcExpand(ctx, &m, lk, svcExpandOpts{FullName: "web"})
	if diags.HasError() {
		t.Fatalf("expand: %v", diags)
	}
	// Spot checks on the spec.
	cs := spec.TaskTemplate.ContainerSpec
	if got := cs.Env; !reflect.DeepEqual(got, []string{"A=1", "B=two=2"}) {
		t.Errorf("env = %v", got)
	}
	if spec.Labels[labelManagedBy] != labelManagedByValue || spec.Labels["team"] != "web" {
		t.Errorf("labels = %v", spec.Labels)
	}
	if *spec.Mode.Replicated.Replicas != 3 {
		t.Errorf("replicas = %d", *spec.Mode.Replicated.Replicas)
	}
	var targets []string
	for _, n := range spec.TaskTemplate.Networks {
		targets = append(targets, n.Target)
	}
	if !reflect.DeepEqual(targets, []string{"net111", "net333"}) {
		t.Errorf("network targets = %v", targets)
	}
	if s := cs.Secrets[0]; s.SecretID != "sec111" || s.SecretName != "db_password" || s.File.Name != "db_password" || s.File.Mode != 0o444 {
		t.Errorf("secret[0] = %+v %+v", s, s.File)
	}
	if c := cs.Configs[0]; c.ConfigID != "cfg111" || c.File.Name != "/etc/nginx/nginx.conf" {
		t.Errorf("config[0] = %+v", c)
	}
	if cs.Mounts[0].VolumeOptions == nil || cs.Mounts[0].VolumeOptions.DriverConfig.Options["type"] != "nfs" {
		t.Errorf("volume mount = %+v", cs.Mounts[0])
	}
	if cs.Mounts[1].TmpfsOptions == nil || cs.Mounts[1].TmpfsOptions.SizeBytes != 1<<20 || cs.Mounts[1].TmpfsOptions.Mode != 0o700 {
		t.Errorf("tmpfs mount = %+v", cs.Mounts[1])
	}
	if !cs.Mounts[0].VolumeOptions.NoCopy || cs.Mounts[2].BindOptions == nil || cs.Mounts[2].BindOptions.Propagation != mount.PropagationRSlave {
		t.Errorf("mount options = %+v / %+v", cs.Mounts[0].VolumeOptions, cs.Mounts[2].BindOptions)
	}
	if n := spec.TaskTemplate.Networks[1]; !reflect.DeepEqual(n.Aliases, []string{"api"}) || n.DriverOpts["com.example.opt"] != "1" {
		t.Errorf("network attachment = %+v", n)
	}
	if cs.Init == nil || !*cs.Init || !cs.ReadOnly || cs.StopSignal != "SIGQUIT" || !reflect.DeepEqual(cs.Groups, []string{"audio"}) {
		t.Errorf("gap attributes = init %v ro %v sig %q groups %v", cs.Init, cs.ReadOnly, cs.StopSignal, cs.Groups)
	}
	if p := cs.Privileges; p == nil || p.SELinuxContext == nil || p.SELinuxContext.Level != "s0:c100,c200" || p.CredentialSpec != nil {
		t.Errorf("privileges = %+v", p)
	}
	if g := spec.TaskTemplate.Resources.Reservations.GenericResources; len(g) != 2 || g[0].NamedResourceSpec.Value != "uuid-1" || g[1].DiscreteResourceSpec.Value != 2 {
		t.Errorf("generic resources = %+v", g)
	}
	if spec.EndpointSpec.Ports[0].Name != "http" {
		t.Errorf("port name = %q", spec.EndpointSpec.Ports[0].Name)
	}
	if spec.UpdateConfig.Monitor != time.Minute || spec.UpdateConfig.MaxFailureRatio != float32(0.1) {
		t.Errorf("update config = %+v", spec.UpdateConfig)
	}
	if p := spec.TaskTemplate.Placement.Preferences[0]; p.Spread.SpreadDescriptor != "node.labels.zone" {
		t.Errorf("preference = %+v", p)
	}
	if spec.TaskTemplate.Resources.Limits.MemoryBytes != 268435456 {
		t.Errorf("resources = %+v", spec.TaskTemplate.Resources)
	}
	if cs.Healthcheck.Interval != 30*time.Second || cs.Healthcheck.Retries != 3 {
		t.Errorf("healthcheck = %+v", cs.Healthcheck)
	}

	got := svcFlatten(svcSimulateDaemon(spec), m, lk, svcFlattenOpts{})
	if !reflect.DeepEqual(got, m) {
		t.Errorf("round trip mismatch\n got: %+v\nwant: %+v", got, m)
		svcDiffFields(t, got, m)
	}
}

func svcDiffFields(t *testing.T, got, want svcSpecModel) {
	t.Helper()
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := 0; i < gv.NumField(); i++ {
		if !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
			t.Errorf("field %s: got %#v want %#v", gv.Type().Field(i).Name, gv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}
}

func TestServiceFlattenMinimalNoSpuriousBlocks(t *testing.T) {
	ctx := context.Background()
	m := svcSpecModel{
		Name: types.StringValue("api"), Image: types.StringValue("nginx"), Mode: types.StringValue("global"), Replicas: types.Int64Value(1),
		Env: types.MapNull(types.StringType), Args: types.ListNull(types.StringType), Command: types.ListNull(types.StringType),
		Networks: types.MapNull(types.ObjectType{AttrTypes: svcNetworkAttachAttrTypes}), Labels: types.MapValueMust(types.StringType, map[string]attr.Value{}), ContainerLabels: types.MapNull(types.StringType),
	}
	svcP2Nulls(&m)
	spec, diags := svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if spec.Mode.Global == nil || spec.Name != "api" {
		t.Fatalf("spec = %+v", spec)
	}
	daemon := svcSimulateDaemon(spec)
	daemon.TaskTemplate.ContainerSpec.Image = "nginx:latest@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	daemon.EndpointSpec = &swarm.EndpointSpec{Mode: swarm.ResolutionModeVIP}
	got := svcFlatten(daemon, m, svcTestLookup(), svcFlattenOpts{})
	if !reflect.DeepEqual(got, m) {
		svcDiffFields(t, got, m)
	}
}

func TestServiceImportFlattenShowsDaemonValues(t *testing.T) {
	r := uint64(2)
	spec := swarm.ServiceSpec{
		Annotations:  swarm.Annotations{Name: "x", Labels: map[string]string{labelManagedBy: labelManagedByValue, "k": "v"}},
		Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &r}},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "redis:7@sha256:abc"}, Networks: []swarm.NetworkAttachmentConfig{{Target: "net333"}}},
	}
	got := svcFlatten(spec, svcSpecModel{}, svcTestLookup(), svcFlattenOpts{})
	if got.Image.ValueString() != "redis:7@sha256:abc" || got.Replicas.ValueInt64() != 2 {
		t.Errorf("got %+v", got)
	}
	if lbl := got.Labels.Elements(); len(lbl) != 1 {
		t.Errorf("labels = %v", lbl)
	}
	if n := got.Networks.Elements(); len(n) != 1 || n["net333"] == nil {
		t.Errorf("networks = %v", n)
	}
}

func TestServiceImageDigestStripping(t *testing.T) {
	cases := []struct {
		want, daemon string
		match        bool
	}{
		{"nginx", "nginx:latest@sha256:aaaa", true},
		{"nginx:1.27", "nginx:1.27@sha256:aaaa", true},
		{"docker.io/library/nginx:1.27", "nginx:1.27@sha256:aaaa", true},
		{"ghcr.io/org/app:v1", "ghcr.io/org/app:v1@sha256:bbbb", true},
		{"nginx:1.27", "nginx:1.28@sha256:aaaa", false},
		{"nginx@sha256:aaaa", "nginx@sha256:aaaa", true},
		{"nginx@sha256:aaaa", "nginx@sha256:bbbb", false},
		{"localhost:5000/app", "localhost:5000/app:latest", true},
	}
	for _, c := range cases {
		if got := svcImageMatches(c.want, c.daemon); got != c.match {
			t.Errorf("svcImageMatches(%q, %q) = %v, want %v", c.want, c.daemon, got, c.match)
		}
	}
	if got := svcStripDigest("nginx:1@sha256:abc"); got != "nginx:1" {
		t.Errorf("strip = %q", got)
	}
	if got := svcImageValue(types.StringValue("nginx"), "nginx:latest@sha256:abc"); got.ValueString() != "nginx" {
		t.Errorf("image value = %v", got)
	}
	if got := svcImageValue(types.StringValue("nginx:1"), "nginx:2@sha256:abc"); got.ValueString() != "nginx:2@sha256:abc" {
		t.Errorf("drifted image value = %v", got)
	}
}

func TestServiceDurationValue(t *testing.T) {
	if got := svcDurValue(types.StringValue("1m"), time.Minute); got.ValueString() != "1m" {
		t.Errorf("kept spelling: %v", got)
	}
	if got := svcDurValue(types.StringValue("1m"), 2*time.Minute); got.ValueString() != "2m" {
		t.Errorf("drift: %v", got)
	}
	for d, want := range map[time.Duration]string{time.Hour: "1h", 90 * time.Minute: "1h30m", 61 * time.Second: "1m1s", 1500 * time.Millisecond: "1.5s"} {
		if got := svcFormatDuration(d); got != want {
			t.Errorf("format %v = %q want %q", d, got, want)
		}
	}
	if got := svcDurValue(types.StringNull(), 0); !got.IsNull() {
		t.Errorf("zero with null prior: %v", got)
	}
	if got := svcDurValue(types.StringNull(), 5*time.Second); got.ValueString() != "5s" {
		t.Errorf("import: %v", got)
	}
}

func TestServiceLookupStackPrefix(t *testing.T) {
	lk := svcTestLookup()
	if id, _ := lk.resolve("network", "frontend"); id != "net111" {
		t.Errorf("no prefix: %s", id)
	}
	lk.stackPrefix = "app_"
	if id, _ := lk.resolve("network", "frontend"); id != "net222" {
		t.Errorf("stack-local name should win: %s", id)
	}
	if id, _ := lk.resolve("network", "backend"); id != "net333" {
		t.Errorf("external name: %s", id)
	}
	if id, _ := lk.resolve("secret", "db_password"); id != "sec222" {
		t.Errorf("stack secret: %s", id)
	}
	if !lk.matches("network", "frontend", "net222") || lk.matches("network", "frontend", "net111") {
		t.Error("matches")
	}
	if got := lk.displayRef("network", "net222"); got != "frontend" {
		t.Errorf("displayRef stack = %q", got)
	}
	if got := lk.displayRef("network", "net333"); got != "net333" {
		t.Errorf("displayRef external = %q", got)
	}
	if _, err := lk.resolve("config", "nope"); err == nil || !isNotFound(err) {
		t.Errorf("missing: %v", err)
	}
}

func TestServiceDefaultFileName(t *testing.T) {
	if got := svcDefaultFileName("db_pw", "id1", "app_db_pw"); got != "db_pw" {
		t.Errorf("by name: %q", got)
	}
	if got := svcDefaultFileName("id1", "id1", "app_db_pw"); got != "app_db_pw" {
		t.Errorf("by id: %q", got)
	}
	if got := svcDefaultFileName("id", "id1234", "x"); got != "x" {
		t.Errorf("by id prefix: %q", got)
	}
}

func TestServiceExpandErrors(t *testing.T) {
	ctx := context.Background()
	m := svcTestModel()
	m.Placement.Preferences = stringListValue([]string{"pack=node.id"}, false)
	m.Networks = svcNetworksValue(types.MapNull(types.ObjectType{AttrTypes: svcNetworkAttachAttrTypes}), map[string]svcNetworkAttachModel{"missing": svcNetworkAttachment(nil, nil)})
	m.UpdateConfig.Delay = types.StringValue("soon")
	_, diags := svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{})
	if diags.ErrorsCount() != 3 {
		t.Errorf("want 3 errors, got %v", diags)
	}
}

func TestServiceMountVolumeOptionsOnlyWhenSet(t *testing.T) {
	ctx := context.Background()
	m := svcTestModel()
	m.Mounts = []svcMountModel{{Type: types.StringValue("volume"), Source: types.StringValue("v"), Target: types.StringValue("/v"),
		ReadOnly: types.BoolValue(false)}}
	spec, _ := svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{})
	if mt := spec.TaskTemplate.ContainerSpec.Mounts[0]; mt.VolumeOptions != nil || mt.Type != mount.TypeVolume {
		t.Errorf("mount = %+v", mt)
	}
}

// TestServiceModelMatchesSchema checks the Go models line up with the
// schemas by writing them into framework state and reading them back.
func TestServiceModelMatchesSchema(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	NewServiceResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatal(sr.Diagnostics)
	}
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	in := serviceResourceModel{ID: types.StringValue("abc"), svcSpecModel: svcTestModel()}
	in.svcResourceComputedNull()
	if d := st.Set(ctx, &in); d.HasError() {
		t.Fatalf("set: %v", d)
	}
	var out serviceResourceModel
	if d := st.Get(ctx, &out); d.HasError() {
		t.Fatalf("get: %v", d)
	}
	if !reflect.DeepEqual(in.svcSpecModel, out.svcSpecModel) {
		svcDiffFields(t, out.svcSpecModel, in.svcSpecModel)
	}
}
