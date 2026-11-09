package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

func TestContainerExpandPorts(t *testing.T) {
	in := []containerPortModel{
		{ContainerPort: types.Int64Value(80), HostPort: types.Int64Value(8080), HostIP: types.StringNull(), Protocol: types.StringValue("tcp")},
		{ContainerPort: types.Int64Value(53), HostPort: types.Int64Null(), HostIP: types.StringValue("127.0.0.1"), Protocol: types.StringValue("udp")},
		{ContainerPort: types.Int64Value(80), HostPort: types.Int64Value(8081), HostIP: types.StringValue("10.0.0.1"), Protocol: types.StringNull()},
	}
	set, pm, err := containerExpandPorts(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 2 {
		t.Errorf("exposed: %v", set)
	}
	if got := pm[network.MustParsePort("80/tcp")]; len(got) != 2 || got[0].HostPort != "8080" || addrString(got[1].HostIP) != "10.0.0.1" {
		t.Errorf("80/tcp bindings: %+v", got)
	}
	if got := pm[network.MustParsePort("53/udp")]; len(got) != 1 || got[0].HostPort != "" || addrString(got[0].HostIP) != "127.0.0.1" {
		t.Errorf("53/udp bindings: %+v", got)
	}

	// Round trip keeps the prior order.
	back := containerFlattenPorts(pm, in)
	if len(back) != 3 {
		t.Fatalf("flatten: %+v", back)
	}
	if back[0].HostPort.ValueInt64() != 8080 || back[1].ContainerPort.ValueInt64() != 53 || !back[1].HostPort.IsNull() ||
		back[2].HostIP.ValueString() != "10.0.0.1" || back[2].Protocol.ValueString() != "tcp" || !back[0].HostIP.IsNull() {
		t.Errorf("flatten order/values: %+v", back)
	}

	if _, _, err := containerExpandPorts([]containerPortModel{{ContainerPort: types.Int64Value(0)}}); err == nil {
		t.Error("port 0 should fail")
	}
	if s, m, err := containerExpandPorts(nil); s != nil || m != nil || err != nil {
		t.Error("nil ports should give nil maps")
	}
}

func TestContainerFlattenPortsImport(t *testing.T) {
	pm := network.PortMap{network.MustParsePort("443/tcp"): {{HostPort: "443"}}, network.MustParsePort("80/tcp"): {{HostPort: "80"}}}
	got := containerFlattenPorts(pm, nil)
	if len(got) != 2 || got[0].ContainerPort.ValueInt64() != 80 || got[1].ContainerPort.ValueInt64() != 443 {
		t.Errorf("import order should be sorted by port: %+v", got)
	}
	if got := containerFlattenPorts(nil, nil); got == nil || len(got) != 0 {
		t.Error("empty flatten must be a non-nil empty slice")
	}
}

func TestContainerExpandMounts(t *testing.T) {
	in := []containerMountModel{
		{Type: types.StringValue("bind"), Source: types.StringValue("/srv"), Target: types.StringValue("/data"), ReadOnly: types.BoolValue(true),
			BindOptions: &ctrBindOptionsModel{Propagation: types.StringValue("rshared")}},
		{Type: types.StringValue("volume"), Source: types.StringValue("vol1"), Target: types.StringValue("/vol"), ReadOnly: types.BoolValue(false),
			VolumeOptions: &ctrVolumeOptionsModel{NoCopy: types.BoolValue(true), Labels: stringMapValue(map[string]string{"a": "b"}, false), Subpath: types.StringValue("sub"),
				DriverConfig: &ctrDriverConfigModel{Name: types.StringValue("local"), Options: types.MapNull(types.StringType)}}},
		{Type: types.StringValue("tmpfs"), Source: types.StringNull(), Target: types.StringValue("/tmp"), ReadOnly: types.BoolValue(false),
			TmpfsOptions: &ctrTmpfsOptionsModel{SizeBytes: types.Int64Value(1 << 20), Mode: types.StringValue("1777")}},
	}
	out, err := containerExpandMounts(in)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Type != mount.TypeBind || !out[0].ReadOnly || out[2].TmpfsOptions == nil || out[2].TmpfsOptions.SizeBytes != 1<<20 ||
		ctrModeBits(out[2].TmpfsOptions.Mode) != 0o1777 || out[0].BindOptions.Propagation != "rshared" ||
		!out[1].VolumeOptions.NoCopy || out[1].VolumeOptions.Subpath != "sub" || out[1].VolumeOptions.DriverConfig.Name != "local" {
		t.Errorf("expand: %+v", out)
	}
	back := containerFlattenMounts(out, nil, in)
	for i := range in {
		if !back[i].Type.Equal(in[i].Type) || !back[i].Source.Equal(in[i].Source) || !back[i].Target.Equal(in[i].Target) ||
			!back[i].ReadOnly.Equal(in[i].ReadOnly) {
			t.Errorf("round trip %d: got %+v want %+v", i, back[i], in[i])
		}
	}
	if back[2].TmpfsOptions.Mode.ValueString() != "1777" || back[2].TmpfsOptions.SizeBytes.ValueInt64() != 1<<20 ||
		back[0].BindOptions.Propagation.ValueString() != "rshared" || !back[1].VolumeOptions.NoCopy.ValueBool() ||
		back[1].VolumeOptions.DriverConfig.Name.ValueString() != "local" || len(back[1].VolumeOptions.Labels.Elements()) != 1 {
		t.Errorf("round trip options: %+v %+v %+v", back[0].BindOptions, back[1].VolumeOptions, back[2].TmpfsOptions)
	}

	bad := [][]containerMountModel{
		{{Type: types.StringValue("bind"), Target: types.StringValue("/x")}},
		{{Type: types.StringValue("tmpfs"), Source: types.StringValue("s"), Target: types.StringValue("/x")}},
		{{Type: types.StringValue("volume"), Target: types.StringValue("/x"), TmpfsOptions: &ctrTmpfsOptionsModel{SizeBytes: types.Int64Value(5)}}},
		{{Type: types.StringValue("volume"), Target: types.StringValue("/x"), BindOptions: &ctrBindOptionsModel{Propagation: types.StringValue("shared")}}},
		{{Type: types.StringValue("bind"), Source: types.StringValue("/s"), Target: types.StringValue("/x"), VolumeOptions: &ctrVolumeOptionsModel{}}},
		{{Type: types.StringValue("tmpfs"), Target: types.StringValue("/x"), TmpfsOptions: &ctrTmpfsOptionsModel{Mode: types.StringValue("999")}}},
		{{Type: types.StringValue("nfs"), Target: types.StringValue("/x")}},
		{{Type: types.StringValue("volume")}},
	}
	for i, b := range bad {
		if _, err := containerExpandMounts(b); err == nil {
			t.Errorf("bad mount %d should fail", i)
		}
	}
}

func TestContainerUlimits(t *testing.T) {
	in := []containerUlimitModel{{Name: types.StringValue("nproc"), Soft: types.Int64Value(10), Hard: types.Int64Value(20)}}
	out := containerExpandUlimits(in)
	if len(out) != 1 || out[0].Name != "nproc" || out[0].Soft != 10 || out[0].Hard != 20 {
		t.Fatalf("expand: %+v", out)
	}
	daemon := append(out, &container.Ulimit{Name: "nofile", Soft: 1024, Hard: 4096})
	if got := containerFlattenUlimits(daemon, in, false); len(got) != 1 || got[0].Name.ValueString() != "nproc" {
		t.Errorf("daemon defaults should be filtered: %+v", got)
	}
	if got := containerFlattenUlimits(daemon, nil, true); len(got) != 2 || got[0].Name.ValueString() != "nofile" {
		t.Errorf("import should report all, sorted: %+v", got)
	}
}

func TestContainerHealthcheck(t *testing.T) {
	h := &containerHealthcheckModel{
		Test:          types.ListNull(types.StringType),
		Interval:      types.StringValue("1m"),
		Timeout:       types.StringValue("5s"),
		StartPeriod:   types.StringNull(),
		StartInterval: types.StringValue("2s"),
		Retries:       types.Int64Value(3),
	}
	hc, err := containerExpandHealthcheck([]string{"CMD", "true"}, h)
	if err != nil {
		t.Fatal(err)
	}
	if hc.Interval != time.Minute || hc.Timeout != 5*time.Second || hc.StartPeriod != 0 || hc.StartInterval != 2*time.Second || hc.Retries != 3 {
		t.Errorf("expand: %+v", hc)
	}
	back := containerFlattenHealthcheck(hc, nil, h)
	if back.Interval.ValueString() != "1m" || back.Timeout.ValueString() != "5s" || !back.StartPeriod.IsNull() || back.StartInterval.ValueString() != "2s" || back.Retries.ValueInt64() != 3 {
		t.Errorf("flatten should keep prior spelling: %+v", back)
	}
	// Import (no prior): durations in Go canonical form.
	imp := containerFlattenHealthcheck(hc, nil, nil)
	if imp.Interval.ValueString() != "1m0s" {
		t.Errorf("import interval: %s", imp.Interval)
	}
	// The image's own healthcheck is not reported when the user set none.
	if got := containerFlattenHealthcheck(hc, hc, nil); got != nil {
		t.Errorf("image default healthcheck should be subtracted: %+v", got)
	}
	if _, err := containerExpandHealthcheck([]string{"CMD"}, &containerHealthcheckModel{Interval: types.StringValue("soon")}); err == nil {
		t.Error("bad duration should fail")
	}
	if hc, _ := containerExpandHealthcheck(nil, nil); hc != nil {
		t.Error("nil block gives nil")
	}
}

func TestContainerEnv(t *testing.T) {
	s := containerEnvToSlice(map[string]string{"B": "2", "A": "1=x"})
	if len(s) != 2 || s[0] != "A=1=x" || s[1] != "B=2" {
		t.Errorf("to slice: %v", s)
	}
	m := containerEnvToMap([]string{"A=1=x", "FLAG"})
	if m["A"] != "1=x" || m["FLAG"] != "" {
		t.Errorf("to map: %v", m)
	}
	got := containerSubtractDefaults(
		map[string]string{"PATH": "/usr/bin", "LANG": "C", "APP": "1", "HOME": "/override"},
		map[string]string{"PATH": "/usr/bin", "LANG": "C", "HOME": "/root"},
		map[string]bool{"LANG": true}, false)
	if len(got) != 3 || got["APP"] != "1" || got["LANG"] != "C" || got["HOME"] != "/override" {
		t.Errorf("subtract: %v", got)
	}
	only := containerSubtractDefaults(map[string]string{"PATH": "/x", "APP": "1"}, nil, map[string]bool{"APP": true}, true)
	if len(only) != 1 || only["APP"] != "1" {
		t.Errorf("onlyKeep: %v", only)
	}
}

func TestContainerFlattenNetworks(t *testing.T) {
	id := "0123456789abcdef0123"
	eps := map[string]*network.EndpointSettings{
		"bridge": {NetworkID: "b1"},
		"app-net": {NetworkID: "n1aaaaaaaaaaaaaaaa", Aliases: []string{"web", "0123456789ab", "c1"}, IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: mustAddr("10.1.0.5")},
			IPAddress: mustAddr("10.1.0.5"), IPPrefixLen: 24, Gateway: mustAddr("10.1.0.1"), EndpointID: "e1", DriverOpts: map[string]string{"k": "v"}, GwPriority: 5},
		"other": {NetworkID: "n2"},
	}
	n1 := ctrNullNetwork()
	prior := types.MapValueMust(types.ObjectType{AttrTypes: ctrNetworkAttrTypes}, map[string]attr.Value{"n1aaaaaaaaaa": ctrNetworkObject(t, n1)})
	got := ctrFlattenNetworks(eps, "default", false, id, "c1", prior)
	els := got.Elements()
	if len(els) != 2 || els["n1aaaaaaaaaa"] == nil || els["other"] == nil {
		t.Fatalf("want app-net (by ID prefix) + other, got %v", got)
	}
	var m ctrNetworkModel
	if d := els["n1aaaaaaaaaa"].(types.Object).As(context.Background(), &m, basetypes.ObjectAsOptions{}); d.HasError() {
		t.Fatal(d)
	}
	if m.IPv4Address.ValueString() != "10.1.0.5" || len(m.Aliases.Elements()) != 1 || m.IPAddress.ValueString() != "10.1.0.5" ||
		m.IPPrefixLen.ValueInt64() != 24 || m.Gateway.ValueString() != "10.1.0.1" || m.EndpointID.ValueString() != "e1" ||
		m.GwPriority.ValueInt64() != 5 || len(m.DriverOpts.Elements()) != 1 {
		t.Errorf("first: %+v", m)
	}
	// No prior and only the default network: null.
	if got := ctrFlattenNetworks(map[string]*network.EndpointSettings{"bridge": {}}, "bridge", false, id, "c1", types.MapNull(types.ObjectType{AttrTypes: ctrNetworkAttrTypes})); !got.IsNull() {
		t.Errorf("default network should not be reported: %v", got)
	}
	// network_mode set by the user to a network: that endpoint is not drift.
	if got := ctrFlattenNetworks(map[string]*network.EndpointSettings{"app": {}}, "app", true, id, "c1", types.MapNull(types.ObjectType{AttrTypes: ctrNetworkAttrTypes})); !got.IsNull() {
		t.Errorf("network_mode endpoint should not be reported: %v", got)
	}
	if !containerIsDefaultNetwork("bridge", "default") || containerIsDefaultNetwork("bridge", "app-net") || !containerIsDefaultNetwork("host", "host") {
		t.Error("containerIsDefaultNetwork")
	}
}

