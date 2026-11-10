package provider

// Pass 5.3 Lane A unit tests: docker_container state (D1), the new
// container fields, the stop_timeout read fix, the start request body
// (D3), owner labels (container, network, volume, docker_container_run)
// and docker_container_run (D2), all against httptest fake Engines.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
)

// ctrLogFrames builds a multiplexed log stream (stream 1 = stdout, 2 = stderr).
func ctrLogFrames(lines ...[2]string) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		payload := "2026-11-09T10:00:00.000000000Z " + l[1] + "\n"
		hdr := make([]byte, 8)
		hdr[0] = map[string]byte{"stdout": 1, "stderr": 2}[l[0]]
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
		b.Write(hdr)
		b.WriteString(payload)
	}
	return b.Bytes()
}

func ctrCreate(t *testing.T, r *containerResource, plan containerResourceModel) (containerResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	sr := ctrSchema(t)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: ctrPlan(t, sr, plan)}, &resp)
	var st containerResourceModel
	if !resp.State.Raw.IsNull() {
		resp.Diagnostics.Append(resp.State.Get(ctx, &st)...)
	}
	return st, resp.Diagnostics
}

func ctrRunModelPlan() containerResourceModel {
	m := ctrUploadModel()
	m.Files = nil
	m.Tty, m.StdinOpen = types.BoolValue(false), types.BoolValue(false)
	m.FinishedAt, m.Logs = types.StringUnknown(), types.StringUnknown()
	m.LogEntries = contractUnknown(contractOutputNode(contractMustItem("engine:container-logs"),
		contractFindOutput("engine:container-logs", "log_entries"))).(types.List)
	return m
}

func TestCtrP53Schema(t *testing.T) {
	sr := ctrSchema(t)
	if sr.Schema.Version != 1 {
		t.Errorf("version = %d", sr.Schema.Version)
	}
	a := sr.Schema.Attributes
	for _, n := range []string{"state", "expected_exit_codes", "logs_tail", "finished_at", "logs", "log_entries", "adopt", "annotations",
		"blkio_weight", "cgroup", "console_size", "cpu_count", "cpu_percent", "cpu_realtime_period", "cpu_realtime_runtime", "cpuset_mems",
		"io_maximum_bandwidth", "io_maximum_iops", "isolation", "links", "masked_paths", "readonly_paths", "memory_swappiness",
		"network_disabled", "oom_kill_disable", "oom_score_adj", "pids_limit", "umask", "uts_mode", "volume_driver", "shell", "volumes"} {
		if _, ok := a[n]; !ok {
			t.Errorf("attribute %s missing", n)
		}
	}
	if _, ok := a["must_run"]; ok {
		t.Error("must_run should be gone (D1)")
	}
	if _, ok := sr.Schema.Blocks["blkio_weight_device"]; !ok {
		t.Error("blkio_weight_device block missing")
	}
	if !strings.Contains(a["umask"].GetMarkdownDescription(), APISincePrefix+"1.56") {
		t.Error("umask needs the 1.56 API annotation")
	}
}

