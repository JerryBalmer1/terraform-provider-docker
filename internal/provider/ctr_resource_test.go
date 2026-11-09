package provider

// Pass 5 docker_container tests: networks (Q1) connect/disconnect in place,
// the gap attributes, the outputs contract, delete, import and read of a
// removed container.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

func ctrSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	var sr resource.SchemaResponse
	NewContainerResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatal(sr.Diagnostics)
	}
	return sr
}

func ctrState(t *testing.T, sr resource.SchemaResponse, m containerResourceModel) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(context.Background()), nil)}
	if d := st.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return st
}

func ctrNetMap(t *testing.T, nets map[string]ctrNetworkModel) types.Map {
	vals := map[string]attr.Value{}
	for k, n := range nets {
		vals[k] = ctrNetworkObject(t, n)
	}
	return types.MapValueMust(types.ObjectType{AttrTypes: ctrNetworkAttrTypes}, vals)
}

// TestCtrSchema checks the Pass 5 shape: renamed and added attributes exist,
// the old names are gone, networks is not RequiresReplace, every attribute
// has a description, and RequiresReplace ones say "Forces replacement.".
func TestCtrSchema(t *testing.T) {
	sr := ctrSchema(t)
	if d := sr.Schema.ValidateImplementation(context.Background()); d.HasError() {
		t.Fatal(d)
	}
	a, b := sr.Schema.Attributes, sr.Schema.Blocks
	for _, name := range []string{"networks", "maximum_retry_count", "cap_add", "cap_drop", "cgroup_parent", "cgroupns_mode", "cpu_period",
		"cpu_quota", "cpuset_cpus", "device_cgroup_rules", "dns_options", "domainname", "exit_code", "group_add", "init", "ipc_mode",
		"memory_reservation", "network_mode", "pid_mode", "platform", "publish_all_ports", "auto_remove", "runtime", "shm_size",
		"storage_opt", "tmpfs", "userns_mode", "volumes_from", "inspect_json", "inspect", "engine_api_version", "docker_server_version",
		"content_sha256", "id", "name", "image_id", "labels"} {
		if _, ok := a[name]; !ok {
			t.Errorf("attribute %s missing", name)
		}
	}
	for _, name := range []string{"files", "logging", "device_requests", "blkio_device_read_bps", "blkio_device_write_bps",
		"blkio_device_read_iops", "blkio_device_write_iops", "ports", "devices", "mounts"} {
		if _, ok := b[name]; !ok {
			t.Errorf("block %s missing", name)
		}
	}
	for _, old := range []string{"networks_advanced", "max_retry_count", "log_driver", "log_opts", "capabilities", "upload", "network_data"} {
		if _, ok := a[old]; ok {
			t.Errorf("old attribute %s still present", old)
		}
		if _, ok := b[old]; ok {
			t.Errorf("old block %s still present", old)
		}
	}
	nets := a["networks"].(schema.MapNestedAttribute)
	if len(nets.PlanModifiers) != 0 {
		t.Error("networks must not force replacement")
	}
	ports := b["ports"].(schema.ListNestedBlock).NestedObject.Attributes
	for _, n := range []string{"container_port", "host_port", "host_ip", "protocol"} {
		if _, ok := ports[n]; !ok {
			t.Errorf("ports.%s missing", n)
		}
	}
	mounts := b["mounts"].(schema.ListNestedBlock).NestedObject
	for _, n := range []string{"tmpfs_options", "bind_options", "volume_options"} {
		if _, ok := mounts.Blocks[n]; !ok {
			t.Errorf("mounts.%s missing", n)
		}
	}
	if _, ok := mounts.Attributes["tmpfs_size"]; ok {
		t.Error("mounts.tmpfs_size should be gone")
	}
	files := b["files"].(schema.ListNestedBlock).NestedObject.Attributes
	for _, n := range []string{"path", "content", "content_base64", "source", "source_hash", "mode", "uid", "gid"} {
		if _, ok := files[n]; !ok {
			t.Errorf("files.%s missing", n)
		}
	}
	for name, at := range a {
		desc := at.GetMarkdownDescription()
		if desc == "" {
			t.Errorf("%s: no description", name)
		}
		if ctrHasRequiresReplace(at) && !strings.Contains(desc, "Forces replacement.") && !strings.Contains(desc, "forces a new resource") {
			t.Errorf("%s: RequiresReplace without \"Forces replacement.\": %q", name, desc)
		}
	}
}