func TestContainerCapabilities(t *testing.T) {
	prior := stringSetValue([]string{"NET_ADMIN"}, false)
	if got := containerCapSet(prior, []string{"NET_ADMIN"}, []string{"CAP_NET_ADMIN"}); !got.Equal(prior) {
		t.Errorf("normalised caps should keep prior spelling: %v", got)
	}
	if got := containerCapSet(types.SetNull(types.StringType), nil, nil); !got.IsNull() {
		t.Error("no caps and no prior gives null")
	}
}

func TestContainerFromInspectSubtractsImageDefaults(t *testing.T) {
	img := &container.Config{
		Env:         []string{"PATH=/usr/bin", "NGINX_VERSION=1.27"},
		Cmd:         []string{"nginx", "-g", "daemon off;"},
		Entrypoint:  []string{"/docker-entrypoint.sh"},
		Labels:      map[string]string{"maintainer": "nginx"},
		WorkingDir:  "/",
		Healthcheck: &container.HealthConfig{Test: []string{"CMD", "true"}},
	}
	insp := container.InspectResponse{
		ID: "abc", Name: "/web", Image: "sha256:1",
		State: &container.State{Running: false, Status: "exited"},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
			LogConfig:     container.LogConfig{Type: "json-file"},
			PortBindings:  network.PortMap{network.MustParsePort("80/tcp"): {{HostPort: "8080"}}},
			NetworkMode:   "default",
		},
		Config: &container.Config{
			Image:       "nginx:1.27",
			Env:         []string{"PATH=/usr/bin", "NGINX_VERSION=1.27", "APP=1"},
			Cmd:         img.Cmd,
			Entrypoint:  img.Entrypoint,
			Labels:      map[string]string{"maintainer": "nginx", "team": "web", labelManagedBy: labelManagedByValue},
			WorkingDir:  "/",
			Healthcheck: img.Healthcheck,
		},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{"bridge": {NetworkID: "b"}}},
	}

	// Import: everything null but id.
	prior := containerResourceModel{
		ID: types.StringValue("web"), Image: types.StringNull(), Env: types.MapNull(types.StringType),
		Labels: types.MapNull(types.StringType), Command: types.ListNull(types.StringType), Entrypoint: types.ListNull(types.StringType),
		Memory: types.Int64Null(), CPUShares: types.Int64Null(), NanoCPUs: types.Int64Null(), MaximumRetryCount: types.Int64Null(),
		User: types.StringNull(), WorkingDir: types.StringNull(), Networks: types.MapNull(types.ObjectType{AttrTypes: ctrNetworkAttrTypes}),
		MustRun: types.BoolNull(), Wait: types.BoolNull(), WaitTimeout: types.StringNull(), Running: types.BoolNull(),
	}
	got := containerFromInspect(insp, img, prior)
	if got.ID.ValueString() != "abc" || got.Name.ValueString() != "web" || got.Image.ValueString() != "nginx:1.27" {
		t.Errorf("identity: %v %v %v", got.ID, got.Name, got.Image)
	}
	if env := got.Env.Elements(); len(env) != 1 || env["APP"].(types.String).ValueString() != "1" {
		t.Errorf("env should only hold APP: %v", got.Env)
	}
	if l := got.Labels.Elements(); len(l) != 1 || l["team"] == nil {
		t.Errorf("labels should only hold team: %v", got.Labels)
	}
	if !got.Command.IsNull() || !got.Entrypoint.IsNull() || !got.WorkingDir.IsNull() || got.Healthcheck != nil {
		t.Errorf("image defaults leaked: cmd=%v ep=%v wd=%v hc=%+v", got.Command, got.Entrypoint, got.WorkingDir, got.Healthcheck)
	}
	if got.Restart.ValueString() != "unless-stopped" || got.Running.ValueBool() || !got.MustRun.ValueBool() || got.WaitTimeout.ValueString() != containerDefaultWaitTimeout {
		t.Errorf("restart/running/defaults: %v %v %v %v", got.Restart, got.Running, got.MustRun, got.WaitTimeout)
	}
	if len(got.Ports) != 1 || !got.Networks.IsNull() || !got.Memory.IsNull() {
		t.Errorf("ports/networks/memory: %+v %v %v", got.Ports, got.Networks, got.Memory)
	}
	if got.Logging == nil || got.Logging.Driver.ValueString() != "json-file" {
		t.Errorf("import reports logging: %+v", got.Logging)
	}

	// A user who explicitly sets a value equal to the image default keeps it.
	prior.Image = types.StringValue("nginx:1.27")
	prior.Env = stringMapValue(map[string]string{"NGINX_VERSION": "1.27"}, false)
	prior.Command = stringListValue(img.Cmd, false)
	got = containerFromInspect(insp, img, prior)
	if env := got.Env.Elements(); len(env) != 2 || env["NGINX_VERSION"] == nil {
		t.Errorf("explicit env equal to image default must be kept: %v", got.Env)
	}
	if got.Command.IsNull() {
		t.Error("explicit command equal to image default must be kept")
	}

	// Image could not be inspected: only keys already in state are reported.
	got = containerFromInspect(insp, nil, prior)
	if env := got.Env.Elements(); len(env) != 1 || env["NGINX_VERSION"] == nil {
		t.Errorf("without image config only prior keys: %v", got.Env)
	}
}