// TestCtrP53ExpandRead: every new field is sent on create and read back
// unchanged.
func TestCtrP53ExpandRead(t *testing.T) {
	ctx := context.Background()
	m := containerP2Model()
	i64 := types.Int64Value
	str := types.StringValue
	m.Annotations = stringMapValue(map[string]string{"a": "1"}, false)
	m.BlkioWeight, m.CPURealtimePeriod, m.CPURealtimeRuntime = i64(300), i64(1000000), i64(950000)
	m.BlkioWeightDevice = []ctrWeightDeviceModel{{Path: str("/dev/sda"), Weight: i64(200)}}
	m.Cgroup, m.CpusetMems, m.Isolation, m.UTSMode, m.VolumeDriver = str("container:x"), str("0"), str("process"), str("host"), str("local")
	m.ConsoleSize = types.ListValueMust(types.Int64Type, []attr.Value{i64(24), i64(80)})
	m.CPUCount, m.CPUPercent, m.IOMaximumBandwidth, m.IOMaximumIOps = i64(2), i64(50), i64(1000), i64(10)
	m.Links = stringListValue([]string{"db:database", "cache"}, false)
	m.MaskedPaths, m.ReadonlyPaths = stringListValue([]string{"/proc/kcore"}, false), stringListValue([]string{"/proc/sys"}, false)
	m.MemorySwappiness, m.OomScoreAdj, m.PidsLimit = i64(10), i64(-100), i64(64)
	m.NetworkDisabled, m.OomKillDisable = types.BoolValue(true), types.BoolValue(true)
	m.Umask = str("0027")
	m.Shell = stringListValue([]string{"/bin/bash", "-c"}, false)
	m.Volumes = stringSetValue([]string{"/cache"}, false)
	var diags diag.Diagnostics
	cfg, hc, netCfg := containerBuildCreate(ctx, m, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if hc.Annotations["a"] != "1" || hc.BlkioWeight != 300 || len(hc.BlkioWeightDevice) != 1 || hc.Cgroup != "container:x" ||
		hc.ConsoleSize != [2]uint{24, 80} || hc.CPUCount != 2 || hc.CPUPercent != 50 || hc.CPURealtimePeriod != 1000000 ||
		hc.CPURealtimeRuntime != 950000 || hc.CpusetMems != "0" || hc.IOMaximumBandwidth != 1000 || hc.IOMaximumIOps != 10 ||
		hc.Isolation != "process" || len(hc.Links) != 2 || hc.MaskedPaths[0] != "/proc/kcore" || hc.ReadonlyPaths[0] != "/proc/sys" ||
		*hc.MemorySwappiness != 10 || !cfg.NetworkDisabled || !*hc.OomKillDisable || hc.OomScoreAdj != -100 || *hc.PidsLimit != 64 ||
		*hc.Umask != 0o027 || hc.UTSMode != "host" || hc.VolumeDriver != "local" || len(cfg.Shell) != 2 {
		t.Errorf("create args: hc=%+v cfg=%+v", hc, cfg)
	}
	if _, ok := cfg.Volumes["/cache"]; !ok {
		t.Errorf("volumes = %v", cfg.Volumes)
	}
	// The daemon reports links as "/name:/container/alias".
	hc.Links = []string{"/db:/p2/database", "/cache:/p2/cache"}
	img := &container.Config{StopSignal: "SIGTERM", Volumes: map[string]struct{}{"/data": {}}}
	cfg.Volumes["/data"] = struct{}{} // image VOLUME: not reported
	got := containerFromInspect(containerP2Inspect(m, cfg, hc, netCfg), img, m)
	if !reflect.DeepEqual(got.ctrP53Model, m.ctrP53Model) {
		t.Errorf("read back:\n got %#v\nwant %#v", got.ctrP53Model, m.ctrP53Model)
	}
}

func TestCtrP53ReadHelpers(t *testing.T) {
	if got := ctrLinksRead(types.ListNull(types.StringType), []string{"/db:/web/db"}); got.Elements()[0].(types.String).ValueString() != "db:db" {
		t.Errorf("import links = %v", got)
	}
	null := types.Int64Null()
	if v := ctrPtrInt64Read(null, nil, 0); !v.IsNull() {
		t.Error("unset pids_limit should stay null")
	}
	zero := int64(0)
	if v := ctrPtrInt64Read(null, &zero, 0); !v.IsNull() {
		t.Error("pids_limit 0 (unlimited) with a null prior should stay null")
	}
	if v := ctrPtrInt64Read(types.Int64Value(64), nil, 0); v.ValueInt64() != 0 {
		t.Errorf("pids_limit cleared by the daemon = %v", v)
	}
	if _, err := ctrParseUmask("0999"); err == nil {
		t.Error("bad umask accepted")
	}
	var res container.Resources
	plan, state := containerP2Model(), containerP2Model()
	plan.PidsLimit, state.PidsLimit = types.Int64Null(), types.Int64Value(64)
	plan.BlkioWeight, plan.CpusetMems = types.Int64Value(500), types.StringValue("0-1")
	ctrUpdateP53(plan, state, &res)
	if res.PidsLimit == nil || *res.PidsLimit != 0 || res.BlkioWeight != 500 || res.CpusetMems != "0-1" {
		t.Errorf("update resources = %+v", res)
	}
}

// TestCtrStateCreated: state = "created" creates and never starts.
func TestCtrStateCreated(t *testing.T) {
	f, pd := ctrFake(t)
	plan := ctrRunModelPlan()
	plan.State = types.StringValue(ctrStateCreated)
	plan.Ports = []containerPortModel{{ContainerPort: types.Int64Value(80), HostPort: types.Int64Value(8080), HostIP: types.StringNull(), Protocol: types.StringValue("tcp")}}
	st, d := ctrCreate(t, &containerResource{pd: pd}, plan)
	if d.HasError() {
		t.Fatal(d)
	}
	if got := strings.Join(f.calls, ","); got != "create" {
		t.Errorf("calls = %s, want create only", got)
	}
	if st.Running.ValueBool() || st.State.ValueString() != ctrStateCreated || !st.Logs.IsNull() || !st.FinishedAt.IsNull() {
		t.Errorf("state: running=%v state=%v logs=%v finished=%v", st.Running, st.State, st.Logs, st.FinishedAt)
	}
	// The #211 class: a container that is not running has no
	// NetworkSettings.Ports; ports come from HostConfig.PortBindings.
	if len(st.Ports) != 1 || st.Ports[0].HostPort.ValueInt64() != 8080 || st.Ports[0].ContainerPort.ValueInt64() != 80 {
		t.Errorf("ports of a stopped container = %+v", st.Ports)
	}
}

// TestCtrStoppedPortsNoDiff: Read of a stopped container with port bindings
// (and no runtime NetworkSettings.Ports) keeps the configured ports, so a
// plan shows no diff (#211 class).
func TestCtrStoppedPortsNoDiff(t *testing.T) {
	prior := containerP2Model()
	prior.Ports = []containerPortModel{
		{ContainerPort: types.Int64Value(80), HostPort: types.Int64Value(8080), HostIP: types.StringValue("127.0.0.1"), Protocol: types.StringValue("tcp")},
		{ContainerPort: types.Int64Value(53), HostPort: types.Int64Null(), HostIP: types.StringNull(), Protocol: types.StringValue("udp")},
	}
	var diags diag.Diagnostics
	cfg, hc, netCfg := containerBuildCreate(context.Background(), prior, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	insp := containerP2Inspect(prior, cfg, hc, netCfg)
	insp.State = &container.State{Status: "exited", ExitCode: 0}
	insp.NetworkSettings.Ports = nil // a stopped container has no published ports
	got := containerFromInspect(insp, &container.Config{StopSignal: "SIGTERM"}, prior)
	if !reflect.DeepEqual(got.Ports, prior.Ports) {
		t.Errorf("ports drifted: got %+v want %+v", got.Ports, prior.Ports)
	}
	if !reflect.DeepEqual(got.Mounts, prior.Mounts) || !got.Networks.Equal(prior.Networks) {
		t.Error("config-only fields of a stopped container drifted")
	}
}

// TestCtrStateExited: state = "exited" starts, waits, captures output and
// fails on an unexpected exit code (keeping the container in state).
func TestCtrStateExited(t *testing.T) {
	f, pd := ctrFake(t)
	f.oneShot = true
	f.logs = ctrLogFrames([2]string{"stdout", "migrating"}, [2]string{"stderr", "warn: x"})
	plan := ctrRunModelPlan()
	plan.State = types.StringValue(ctrStateExited)
	plan.WaitTimeout = types.StringValue("1m")
	st, d := ctrCreate(t, &containerResource{pd: pd}, plan)
	if d.HasError() {
		t.Fatal(d)
	}
	if got := strings.Join(f.calls, ","); got != "create,start,wait,logs" {
		t.Errorf("calls = %s", got)
	}
	if st.ExitCode.ValueInt64() != 0 || st.Running.ValueBool() || st.FinishedAt.ValueString() != "2026-11-09T10:00:00Z" ||
		!strings.Contains(st.Logs.ValueString(), "stdout up migrating") || len(st.LogEntries.Elements()) != 2 {
		t.Errorf("exited outputs: code=%v running=%v finished=%v logs=%q entries=%d", st.ExitCode, st.Running, st.FinishedAt, st.Logs.ValueString(),
			len(st.LogEntries.Elements()))
	}

	// A later Read keeps the run's output and never starts it again.
	f.calls = nil
	sr := ctrSchema(t)
	stt := ctrState(t, sr, st)
	rresp := resource.ReadResponse{State: stt}
	(&containerResource{pd: pd}).Read(context.Background(), resource.ReadRequest{State: stt}, &rresp)
	var after containerResourceModel
	rresp.Diagnostics.Append(rresp.State.Get(context.Background(), &after)...)
	if rresp.Diagnostics.HasError() || !after.Logs.Equal(st.Logs) || strings.Contains(strings.Join(f.calls, ","), "start") {
		t.Errorf("read: %v logs=%v calls=%v", rresp.Diagnostics, after.Logs, f.calls)
	}

	// Exit code 3 is not expected: the apply fails, the container stays in state.
	f2, pd2 := ctrFake(t)
	f2.oneShot, f2.exitCode = true, 3
	st2, d2 := ctrCreate(t, &containerResource{pd: pd2}, plan)
	if !d2.HasError() || !strings.Contains(d2.Errors()[0].Detail(), "exited with code 3") || st2.ID.ValueString() == "" {
		t.Errorf("unexpected code: diags=%v id=%v", d2, st2.ID)
	}
	plan.ExpectedExitCodes = types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(0), types.Int64Value(3)})
	f3, pd3 := ctrFake(t)
	f3.oneShot, f3.exitCode = true, 3
	if _, d3 := ctrCreate(t, &containerResource{pd: pd3}, plan); d3.HasError() {
		t.Errorf("3 is expected: %v", d3)
	}
}