// ctrHasRequiresReplace reports a plain RequiresReplace (not RequiresReplaceIf)
// on a string, bool, int64, list, set or map attribute.
func ctrHasRequiresReplace(a schema.Attribute) bool {
	desc := func(mods []string) bool {
		for _, d := range mods {
			if strings.Contains(d, "the resource will be replaced") || strings.Contains(d, "will force replacement") {
				return true
			}
		}
		return false
	}
	ctx := context.Background()
	var ds []string
	add := func(m interface{ Description(context.Context) string }) { ds = append(ds, m.Description(ctx)) }
	switch v := a.(type) {
	case schema.StringAttribute:
		for _, m := range v.PlanModifiers {
			add(m)
		}
	case schema.BoolAttribute:
		for _, m := range v.PlanModifiers {
			add(m)
		}
	case schema.Int64Attribute:
		for _, m := range v.PlanModifiers {
			add(m)
		}
	case schema.ListAttribute:
		for _, m := range v.PlanModifiers {
			add(m)
		}
	case schema.SetAttribute:
		for _, m := range v.PlanModifiers {
			add(m)
		}
	case schema.MapAttribute:
		for _, m := range v.PlanModifiers {
			add(m)
		}
	}
	return desc(ds)
}

// TestCtrNetworksUpdateInPlace: an added network is connected, a removed one
// disconnected, a changed one reconnected, all without replacement.
func TestCtrNetworksUpdateInPlace(t *testing.T) {
	ctx := context.Background()
	f, pd := ctrFake(t)
	r := &containerResource{pd: pd}
	sr := ctrSchema(t)

	plan := ctrUploadModel()
	plan.Files = []containerUploadModel{}
	front, back := ctrNullNetwork(), ctrNullNetwork()
	front.Aliases = stringSetValue([]string{"www"}, false)
	plan.Networks = ctrNetMap(t, map[string]ctrNetworkModel{"front": front, "back": back})
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: ctrPlan(t, sr, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "create,start" {
		t.Errorf("create calls = %s (endpoints are configured at create, no connect)", got)
	}
	if f.created.HostConfig.NetworkMode != "back" || len(f.created.NetworkingConfig.EndpointsConfig) != 2 {
		t.Errorf("create network config: mode=%s eps=%v", f.created.HostConfig.NetworkMode, f.created.NetworkingConfig.EndpointsConfig)
	}
	var state containerResourceModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &state)...)
	got := ctrNetworksFrom(ctx, state.Networks, &resp.Diagnostics)
	if len(got) != 2 || got["front"].IPAddress.ValueString() != "10.9.0.2" || got["front"].NetworkID.ValueString() != "n-front" ||
		len(got["front"].Aliases.Elements()) != 1 {
		t.Fatalf("networks state: %+v", got)
	}

	// Plan: drop back, change front's aliases, add side.
	f.calls = nil
	next := state
	front2 := got["front"]
	front2.Aliases = stringSetValue([]string{"www", "web"}, false)
	front2.IPAddress, front2.EndpointID = types.StringUnknown(), types.StringUnknown()
	side := ctrNullNetwork()
	side.GwPriority = types.Int64Value(3)
	next.Networks = ctrNetMap(t, map[string]ctrNetworkModel{"front": front2, "side": side})
	next.contractSetUnknown()
	st := ctrState(t, sr, state)
	uresp := resource.UpdateResponse{State: st}
	r.Update(ctx, resource.UpdateRequest{Plan: ctrPlan(t, sr, next), State: st}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "disconnect:n-back,disconnect:n-front,connect:front,connect:side" {
		t.Errorf("update calls = %s", got)
	}
	var after containerResourceModel
	uresp.Diagnostics.Append(uresp.State.Get(ctx, &after)...)
	if n := ctrNetworksFrom(ctx, after.Networks, &uresp.Diagnostics); len(n) != 2 || n["side"].GwPriority.ValueInt64() != 3 || len(n["front"].Aliases.Elements()) != 2 {
		t.Errorf("networks after update: %+v", n)
	}
}

