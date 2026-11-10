package provider

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// containerP2Model is a plan using every Pass 2 and Pass 5 attribute.
func containerP2Model() containerResourceModel {
	str := types.StringValue
	i64 := types.Int64Value
	netType := types.ObjectType{AttrTypes: ctrNetworkAttrTypes}
	v6 := types.ObjectValueMust(ctrNetworkAttrTypes, map[string]attr.Value{
		"aliases": types.SetNull(types.StringType), "ipv4_address": types.StringNull(), "ipv6_address": str("fd00::10"),
		"link_local_ips": stringSetValue([]string{"169.254.0.5"}, false), "mac_address": str("02:42:ac:11:00:02"),
		"driver_opts": stringMapValue(map[string]string{"com.example.opt": "1"}, false), "gw_priority": i64(10), "links": types.ListNull(types.StringType),
		"network_id": str("n-v6net"), "endpoint_id": types.StringNull(), "gateway": types.StringNull(), "ip_address": types.StringNull(),
		"ip_prefix_len": i64(0), "ipv6_gateway": types.StringNull(), "global_ipv6_address": types.StringNull(), "global_ipv6_prefix_len": i64(0),
	})
	m := containerResourceModel{
		ID: types.StringValue("0123456789abcdef0123"), Name: str("p2"), Image: str("busybox:1.36"), ImageID: types.StringUnknown(),
		Command: types.ListNull(types.StringType), Entrypoint: types.ListNull(types.StringType), Env: types.MapNull(types.StringType),
		Labels: types.MapNull(types.StringType), Restart: str("no"), MaximumRetryCount: i64(0), Privileged: types.BoolValue(false),
		User: types.StringNull(), WorkingDir: types.StringNull(),
		Logging: &ctrLoggingModel{Driver: str("json-file"), Options: stringMapValue(map[string]string{"max-size": "10m"}, false)},
		Memory:  types.Int64Null(), MemorySwap: i64(0), CPUShares: types.Int64Null(), NanoCPUs: types.Int64Null(),
		Wait: types.BoolValue(false), WaitTimeout: str(containerDefaultWaitTimeout), Running: types.BoolValue(true),
		ExitCode: i64(0),
		Ports:    []containerPortModel{}, Ulimits: []containerUlimitModel{},
		Mounts: []containerMountModel{
			{Type: str("bind"), Source: str("/srv/a"), Target: str("/a"), ReadOnly: types.BoolValue(true), SelinuxRelabel: types.StringNull()},
			{Type: str("bind"), Source: str("/srv/b"), Target: str("/b"), ReadOnly: types.BoolValue(true), SelinuxRelabel: str("Z")},
			{Type: str("volume"), Source: str("vol"), Target: str("/v"), ReadOnly: types.BoolValue(false), SelinuxRelabel: str("z")},
			{Type: str("tmpfs"), Source: types.StringNull(), Target: str("/t"), ReadOnly: types.BoolValue(false), SelinuxRelabel: types.StringNull(),
				TmpfsOptions: &ctrTmpfsOptionsModel{SizeBytes: i64(1 << 20), Mode: str("1777"), Options: types.ListNull(ctrTmpfsOptionsType)}},
		},
		Files: []containerUploadModel{{
			Path: str("/etc/app.conf"), Content: str("x=1"), ContentBase64: types.StringNull(), Source: types.StringNull(),
			SourceHash: types.StringNull(), Mode: str("0640"), UID: i64(1000), GID: types.Int64Null(),
		}},
		Networks: types.MapValueMust(netType, map[string]attr.Value{"v6net": v6}),

		Hostname:   str("box"),
		DNS:        stringListValue([]string{"1.1.1.1", "8.8.8.8"}, false),
		DNSSearch:  stringListValue([]string{"corp.local"}, false),
		ExtraHosts: stringListValue([]string{"db.local:10.0.0.5", "host.docker.internal:host-gateway"}, false),
		Devices: []containerDeviceModel{
			{PathOnHost: str("/dev/fuse"), PathInContainer: types.StringNull(), CgroupPermissions: str("rwm")},
			{PathOnHost: str("/dev/sda"), PathInContainer: str("/dev/xvda"), CgroupPermissions: str("r")},
		},
		Tty:         types.BoolValue(true),
		StdinOpen:   types.BoolValue(true),
		StopSignal:  str("SIGQUIT"),
		StopTimeout: i64(30),
		Sysctls:     stringMapValue(map[string]string{"net.core.somaxconn": "1024"}, false),
		SecurityOpt: stringSetValue([]string{"no-new-privileges:true", "apparmor=unconfined"}, false),
		ReadOnly:    types.BoolValue(true),
		GPUs:        str("device=0,1"),

		CapAdd:               stringSetValue([]string{"NET_ADMIN"}, false),
		CapDrop:              stringSetValue([]string{"ALL"}, false),
		MemoryReservation:    i64(32 << 20),
		CPUPeriod:            i64(100000),
		CPUQuota:             i64(50000),
		CpusetCpus:           str("0-1"),
		Domainname:           str("corp.local"),
		DNSOptions:           stringListValue([]string{"ndots:2"}, false),
		DeviceCgroupRules:    stringListValue([]string{"c 1:3 mr"}, false),
		DeviceRequests:       []ctrDeviceRequestModel{{Driver: str("cdi"), Count: types.Int64Null(), DeviceIDs: stringListValue([]string{"vendor.com/dev=a"}, false), Capabilities: types.ListNull(types.ListType{ElemType: types.StringType}), Options: types.MapNull(types.StringType)}},
		BlkioDeviceReadBps:   []ctrThrottleModel{{Path: str("/dev/sda"), Rate: i64(1048576)}},
		BlkioDeviceWriteBps:  []ctrThrottleModel{},
		BlkioDeviceReadIOps:  []ctrThrottleModel{},
		BlkioDeviceWriteIOps: []ctrThrottleModel{{Path: str("/dev/sda"), Rate: i64(100)}},
		CgroupParent:         str("/tf"),
		CgroupnsMode:         str("private"),
		GroupAdd:             stringSetValue([]string{"audio"}, false),
		Init:                 types.BoolValue(true),
		IpcMode:              str("shareable"),
		NetworkMode:          str("v6net"),
		PidMode:              str("host"),
		Platform:             str("linux/amd64"),
		PublishAllPorts:      types.BoolValue(true),
		AutoRemove:           types.BoolValue(false),
		Runtime:              str("runc"),
		ShmSize:              i64(128 << 20),
		StorageOpt:           stringMapValue(map[string]string{"size": "20G"}, false),
		Tmpfs:                stringMapValue(map[string]string{"/run": "rw,size=64m"}, false),
		UsernsMode:           str("host"),
		VolumesFrom:          stringListValue([]string{"data:ro"}, false),
	}
	m.ctrP53Model = ctrP53Null()
	m.contractSetNull()
	return m
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
	if hc.LogConfig.Type == "" {
		hc.LogConfig.Type = "json-file"
	}
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
	cfg, hc, netCfg := containerBuildCreate(ctx, m, &diags)
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
	if len(hc.DeviceRequests) != 2 || hc.DeviceRequests[0].Driver != "nvidia" || !reflect.DeepEqual(hc.DeviceRequests[0].DeviceIDs, []string{"0", "1"}) ||
		!reflect.DeepEqual(hc.DeviceRequests[0].Capabilities, [][]string{{"gpu"}}) {
		t.Errorf("device requests = %+v", hc.DeviceRequests)
	}
	if ep := netCfg.EndpointsConfig["v6net"]; ep.IPAMConfig == nil || addrString(ep.IPAMConfig.IPv6Address) != "fd00::10" || addrString(ep.IPAMConfig.IPv4Address) != "" ||
		ep.GwPriority != 10 || ep.MacAddress.String() != "02:42:ac:11:00:02" || len(ep.IPAMConfig.LinkLocalIPs) != 1 || ep.DriverOpts["com.example.opt"] != "1" {
		t.Errorf("endpoint = %+v", ep)
	}
	if hc.NetworkMode != "v6net" || hc.CgroupParent != "/tf" || hc.CgroupnsMode != "private" || hc.CPUPeriod != 100000 || hc.CPUQuota != 50000 ||
		hc.CpusetCpus != "0-1" || hc.MemoryReservation != 32<<20 || cfg.Domainname != "corp.local" || hc.Init == nil || !*hc.Init ||
		hc.IpcMode != "shareable" || hc.PidMode != "host" || !hc.PublishAllPorts || hc.Runtime != "runc" || hc.ShmSize != 128<<20 ||
		hc.StorageOpt["size"] != "20G" || hc.Tmpfs["/run"] == "" || hc.UsernsMode != "host" || hc.VolumesFrom[0] != "data:ro" ||
		len(hc.CapAdd) != 1 || len(hc.CapDrop) != 1 || len(hc.BlkioDeviceReadBps) != 1 || len(hc.BlkioDeviceWriteIOps) != 1 ||
		hc.LogConfig.Type != "json-file" || hc.LogConfig.Config["max-size"] != "10m" || len(hc.DNSOptions) != 1 || len(hc.DeviceCgroupRules) != 1 ||
		len(hc.GroupAdd) != 1 {
		t.Errorf("gap host config: %+v", hc)
	}

	img := &container.Config{StopSignal: "SIGTERM"}
	got := containerFromInspect(containerP2Inspect(m, cfg, hc, netCfg), img, m)
	got.ImageID = m.ImageID
	if !reflect.DeepEqual(got, m) {
		gv, wv := reflect.ValueOf(got), reflect.ValueOf(m)
		if !reflect.DeepEqual(got.ctrP53Model, m.ctrP53Model) {
			t.Errorf("Pass 5.3 fields: got %#v want %#v", got.ctrP53Model, m.ctrP53Model)
		}
		for i := 0; i < gv.NumField(); i++ {
			if !gv.Field(i).CanInterface() {
				continue
			}
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
	m.CgroupnsMode, m.IpcMode, m.Runtime, m.ShmSize = types.StringNull(), types.StringNull(), types.StringNull(), types.Int64Null()
	m.Logging, m.Networks, m.NetworkMode = nil, types.MapNull(types.ObjectType{AttrTypes: ctrNetworkAttrTypes}), types.StringNull()
	cfg := &container.Config{Image: "busybox:1.36", Hostname: "0123456789ab", StopSignal: "SIGQUIT"}
	hc := &container.HostConfig{Sysctls: map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"},
		CgroupnsMode: "private", IpcMode: "private", Runtime: "runc", ShmSize: 64 << 20, NetworkMode: "bridge"}
	got := containerFromInspect(containerP2Inspect(m, cfg, hc, &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{"bridge": {}}}),
		&container.Config{StopSignal: "SIGQUIT"}, m)
	if !got.Hostname.IsNull() || !got.StopSignal.IsNull() || !got.Sysctls.IsNull() || !got.StopTimeout.IsNull() || !got.GPUs.IsNull() {
		t.Errorf("defaults leaked: hostname=%v stop=%v sysctls=%v timeout=%v gpus=%v", got.Hostname, got.StopSignal, got.Sysctls, got.StopTimeout, got.GPUs)
	}
	if len(got.Devices) != 0 || !got.DNS.IsNull() || !got.SecurityOpt.IsNull() {
		t.Errorf("devices/dns/secopt: %+v %v %v", got.Devices, got.DNS, got.SecurityOpt)
	}
	if !got.CgroupnsMode.IsNull() || !got.IpcMode.IsNull() || !got.Runtime.IsNull() || !got.ShmSize.IsNull() || got.Logging != nil || !got.Networks.IsNull() {
		t.Errorf("daemon defaults leaked: cgroupns=%v ipc=%v runtime=%v shm=%v logging=%+v networks=%v",
			got.CgroupnsMode, got.IpcMode, got.Runtime, got.ShmSize, got.Logging, got.Networks)
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
	if _, err := containerExpandDevices([]containerDeviceModel{{PathOnHost: types.StringValue("/dev/x"), CgroupPermissions: types.StringValue("rx")}}); err == nil {
		t.Error("bad permissions should fail")
	}
	if _, err := containerExpandDevices([]containerDeviceModel{{PathOnHost: types.StringValue("")}}); err == nil {
		t.Error("empty path_on_host should fail")
	}
	// Import: container_path equal to host_path stays null, different is reported.
	got := containerFlattenDevices([]container.DeviceMapping{{PathOnHost: "/dev/a", PathInContainer: "/dev/a"}, {PathOnHost: "/dev/b", PathInContainer: "/dev/c", CgroupPermissions: "rw"}}, nil)
	if len(got) != 2 || !got[0].PathInContainer.IsNull() || got[0].CgroupPermissions.ValueString() != "rwm" || got[1].PathInContainer.ValueString() != "/dev/c" {
		t.Errorf("flatten = %+v", got)
	}
	// Explicit container_path equal to host_path is kept.
	prior := []containerDeviceModel{{PathOnHost: types.StringValue("/dev/a"), PathInContainer: types.StringValue("/dev/a"), CgroupPermissions: types.StringValue("rwm")}}
	got = containerFlattenDevices([]container.DeviceMapping{{PathOnHost: "/dev/a", PathInContainer: "/dev/a", CgroupPermissions: "rwm"}}, prior)
	if got[0].PathInContainer.ValueString() != "/dev/a" {
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