// TestCtrStartSendsNoBody (D3): POST /containers/{id}/start carries no
// request body (the HostConfig-on-start form was removed from the API).
func TestCtrStartSendsNoBody(t *testing.T) {
	f, pd := ctrFake(t)
	if _, d := ctrCreate(t, &containerResource{pd: pd}, ctrRunModelPlan()); d.HasError() {
		t.Fatal(d)
	}
	if len(f.startBodies) != 1 || f.startBodies[0] != 0 {
		t.Errorf("start request bodies = %v, want one empty body", f.startBodies)
	}
}

// TestCtrStopTimeoutDaemonDefault: a daemon that fills Config.StopTimeout
// (Docker Desktop 29.5: 1) for a container created without it. stop_timeout
// is Optional + Computed, so an unset value is planned as unknown and the
// daemon's value lands in state (no inconsistent result after apply), and an
// import reads the real value (Pass 5.3 Lane Z: TestAccContainerP3_laneCAttributes).
func TestCtrStopTimeoutDaemonDefault(t *testing.T) {
	f, pd := ctrFake(t)
	one := 1
	f.mutate = func(r *container.InspectResponse) { r.Config.StopTimeout = &one }
	plan := ctrRunModelPlan()
	plan.StopTimeout = types.Int64Unknown()
	st, d := ctrCreate(t, &containerResource{pd: pd}, plan)
	if d.HasError() {
		t.Fatal(d)
	}
	if f.created.Config.StopTimeout != nil {
		t.Errorf("create sent StopTimeout %v", *f.created.Config.StopTimeout)
	}
	if st.StopTimeout.ValueInt64() != 1 {
		t.Errorf("stop_timeout = %v, want the daemon's 1", st.StopTimeout)
	}
	// Set by the user, it is sent and reported.
	plan.StopTimeout = types.Int64Value(5)
	f.mutate = nil
	st, _ = ctrCreate(t, &containerResource{pd: pd}, plan)
	if f.created.Config.StopTimeout == nil || *f.created.Config.StopTimeout != 5 || st.StopTimeout.ValueInt64() != 5 {
		t.Errorf("configured stop_timeout = %v", st.StopTimeout)
	}
}