func TestCtrNetInputsEqual(t *testing.T) {
	a := ctrNullNetwork()
	b := ctrNullNetwork()
	b.Aliases = stringSetValue(nil, false) // known empty == null
	b.GwPriority = types.Int64Value(0)
	b.MacAddress = types.StringValue("02:42:ac:11:00:02") // computed on b, unset on a
	if !ctrNetInputsEqual(a, b) {
		t.Error("null/empty/zero inputs should be equal; unset mac ignored")
	}
	a.MacAddress = types.StringValue("02:42:ac:11:00:03")
	if ctrNetInputsEqual(a, b) {
		t.Error("a set mac_address that differs is a change")
	}
	a = ctrNullNetwork()
	a.IPv4Address = types.StringValue("10.0.0.5")
	if ctrNetInputsEqual(a, b) {
		t.Error("ipv4 change")
	}
	if _, err := ctrEndpointFromModel(context.Background(), ctrNetworkModel{MacAddress: types.StringValue("nope")}, nil); err == nil {
		t.Error("bad mac should fail")
	}
}

// TestCtrNetKeepState: the computed endpoint values keep their state while
// the element's inputs are unchanged and go unknown when they change.
func TestCtrNetKeepState(t *testing.T) {
	ctx := context.Background()
	sr := ctrSchema(t)
	m := containerP2Model()
	st := ctrState(t, sr, m)
	p := path.Root("networks").AtMapKey("v6net").AtName("network_id")

	planM := m
	n := ctrNetworksFrom(ctx, m.Networks, nil)["v6net"]
	n.NetworkID = types.StringUnknown()
	planM.Networks = ctrNetMap(t, map[string]ctrNetworkModel{"v6net": n})
	planM.Networks = ctrForceUnknown(t, planM.Networks, "v6net", "network_id")
	plan := ctrPlan(t, sr, planM)
	cfg := tfsdk.Config{Schema: sr.Schema, Raw: plan.Raw}
	req := planmodifier.StringRequest{Path: p, Plan: plan, State: st, Config: cfg, PlanValue: types.StringUnknown(), StateValue: types.StringValue("n-v6net"), ConfigValue: types.StringNull()}
	var resp planmodifier.StringResponse
	resp.PlanValue = req.PlanValue
	ctrNetKeepState{}.PlanModifyString(ctx, req, &resp)
	if resp.PlanValue.ValueString() != "n-v6net" {
		t.Errorf("unchanged inputs should keep state: %v", resp.PlanValue)
	}

	n.IPv6Address = types.StringValue("fd00::11")
	planM.Networks = ctrForceUnknown(t, ctrNetMap(t, map[string]ctrNetworkModel{"v6net": n}), "v6net", "network_id")
	plan = ctrPlan(t, sr, planM)
	req.Plan = plan
	resp = planmodifier.StringResponse{PlanValue: types.StringUnknown()}
	ctrNetKeepState{}.PlanModifyString(ctx, req, &resp)
	if !resp.PlanValue.IsUnknown() {
		t.Errorf("changed inputs should leave unknown: %v", resp.PlanValue)
	}
}

// ctrForceUnknown sets one string attribute of one networks element unknown
// (ctrNetworkObject nulls unknowns).
func ctrForceUnknown(t *testing.T, m types.Map, key, attrName string) types.Map {
	t.Helper()
	els := m.Elements()
	o := els[key].(types.Object)
	at := o.Attributes()
	at[attrName] = types.StringUnknown()
	els[key] = types.ObjectValueMust(ctrNetworkAttrTypes, at)
	return types.MapValueMust(types.ObjectType{AttrTypes: ctrNetworkAttrTypes}, els)
}

