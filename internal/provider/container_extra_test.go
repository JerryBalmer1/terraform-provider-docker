package provider

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// containerP2Model is a plan using every Pass 2 attribute.
func containerP2Model() containerResourceModel {
	str := types.StringValue
	return containerResourceModel{
		ID: types.StringValue("0123456789abcdef0123"), Name: str("p2"), Image: str("busybox:1.36"), ImageID: types.StringUnknown(),
		Command: types.ListNull(types.StringType), Entrypoint: types.ListNull(types.StringType), Env: types.MapNull(types.StringType),
		Labels: types.MapNull(types.StringType), Restart: str("no"), MaxRetryCount: types.Int64Value(0), Privileged: types.BoolValue(false),
		User: types.StringNull(), WorkingDir: types.StringNull(), LogDriver: str("json-file"), LogOpts: types.MapNull(types.StringType),
		Memory: types.Int64Null(), MemorySwap: types.Int64Value(0), CPUShares: types.Int64Null(), NanoCPUs: types.Int64Null(),
		MustRun: types.BoolValue(true), Wait: types.BoolValue(false), WaitTimeout: str(containerDefaultWaitTimeout), Running: types.BoolValue(true),
		Ports: []containerPortModel{}, Ulimits: []containerUlimitModel{},
		Mounts: []containerMountModel{
			{Type: str("bind"), Source: str("/srv/a"), Target: str("/a"), ReadOnly: types.BoolValue(true), TmpfsSize: types.Int64Null(), SelinuxRelabel: types.StringNull()},
			{Type: str("bind"), Source: str("/srv/b"), Target: str("/b"), ReadOnly: types.BoolValue(true), TmpfsSize: types.Int64Null(), SelinuxRelabel: str("Z")},
			{Type: str("volume"), Source: str("vol"), Target: str("/v"), ReadOnly: types.BoolValue(false), TmpfsSize: types.Int64Null(), SelinuxRelabel: str("z")},
		},
		Upload: []containerUploadModel{{
			File: str("/etc/app.conf"), Content: str("x=1"), ContentBase64: types.StringNull(), Source: types.StringNull(),
			SourceHash: types.StringNull(), Executable: types.BoolValue(false), Permissions: str("0640"), Owner: types.Int64Value(1000), Group: types.Int64Null(),
		}},
		NetworksAdvanced: []containerNetworkModel{{Name: str("v6net"), Aliases: types.SetNull(types.StringType),
			IPv4Address: types.StringNull(), IPv6Address: str("fd00::10")}},

		Hostname:   str("box"),
		DNS:        stringListValue([]string{"1.1.1.1", "8.8.8.8"}, false),
		DNSSearch:  stringListValue([]string{"corp.local"}, false),
		ExtraHosts: stringListValue([]string{"db.local:10.0.0.5", "host.docker.internal:host-gateway"}, false),
		Devices: []containerDeviceModel{
			{HostPath: str("/dev/fuse"), ContainerPath: types.StringNull(), Permissions: str("rwm")},
			{HostPath: str("/dev/sda"), ContainerPath: str("/dev/xvda"), Permissions: str("r")},
		},
		Tty:         types.BoolValue(true),
		StdinOpen:   types.BoolValue(true),
		StopSignal:  str("SIGQUIT"),
		StopTimeout: types.Int64Value(30),
		Sysctls:     stringMapValue(map[string]string{"net.core.somaxconn": "1024"}, false),
		SecurityOpt: stringSetValue([]string{"no-new-privileges:true", "apparmor=unconfined"}, false),
		ReadOnly:    types.BoolValue(true),
		GPUs:        str("device=0,1"),
	}
}

// containerP2Inspect simulates ContainerInspect after creating cfg/hc.
func containerP2Inspect(m containerResourceModel, cfg *container.Config, hc *container.HostConfig, netCfg *network.NetworkingConfig) container.InspectResponse {
	eps := map[string]*network.EndpointSettings{}
	if netCfg != nil {
		for k, v := range netCfg.EndpointsConfig {
			cp := *v
			cp.NetworkID = "n-" + k
			eps[k] = &cp
		}
	}
	hc.LogConfig.Type = "json-file"
	return container.InspectResponse{

		ID: m.ID.ValueString(), Name: "/" + m.Name.ValueString(), Image: "sha256:img",
		State: &container.State{Running: true}, HostConfig: hc,
		Config:          cfg,
		NetworkSettings: &container.NetworkSettings{Networks: eps},
	}
}