// TestCtrModifyPlanState: running plans a start for a stopped container;
// created leaves a running one alone with a warning.
func TestCtrModifyPlanState(t *testing.T) {
	ctx := context.Background()
	sr := ctrSchema(t)
	r := &containerResource{}
	run := func(planM, stateM containerResourceModel) (resource.ModifyPlanResponse, containerResourceModel) {
		plan := ctrPlan(t, sr, planM)
		resp := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: plan, State: ctrState(t, sr, stateM), Config: tfsdk.Config{Schema: sr.Schema, Raw: plan.Raw}}, &resp)
		var out containerResourceModel
		resp.Diagnostics.Append(resp.Plan.Get(ctx, &out)...)
		return resp, out
	}
	state := containerP2Model()
	state.Running = types.BoolValue(false)
	resp, out := run(state, state)
	if resp.Diagnostics.HasError() || !out.Running.ValueBool() || !out.InspectJSON.IsUnknown() {
		t.Errorf("running + stopped: running=%v inspect_json unknown=%v %v", out.Running, out.InspectJSON.IsUnknown(), resp.Diagnostics)
	}
	state.Running = types.BoolValue(true)
	plan := state
	plan.State = types.StringValue(ctrStateCreated)
	resp, out = run(plan, state)
	if resp.Diagnostics.WarningsCount() != 1 || !out.Running.ValueBool() {
		t.Errorf("created + running: warnings=%d running=%v", resp.Diagnostics.WarningsCount(), out.Running)
	}
	state.State = types.StringValue(ctrStateExited)
	state.Running = types.BoolValue(false)
	resp, out = run(state, state)
	if resp.Diagnostics.HasError() || out.Running.ValueBool() {
		t.Errorf("exited: running=%v %v", out.Running, resp.Diagnostics)
	}
	// wait_timeout defaults by state when the config leaves it unset.
	plan = containerP2Model()
	plan.State = types.StringValue(ctrStateExited)
	plan.WaitTimeout = types.StringNull()
	p := ctrPlan(t, sr, plan)
	mresp := resource.ModifyPlanResponse{Plan: p}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: p, State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)},
		Config: tfsdk.Config{Schema: sr.Schema, Raw: p.Raw}}, &mresp)
	var wt types.String
	mresp.Plan.GetAttribute(ctx, path.Root("wait_timeout"), &wt)
	if wt.ValueString() != ctrExitedDefaultWaitTimeout {
		t.Errorf("exited wait_timeout default = %v", wt)
	}
}