// TestCtrUpdateResourcesInPlace: cpu_period / cpu_quota / cpuset_cpus /
// memory_reservation go through ContainerUpdate.
func TestCtrUpdateResourcesInPlace(t *testing.T) {
	ctx := context.Background()
	f, pd := ctrFake(t)
	r := &containerResource{pd: pd}
	sr := ctrSchema(t)
	plan := ctrUploadModel()
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: ctrPlan(t, sr, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var state containerResourceModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &state)...)
	f.calls = nil
	next := state
	next.CPUQuota = types.Int64Value(25000)
	next.contractSetUnknown()
	st := ctrState(t, sr, state)
	uresp := resource.UpdateResponse{State: st}
	r.Update(ctx, resource.UpdateRequest{Plan: ctrPlan(t, sr, next), State: st}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "update" {
		t.Errorf("calls = %s", got)
	}
}

// TestCtrReadGoneDeleteImport: Read drops a removed container, Delete
// tolerates one, ImportState sets id from a name.
func TestCtrReadGoneDeleteImport(t *testing.T) {
	ctx := context.Background()
	f, pd := ctrFake(t)
	r := &containerResource{pd: pd}
	sr := ctrSchema(t)
	m := containerP2Model()
	st := ctrState(t, sr, m)

	f.gone = true
	rresp := resource.ReadResponse{State: st}
	r.Read(ctx, resource.ReadRequest{State: st}, &rresp)
	if rresp.Diagnostics.HasError() || !rresp.State.Raw.IsNull() {
		t.Errorf("read of a removed container: diags=%v null=%v", rresp.Diagnostics, rresp.State.Raw.IsNull())
	}
	dresp := resource.DeleteResponse{State: st}
	r.Delete(ctx, resource.DeleteRequest{State: st}, &dresp)
	if dresp.Diagnostics.HasError() || !strings.Contains(strings.Join(f.calls, ","), "remove") {
		t.Errorf("delete: %v calls=%v", dresp.Diagnostics, f.calls)
	}

	f.gone = false
	f.created = container.CreateRequest{Config: &container.Config{Image: "busybox:1.36"}, HostConfig: &container.HostConfig{}}
	iresp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "up"}, &iresp)
	if iresp.Diagnostics.HasError() {
		t.Fatal(iresp.Diagnostics)
	}
	var id types.String
	iresp.State.GetAttribute(ctx, path.Root("id"), &id)
	if id.ValueString() != "c0ffee0123456789" {
		t.Errorf("imported id = %v", id)
	}

	// Error path: an invalid platform fails before anything is created.
	f.calls = nil
	bad := ctrUploadModel()
	bad.Platform = types.StringValue("a/b/c/d")
	cresp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: ctrPlan(t, sr, bad)}, &cresp)
	if !cresp.Diagnostics.HasError() || len(f.calls) != 0 {
		t.Errorf("invalid platform: diags=%v calls=%v", cresp.Diagnostics, f.calls)
	}
}