func TestContainerP2ExpandFlattenRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := containerP2Model()
	var diags diag.Diagnostics
	cfg, hc, netCfg, _ := containerBuildCreate(ctx, m, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if cfg.Hostname != "box" || !cfg.Tty || !cfg.OpenStdin || cfg.StopSignal != "SIGQUIT" || cfg.StopTimeout == nil || *cfg.StopTimeout != 30 {
		t.Errorf("config: %+v", cfg)
	}
	if !reflect.DeepEqual(addrStrings(hc.DNS), []string{"1.1.1.1", "8.8.8.8"}) || hc.DNSSearch[0] != "corp.local" || len(hc.ExtraHosts) != 2 ||
		hc.Sysctls["net.core.somaxconn"] != "1024" || len(hc.SecurityOpt) != 2 || !hc.ReadonlyRootfs {
		t.Errorf("host config: %+v", hc)
	}
	if want := []container.DeviceMapping{{PathOnHost: "/dev/fuse", PathInContainer: "/dev/fuse", CgroupPermissions: "rwm"},
		{PathOnHost: "/dev/sda", PathInContainer: "/dev/xvda", CgroupPermissions: "r"}}; !reflect.DeepEqual(hc.Devices, want) {
		t.Errorf("devices = %+v", hc.Devices)
	}
	if len(hc.DeviceRequests) != 1 || hc.DeviceRequests[0].Driver != "nvidia" || !reflect.DeepEqual(hc.DeviceRequests[0].DeviceIDs, []string{"0", "1"}) ||
		!reflect.DeepEqual(hc.DeviceRequests[0].Capabilities, [][]string{{"gpu"}}) {
		t.Errorf("device requests = %+v", hc.DeviceRequests)
	}
	if ep := netCfg.EndpointsConfig["v6net"]; ep.IPAMConfig == nil || addrString(ep.IPAMConfig.IPv6Address) != "fd00::10" || addrString(ep.IPAMConfig.IPv4Address) != "" {
		t.Errorf("endpoint = %+v", ep)
	}

	img := &container.Config{StopSignal: "SIGTERM"}
	got := containerFromInspect(containerP2Inspect(m, cfg, hc, netCfg), img, m)
	got.ImageID = m.ImageID
	if !reflect.DeepEqual(got, m) {
		gv, wv := reflect.ValueOf(got), reflect.ValueOf(m)
		for i := 0; i < gv.NumField(); i++ {
			if !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
				t.Errorf("field %s: got %#v want %#v", gv.Type().Field(i).Name, gv.Field(i).Interface(), wv.Field(i).Interface())
			}
		}
	}
}