func TestCtrValidateState(t *testing.T) {
	m := containerResourceModel{AutoRemove: types.BoolValue(true), Wait: types.BoolValue(false), Restart: types.StringValue("no")}
	m.State = types.StringValue(ctrStateExited)
	var d diag.Diagnostics
	ctrValidateState(m, &d)
	if !d.HasError() || !strings.Contains(d.Errors()[0].Detail(), "docker_container_run") {
		t.Errorf("auto_remove + exited: %v", d)
	}
	m = containerResourceModel{AutoRemove: types.BoolValue(false), Wait: types.BoolValue(true), Restart: types.StringValue("no")}
	m.State = types.StringValue(ctrStateCreated)
	d = nil
	ctrValidateState(m, &d)
	if !d.HasError() {
		t.Error("wait + created should fail")
	}
	m = containerResourceModel{AutoRemove: types.BoolValue(false), Wait: types.BoolValue(false), Restart: types.StringValue("always")}
	m.State = types.StringValue(ctrStateExited)
	d = nil
	ctrValidateState(m, &d)
	if !d.HasError() {
		t.Error("restart always + exited should fail")
	}
}

func TestCtrMountOptionsP53(t *testing.T) {
	str := types.StringValue
	tr := types.BoolValue(true)
	in := []containerMountModel{
		{Type: str("bind"), Source: str("/srv"), Target: str("/a"), ReadOnly: tr, SelinuxRelabel: types.StringNull(), Consistency: str("cached"),
			BindOptions: &ctrBindOptionsModel{Propagation: types.StringNull(), NonRecursive: tr, CreateMountpoint: tr, ReadOnlyNonRecursive: types.BoolNull(), ReadOnlyForceRecursive: tr}},
		{Type: str("tmpfs"), Source: types.StringNull(), Target: str("/t"), ReadOnly: types.BoolValue(false), SelinuxRelabel: types.StringNull(), Consistency: types.StringNull(),
			TmpfsOptions: &ctrTmpfsOptionsModel{SizeBytes: types.Int64Null(), Mode: types.StringNull(),
				Options: types.ListValueMust(ctrTmpfsOptionsType, []attr.Value{stringListValue([]string{"noexec"}, false), stringListValue([]string{"nr_inodes", "100"}, false)})}},
		{Type: str("image"), Source: str("busybox:1.36"), Target: str("/img"), ReadOnly: types.BoolValue(false), SelinuxRelabel: types.StringNull(), Consistency: types.StringNull(),
			ImageOptions: &ctrImageOptionsModel{Subpath: str("bin")}},
	}
	out, binds, err := containerExpandMountsBinds(in)
	if err != nil || len(binds) != 0 || len(out) != 3 {
		t.Fatalf("expand: %v %v %v", out, binds, err)
	}
	b := out[0].BindOptions
	if out[0].Consistency != "cached" || b == nil || !b.NonRecursive || !b.CreateMountpoint || !b.ReadOnlyForceRecursive || b.ReadOnlyNonRecursive {
		t.Errorf("bind = %+v", out[0])
	}
	if o := out[1].TmpfsOptions; o == nil || len(o.Options) != 2 || o.Options[1][1] != "100" {
		t.Errorf("tmpfs = %+v", out[1].TmpfsOptions)
	}
	if o := out[2].ImageOptions; o == nil || o.Subpath != "bin" {
		t.Errorf("image = %+v", out[2])
	}
	back := containerFlattenMounts(out, nil, in)
	if !reflect.DeepEqual(back, in) {
		t.Errorf("round trip:\n got %+v\nwant %+v", back, in)
	}
	bad := []containerMountModel{{Type: str("image"), Source: types.StringNull(), Target: str("/x")}}
	if _, _, err := containerExpandMountsBinds(bad); err == nil {
		t.Error("image mount without source accepted")
	}
}