func TestCtrGapHelpers(t *testing.T) {
	if p, err := ctrParsePlatform("linux/arm64/v8"); err != nil || p.OS != "linux" || p.Architecture != "arm64" || p.Variant != "v8" {
		t.Errorf("platform: %+v %v", p, err)
	}
	for _, bad := range []string{"/x", "a/b/c/d"} {
		if _, err := ctrParsePlatform(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	gpu := container.DeviceRequest{Driver: "nvidia", Count: -1, Capabilities: [][]string{{"gpu"}}}
	other := container.DeviceRequest{Driver: "cdi", DeviceIDs: []string{"x"}}
	if g, rest := ctrSplitDeviceRequests([]container.DeviceRequest{other, gpu}, true); len(g) != 1 || len(rest) != 1 || rest[0].Driver != "cdi" {
		t.Errorf("split with gpus: %v %v", g, rest)
	}
	if g, rest := ctrSplitDeviceRequests([]container.DeviceRequest{gpu}, false); len(g) != 0 || len(rest) != 1 {
		t.Errorf("split without gpus: %v %v", g, rest)
	}
	tr, fa := true, false
	if !ctrInitRead(types.BoolNull(), nil).IsNull() || !ctrInitRead(types.BoolNull(), &fa).IsNull() || !ctrInitRead(types.BoolNull(), &tr).ValueBool() ||
		ctrInitRead(types.BoolValue(false), &fa).IsNull() {
		t.Error("ctrInitRead")
	}
	if !ctrPriorOnlyString(types.StringNull(), "private").IsNull() || ctrPriorOnlyString(types.StringValue("host"), "host").ValueString() != "host" {
		t.Error("ctrPriorOnlyString")
	}
	if got := ctrLoggingRead(container.LogConfig{Type: "json-file"}, nil, false); got != nil {
		t.Error("logging without prior block is not reported")
	}
	if got := ctrLoggingRead(container.LogConfig{Type: "local"}, nil, true); got == nil || got.Driver.ValueString() != "local" {
		t.Error("logging is reported on import")
	}
	if m, err := ctrParseMode("1777"); err != nil || m != 0o1777 || ctrModeBits(ctrFileMode(m)) != m {
		t.Errorf("mode round trip: %o %v", m, err)
	}
	if v := ctrModeValue(types.StringValue("777"), 0o777); v.ValueString() != "777" {
		t.Errorf("prior spelling kept: %v", v)
	}
	thr := ctrFlattenThrottle(ctrExpandThrottle([]ctrThrottleModel{{Path: types.StringValue("/dev/sda"), Rate: types.Int64Value(5)}}), nil)
	if len(thr) != 1 || thr[0].Rate.ValueInt64() != 5 {
		t.Errorf("throttle round trip: %+v", thr)
	}
}

// TestCtrLoggingReplaceIf: adding or changing logging replaces, removing
// does not.
func TestCtrLoggingReplaceIf(t *testing.T) {
	ctx := context.Background()
	lt := map[string]attr.Type{"driver": types.StringType, "options": types.MapType{ElemType: types.StringType}}
	obj := func(d string) types.Object {
		return types.ObjectValueMust(lt, map[string]attr.Value{"driver": types.StringValue(d), "options": types.MapNull(types.StringType)})
	}
	null := types.ObjectNull(lt)
	for _, c := range []struct {
		name         string
		state, plan  types.Object
		wantReplaced bool
	}{
		{"add", null, obj("local"), true},
		{"change", obj("json-file"), obj("local"), true},
		{"same", obj("local"), obj("local"), false},
		{"remove", obj("local"), null, false},
	} {
		sr := ctrSchema(t)
		req := planmodifier.ObjectRequest{Path: path.Root("logging"), StateValue: c.state, PlanValue: c.plan, ConfigValue: c.plan,
			State: ctrState(t, sr, containerP2Model()), Plan: ctrPlan(t, sr, containerP2Model())}
		resp := &planmodifier.ObjectResponse{PlanValue: c.plan}
		ctrLoggingReplaceIf().PlanModifyObject(ctx, req, resp)
		if resp.RequiresReplace != c.wantReplaced {
			t.Errorf("%s: replace=%v", c.name, resp.RequiresReplace)
		}
	}
}

// TestCtrEndpointFromModel covers every EndpointSettings input.
func TestCtrEndpointFromModel(t *testing.T) {
	n := ctrNullNetwork()
	n.Aliases = stringSetValue([]string{"a"}, false)
	n.IPv4Address, n.IPv6Address = types.StringValue("10.0.0.2"), types.StringValue("fd00::2")
	n.LinkLocalIPs = stringSetValue([]string{"169.254.1.1"}, false)
	n.MacAddress = types.StringValue("02:42:ac:11:00:09")
	n.DriverOpts = stringMapValue(map[string]string{"k": "v"}, false)
	n.GwPriority = types.Int64Value(-1)
	ep, err := ctrEndpointFromModel(context.Background(), n, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%v %s %s %v %s %v %d", ep.Aliases, addrString(ep.IPAMConfig.IPv4Address), addrString(ep.IPAMConfig.IPv6Address),
		addrStrings(ep.IPAMConfig.LinkLocalIPs), ep.MacAddress.String(), ep.DriverOpts, ep.GwPriority)
	if want != "[a] 10.0.0.2 fd00::2 [169.254.1.1] 02:42:ac:11:00:09 map[k:v] -1" {
		t.Errorf("endpoint: %s", want)
	}
	_ = network.EndpointSettings{}
}

// TestAccContainerP5_networksInPlace: adding a network and changing an
// endpoint's aliases is an in-place update (no replacement); the computed
// endpoint values and the inspect outputs are filled.
func TestAccContainerP5_networksInPlace(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-ctrnet")
	cfg := func(nets string) string {
		return fmt.Sprintf(`
resource "docker_network" "a" {
  name = "%[1]s-a"
}

resource "docker_network" "b" {
  name = "%[1]s-b"
}

resource "docker_container" "c" {
  name     = %[1]q
  image    = "busybox:1.36"
  command  = ["sleep", "3600"]
  networks = %[2]s
}
`, name, nets)
	}
	var id string
	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []tfresource.TestStep{
			{
				Config: cfg(`{ (docker_network.a.name) = { aliases = ["one"] } }`),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttrSet("docker_container.c", "networks."+name+"-a.ip_address"),
					tfresource.TestCheckResourceAttrSet("docker_container.c", "inspect_json"),
					tfresource.TestCheckResourceAttrSet("docker_container.c", "content_sha256"),
					tfresource.TestCheckResourceAttrSet("docker_container.c", "docker_server_version"),
					func(s *terraform.State) error { id, _ = p3Attr(s, "docker_container.c", "id"); return nil },
				),
			},
			{
				Config: cfg(`{
    (docker_network.a.name) = { aliases = ["one", "two"] }
    (docker_network.b.name) = {}
  }`),
				ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_container.c", plancheck.ResourceActionUpdate),
				}},
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttrPtr("docker_container.c", "id", &id),
					tfresource.TestCheckResourceAttr("docker_container.c", "networks.%", "2"),
					tfresource.TestCheckResourceAttr("docker_container.c", "networks."+name+"-a.aliases.#", "2"),
					tfresource.TestCheckResourceAttrSet("docker_container.c", "networks."+name+"-b.ip_address"),
				),
			},
			{
				Config: cfg(`{ (docker_network.b.name) = {} }`),
				ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_container.c", plancheck.ResourceActionUpdate),
				}},
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttrPtr("docker_container.c", "id", &id),
					tfresource.TestCheckResourceAttr("docker_container.c", "networks.%", "1"),
				),
			},
		},
	})
}