// TestContainerP2DaemonDefaultsNotReported: with none of the Pass 2
// attributes set, daemon defaults (hostname = ID prefix, image STOPSIGNAL,
// daemon-added sysctls) must not show up.
func TestContainerP2DaemonDefaultsNotReported(t *testing.T) {
	m := containerP2Model()
	m.Hostname, m.StopSignal, m.StopTimeout, m.GPUs = types.StringNull(), types.StringNull(), types.Int64Null(), types.StringNull()
	m.DNS, m.DNSSearch, m.ExtraHosts = types.ListNull(types.StringType), types.ListNull(types.StringType), types.ListNull(types.StringType)
	m.Sysctls, m.SecurityOpt = types.MapNull(types.StringType), types.SetNull(types.StringType)
	m.Devices = []containerDeviceModel{}
	cfg := &container.Config{Image: "busybox:1.36", Hostname: "0123456789ab", StopSignal: "SIGQUIT"}
	hc := &container.HostConfig{Sysctls: map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"}}
	got := containerFromInspect(containerP2Inspect(m, cfg, hc, nil), &container.Config{StopSignal: "SIGQUIT"}, m)
	if !got.Hostname.IsNull() || !got.StopSignal.IsNull() || !got.Sysctls.IsNull() || !got.StopTimeout.IsNull() || !got.GPUs.IsNull() {
		t.Errorf("defaults leaked: hostname=%v stop=%v sysctls=%v timeout=%v gpus=%v", got.Hostname, got.StopSignal, got.Sysctls, got.StopTimeout, got.GPUs)
	}
	if len(got.Devices) != 0 || !got.DNS.IsNull() || !got.SecurityOpt.IsNull() {
		t.Errorf("devices/dns/secopt: %+v %v %v", got.Devices, got.DNS, got.SecurityOpt)
	}

	// Import (null image in prior) reports a non-default hostname and every sysctl.
	imp := containerResourceModel{ID: m.ID, Image: types.StringNull(), Hostname: types.StringNull(), Sysctls: types.MapNull(types.StringType)}
	cfg.Hostname = "custom"
	got = containerFromInspect(containerP2Inspect(m, cfg, hc, nil), nil, imp)
	if got.Hostname.ValueString() != "custom" || len(got.Sysctls.Elements()) != 1 {
		t.Errorf("import: hostname=%v sysctls=%v", got.Hostname, got.Sysctls)
	}
}

func TestContainerGPUs(t *testing.T) {
	cases := map[string]container.DeviceRequest{
		"all":          {Driver: "nvidia", Count: -1, Capabilities: [][]string{{"gpu"}}},
		"2":            {Driver: "nvidia", Count: 2, Capabilities: [][]string{{"gpu"}}},
		"device=GPU-a": {Driver: "nvidia", DeviceIDs: []string{"GPU-a"}, Capabilities: [][]string{{"gpu"}}},
	}
	for in, want := range cases {
		got, err := containerExpandGPUs(in)
		if err != nil || !reflect.DeepEqual(*got, want) {
			t.Errorf("%q: got %+v, %v", in, got, err)
			continue
		}
		if back := containerFlattenGPUs([]container.DeviceRequest{*got}, types.StringNull()); back.ValueString() != in {
			t.Errorf("%q flattened to %v", in, back)
		}
	}
	for _, bad := range []string{"0", "-1", "x", "device="} {
		if _, err := containerExpandGPUs(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	// Prior spelling kept when equivalent; non-GPU requests ignored.
	req := []container.DeviceRequest{{Driver: "other", Capabilities: [][]string{{"tpu"}}}, {Count: 1, Capabilities: [][]string{{"gpu", "utility"}}}}
	if got := containerFlattenGPUs(req, types.StringValue(" 1")); got.ValueString() != " 1" {
		t.Errorf("prior spelling: %v", got)
	}
	if got := containerFlattenGPUs(nil, types.StringValue("all")); !got.IsNull() {
		t.Errorf("no request should be null: %v", got)
	}
}

func TestContainerDevicesValidation(t *testing.T) {
	if _, err := containerExpandDevices([]containerDeviceModel{{HostPath: types.StringValue("/dev/x"), Permissions: types.StringValue("rx")}}); err == nil {
		t.Error("bad permissions should fail")
	}
	if _, err := containerExpandDevices([]containerDeviceModel{{HostPath: types.StringValue("")}}); err == nil {
		t.Error("empty host_path should fail")
	}
	// Import: container_path equal to host_path stays null, different is reported.
	got := containerFlattenDevices([]container.DeviceMapping{{PathOnHost: "/dev/a", PathInContainer: "/dev/a"}, {PathOnHost: "/dev/b", PathInContainer: "/dev/c", CgroupPermissions: "rw"}}, nil)
	if len(got) != 2 || !got[0].ContainerPath.IsNull() || got[0].Permissions.ValueString() != "rwm" || got[1].ContainerPath.ValueString() != "/dev/c" {
		t.Errorf("flatten = %+v", got)
	}
	// Explicit container_path equal to host_path is kept.
	prior := []containerDeviceModel{{HostPath: types.StringValue("/dev/a"), ContainerPath: types.StringValue("/dev/a"), Permissions: types.StringValue("rwm")}}
	got = containerFlattenDevices([]container.DeviceMapping{{PathOnHost: "/dev/a", PathInContainer: "/dev/a", CgroupPermissions: "rwm"}}, prior)
	if got[0].ContainerPath.ValueString() != "/dev/a" {
		t.Errorf("explicit container_path dropped: %+v", got)
	}
}

func TestContainerExtraHostsValidation(t *testing.T) {
	m := containerP2Model()
	m.ExtraHosts = stringListValue([]string{"nocolon"}, false)
	var diags diag.Diagnostics
	containerBuildCreate(context.Background(), m, &diags)
	if !diags.HasError() {
		t.Error("extra_hosts without ':' should fail")
	}
}

func TestContainerSecurityOptRead(t *testing.T) {
	prior := stringSetValue([]string{"b", "a"}, false)
	if got := containerSecurityOptRead(prior, []string{"a", "b"}); !got.Equal(prior) {
		t.Errorf("same entries: %v", got)
	}
	if got := containerSecurityOptRead(prior, []string{"a"}); len(got.Elements()) != 1 {
		t.Errorf("drift: %v", got)
	}
}

// TestContainerModelMatchesSchema writes the model into framework state and
// reads it back, catching tfsdk tag/schema mismatches.
func TestContainerModelMatchesSchema(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	NewContainerResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatal(sr.Diagnostics)
	}
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	in := containerP2Model()
	in.ImageID = types.StringValue("sha256:img")
	if d := st.Set(ctx, &in); d.HasError() {
		t.Fatalf("set: %v", d)
	}
	var out containerResourceModel
	if d := st.Get(ctx, &out); d.HasError() {
		t.Fatalf("get: %v", d)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("model/schema round trip mismatch")
	}
}

// TestContainerExamplePlan plans examples/resources/docker_container with no
// daemon so the example stays valid against the schema.
func TestContainerExamplePlan(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "resources", "docker_container", "resource.tf"))
	if err != nil {
		t.Fatal(err)
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    []tfresource.TestStep{{Config: string(b), PlanOnly: true, ExpectNonEmptyPlan: true}},
	})
}