// ---------------------------------------------------------------------------
// Owner labels

func TestCtrOwnerLabel(t *testing.T) {
	// Unset: nothing added.
	f, pd := ctrFake(t)
	st, d := ctrCreate(t, &containerResource{pd: pd}, ctrRunModelPlan())
	if d.HasError() {
		t.Fatal(d)
	}
	if _, ok := f.created.Config.Labels[ownerLabelKey]; ok {
		t.Error("owner label sent without owner")
	}
	// Set: sent, and not reported in labels.
	f, pd = ctrFake(t)
	pd.Owner = "team-a"
	st, d = ctrCreate(t, &containerResource{pd: pd}, ctrRunModelPlan())
	if d.HasError() {
		t.Fatal(d)
	}
	if f.created.Config.Labels[ownerLabelKey] != "team-a" {
		t.Errorf("labels sent = %v", f.created.Config.Labels)
	}
	if _, ok := st.Labels.Elements()[ownerLabelKey]; ok {
		t.Error("owner label leaked into labels")
	}
	// Conflict: a container of that name owned by someone else.
	f, pd = ctrFake(t)
	pd.Owner = "team-a"
	f.created = container.CreateRequest{Config: &container.Config{Labels: map[string]string{ownerLabelKey: "team-b"}}, HostConfig: &container.HostConfig{}}
	_, d = ctrCreate(t, &containerResource{pd: pd}, ctrRunModelPlan())
	if !d.HasError() || !strings.Contains(d.Errors()[0].Detail(), "team-b") || strings.Contains(strings.Join(f.calls, ","), "create") {
		t.Errorf("conflict: %v calls=%v", d, f.calls)
	}
}