// TestAccContainerP5_gapAttributes: a selection of the Pass 5 attributes on a
// live daemon (files, logging, init, shm_size, tmpfs, group_add, cap_add,
// mount options), then an empty plan.
func TestAccContainerP5_gapAttributes(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-ctrgap")
	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []tfresource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name        = %q
  image       = "busybox:1.36"
  command     = ["sleep", "3600"]
  init        = true
  shm_size    = 134217728
  tmpfs       = { "/run/x" = "rw,size=1m" }
  group_add   = ["audio"]
  cap_add     = ["NET_ADMIN"]
  dns_options = ["ndots:2"]
  domainname  = "example.internal"

  logging {
    driver  = "json-file"
    options = { max-size = "1m" }
  }

  mounts {
    type   = "tmpfs"
    target = "/scratch"
    tmpfs_options {
      size_bytes = 1048576
      mode       = "1777"
    }
  }

  files {
    path    = "/etc/tfacc.txt"
    content = "hello"
    mode    = "0600"
  }
}
`, name),
			Check: func(s *terraform.State) error {
				insp, err := ctrP3Inspect(t, s, "docker_container.c")
				if err != nil {
					return err
				}
				hc := insp.HostConfig
				if hc.Init == nil || !*hc.Init || hc.ShmSize != 134217728 || hc.Tmpfs["/run/x"] == "" || len(hc.GroupAdd) != 1 ||
					hc.LogConfig.Type != "json-file" || hc.LogConfig.Config["max-size"] != "1m" || insp.Config.Domainname != "example.internal" {
					return fmt.Errorf("host config: %+v", hc)
				}
				return nil
			},
		}},
	})
}