func TestContainerReadHelpers(t *testing.T) {
	if !containerInt64Read(types.Int64Null(), 0).IsNull() || containerInt64Read(types.Int64Value(0), 0).IsNull() {
		t.Error("containerInt64Read")
	}
	if !containerStringRead(types.StringNull(), "root", "root").IsNull() || containerStringRead(types.StringValue("root"), "root", "root").IsNull() {
		t.Error("containerStringRead")
	}
	empty := stringMapValue(nil, false)
	if containerMapValue(empty, nil).IsNull() || !containerMapValue(types.MapNull(types.StringType), nil).IsNull() {
		t.Error("containerMapValue keeps a known empty map")
	}
	rp := containerRestartPolicy("on-failure", 3)
	if rp.Name != "on-failure" || rp.MaximumRetryCount != 3 || containerRestartPolicy("", 3).MaximumRetryCount != 0 {
		t.Errorf("restart policy: %+v", rp)
	}
	o := containerOrderByPrior([]string{"c", "a"}, []string{"a", "b", "c", "d"}, func(s string) string { return s })
	if len(o) != 4 || o[0] != "c" || o[1] != "a" || o[2] != "b" || o[3] != "d" {
		t.Errorf("order: %v", o)
	}
}

// ctrNetworkObject converts a networks element model to its object value.
func ctrNetworkObject(t *testing.T, m ctrNetworkModel) types.Object {
	t.Helper()
	nullStr := func(v types.String) types.String {
		if v.IsNull() || v.IsUnknown() {
			return types.StringNull()
		}
		return v
	}
	nullInt := func(v types.Int64) types.Int64 {
		if v.IsNull() || v.IsUnknown() {
			return types.Int64Null()
		}
		return v
	}
	m.NetworkID, m.EndpointID, m.Gateway, m.IPAddress = nullStr(m.NetworkID), nullStr(m.EndpointID), nullStr(m.Gateway), nullStr(m.IPAddress)
	m.IPv6Gateway, m.GlobalIPv6Address = nullStr(m.IPv6Gateway), nullStr(m.GlobalIPv6Address)
	m.IPPrefixLen, m.GlobalIPv6PrefixLen = nullInt(m.IPPrefixLen), nullInt(m.GlobalIPv6PrefixLen)
	o, d := types.ObjectValueFrom(context.Background(), ctrNetworkAttrTypes, m)
	if d.HasError() {
		t.Fatal(d)
	}
	return o
}