func TestNetworkOwnerAndFields(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		owner, existing string
		wantErr         bool
	}{{"", "", false}, {"team-a", "", false}, {"team-a", "team-b", true}} {
		f, pd := objFake(t)
		pd.Owner = c.owner
		var body map[string]any
		if c.existing != "" {
			f.json("GET /networks/app", network.Inspect{Network: network.Network{ID: "old", Name: "app", Labels: map[string]string{ownerLabelKey: c.existing}}})
		}
		f.on("POST /networks/create", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			p3eWriteJSON(w, 201, map[string]string{"Id": "nid"})
		})
		f.json("GET /networks/nid", network.Inspect{Network: network.Network{ID: "nid", Name: "app", Driver: "macvlan", Scope: "swarm",
			ConfigFrom: network.ConfigReference{Network: "cfg"}, EnableIPv4: false, Labels: map[string]string{ownerLabelKey: c.owner, labelManagedBy: labelManagedByValue}}})
		r := &networkResource{pd: pd}
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		typ := sr.Schema.Type().TerraformType(ctx)
		plan := tfsdk.Plan{Schema: sr.Schema, Raw: p3eObject(typ, map[string]tftypes.Value{
			"name": p3eStr("app"), "driver": p3eStr("macvlan"), "scope": p3eStr("swarm"), "config_from": p3eStr("cfg"),
			"enable_ipv4": tftypes.NewValue(tftypes.Bool, false), "config_only": tftypes.NewValue(tftypes.Bool, false)})}
		resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
		r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
		if c.wantErr {
			if !resp.Diagnostics.HasError() || body != nil {
				t.Errorf("conflict: %v body=%v", resp.Diagnostics, body)
			}
			continue
		}
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		labels, _ := body["Labels"].(map[string]any)
		if got, has := labels[ownerLabelKey]; has != (c.owner != "") || (has && got != c.owner) {
			t.Errorf("owner %q: labels sent %v", c.owner, labels)
		}
		if body["Scope"] != "swarm" || body["EnableIPv4"] != false || body["ConfigFrom"] == nil {
			t.Errorf("create body = %v", body)
		}
		var st networkResourceModel
		resp.State.Get(ctx, &st)
		if st.ConfigFrom.ValueString() != "cfg" || st.EnableIPv4.ValueBool() || st.Scope.ValueString() != "swarm" {
			t.Errorf("state = %+v", st)
		}
		if _, ok := st.Labels.Elements()[ownerLabelKey]; ok {
			t.Error("owner label leaked into labels")
		}
	}
}

func TestVolumeOwnerLabel(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		owner, existing string
		wantErr         string
	}{{"", "", ""}, {"team-a", "", ""}, {"team-a", "team-b", "team-b"}} {
		f, pd := objFake(t)
		pd.Owner = c.owner
		var body map[string]any
		created := false
		f.on("GET /volumes/data", func(w http.ResponseWriter, _ *http.Request) {
			if c.existing == "" && !created {
				p3eWriteJSON(w, 404, map[string]string{"message": "no such volume"})
				return
			}
			p3eWriteJSON(w, 200, volume.Volume{Name: "data", Driver: "local", Labels: map[string]string{ownerLabelKey: c.existing + c.owner}})
		})
		f.on("POST /volumes/create", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			created = true
			p3eWriteJSON(w, 201, volume.Volume{Name: "data", Driver: "local"})
		})
		r := &volumeResource{pd: pd}
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		typ := sr.Schema.Type().TerraformType(ctx)
		plan := tfsdk.Plan{Schema: sr.Schema, Raw: p3eObject(typ, map[string]tftypes.Value{"name": p3eStr("data"), "driver": p3eStr("local")})}
		resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
		r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
		if c.wantErr != "" {
			if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), c.wantErr) || body != nil {
				t.Errorf("conflict: %v", resp.Diagnostics)
			}
			continue
		}
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		labels, _ := body["Labels"].(map[string]any)
		if got, has := labels[ownerLabelKey]; has != (c.owner != "") || (has && got != c.owner) {
			t.Errorf("owner %q: labels sent %v", c.owner, labels)
		}
	}
}

// ---------------------------------------------------------------------------
// docker_container_run (D2)

func TestContainerRunAction(t *testing.T) {
	for _, owner := range []string{"", "team-a"} {
		f, pd := ctrFake(t)
		pd.Owner = owner
		f.oneShot = true
		f.logs = ctrLogFrames([2]string{"stdout", "hello"}, [2]string{"stderr", "oops"})
		rp := filepath.Join(t.TempDir(), "run.json")
		res := actInvokePD(t, pd, NewContainerRunAction(), map[string]tftypes.Value{
			"image": p3eStr("busybox:1.36"), "command": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("true")}),
			"result_path": p3eStr(rp)})
		if res.Diags.HasError() {
			t.Fatal(res.errText())
		}
		if got := strings.Join(f.calls, ","); got != "create,start,wait,logs,remove" {
			t.Errorf("calls = %s", got)
		}
		if got, has := f.created.Config.Labels[ownerLabelKey]; has != (owner != "") || got != owner {
			t.Errorf("owner %q: labels = %v", owner, f.created.Config.Labels)
		}
		b, err := os.ReadFile(rp)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		r, _ := out["result"].(map[string]any)
		if r["exit_code"] != float64(0) || r["stdout"] != "hello\n" || r["stderr"] != "oops\n" || out["container_id"] != "c0ffee0123456789" {
			t.Errorf("result = %s", b)
		}
		if len(f.startBodies) != 1 || f.startBodies[0] != 0 {
			t.Errorf("start bodies = %v", f.startBodies)
		}
	}
	// Unexpected exit code: fails, the container is still removed;
	// remove = false keeps it.
	f, pd := ctrFake(t)
	f.oneShot, f.exitCode = true, 2
	res := actInvokePD(t, pd, NewContainerRunAction(), map[string]tftypes.Value{"image": p3eStr("busybox:1.36"),
		"remove": tftypes.NewValue(tftypes.Bool, false)})
	if !res.Diags.HasError() || !strings.Contains(res.errText(), "exited with code 2") || strings.Contains(strings.Join(f.calls, ","), "remove") {
		t.Errorf("exit 2, remove=false: %s calls=%v", res.errText(), f.calls)
	}
	if d := actValidate(t, NewContainerRunAction(), map[string]tftypes.Value{"image": p3eStr("x"), "timeout": p3eStr("soon")}); !d.HasError() {
		t.Error("bad timeout accepted")
	}
}

func TestExecConsoleSize(t *testing.T) {
	var d diag.Diagnostics
	cs := actExecConsoleSize(context.Background(), types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(40), types.Int64Value(120)}), &d)
	if d.HasError() || cs.Height != 40 || cs.Width != 120 {
		t.Errorf("console size = %+v %v", cs, d)
	}
	if d := actValidate(t, NewExecAction(), map[string]tftypes.Value{
		"container_id": p3eStr("c"), "command": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("ls")}),
		"console_size": tftypes.NewValue(tftypes.List{ElementType: tftypes.Number}, []tftypes.Value{tftypes.NewValue(tftypes.Number, 40), tftypes.NewValue(tftypes.Number, 120)}),
	}); !d.HasError() {
		t.Error("console_size without tty accepted")
	}
}
