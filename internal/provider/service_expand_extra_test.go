package provider

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
)

func TestServiceP2ExpandSpotChecks(t *testing.T) {
	m := svcTestModel()
	spec, diags := svcExpand(context.Background(), &m, svcTestLookup(), svcExpandOpts{FullName: "web"})
	if diags.HasError() {
		t.Fatal(diags)
	}
	cs := spec.TaskTemplate.ContainerSpec
	if cs.Hostname != "{{.Task.Name}}" || cs.User != "101:101" || cs.Dir != "/srv" {
		t.Errorf("hostname/user/dir: %q %q %q", cs.Hostname, cs.User, cs.Dir)
	}
	if !reflect.DeepEqual(cs.Hosts, []string{"10.0.0.5 db.local", "fd00::1 v6.local"}) {
		t.Errorf("hosts = %q", cs.Hosts)
	}
	if cs.DNSConfig == nil || addrString(cs.DNSConfig.Nameservers[0]) != "10.0.0.2" || cs.DNSConfig.Search != nil || cs.DNSConfig.Options[0] != "ndots:2" {
		t.Errorf("dns = %+v", cs.DNSConfig)
	}
	if !reflect.DeepEqual(cs.CapabilityAdd, []string{"NET_ADMIN"}) || cs.CapabilityDrop != nil || cs.Sysctls["net.core.somaxconn"] != "1024" {
		t.Errorf("caps/sysctls: %v %v %v", cs.CapabilityAdd, cs.CapabilityDrop, cs.Sysctls)
	}
	if len(cs.Ulimits) != 1 || cs.Ulimits[0].Name != "nofile" || cs.Ulimits[0].Hard != 2048 {
		t.Errorf("ulimits = %+v", cs.Ulimits)
	}
	if cs.StopGracePeriod == nil || *cs.StopGracePeriod != 30*time.Second || spec.TaskTemplate.ForceUpdate != 2 {
		t.Errorf("grace/force: %v %d", cs.StopGracePeriod, spec.TaskTemplate.ForceUpdate)
	}
}

func TestServiceP2FlattenDaemonNormalisation(t *testing.T) {
	prior := svcTestModel()
	spec, _ := svcExpand(context.Background(), &prior, svcTestLookup(), svcExpandOpts{FullName: "web"})
	d := svcSimulateDaemon(spec)
	// Capability spelling normalised by the daemon keeps the prior value.
	d.TaskTemplate.ContainerSpec.CapabilityAdd = []string{"CAP_NET_ADMIN"}
	// A forced update from outside Terraform is ignored when force_update is unset.
	d.TaskTemplate.ForceUpdate = 7
	p := prior
	p.ForceUpdate = types.Int64Null()
	got := svcFlatten(d, p, svcTestLookup(), svcFlattenOpts{})
	if !got.CapAdd.Equal(prior.CapAdd) || !got.ForceUpdate.IsNull() {
		t.Errorf("cap_add=%v force_update=%v", got.CapAdd, got.ForceUpdate)
	}
	got = svcFlatten(d, prior, svcTestLookup(), svcFlattenOpts{})
	if got.ForceUpdate.ValueInt64() != 7 {
		t.Errorf("force_update drift not reported: %v", got.ForceUpdate)
	}

	// Import: daemon values show, empty DNS config gives no block.
	d.TaskTemplate.ContainerSpec.DNSConfig = &swarm.DNSConfig{}
	got = svcFlatten(d, svcSpecModel{}, svcTestLookup(), svcFlattenOpts{})
	if got.Hostname.ValueString() != "{{.Task.Name}}" || got.DNSConfig != nil || len(got.Ulimits) != 1 || got.StopGracePeriod.ValueString() != "30s" {
		t.Errorf("import: hostname=%v dns=%+v ulimits=%+v grace=%v", got.Hostname, got.DNSConfig, got.Ulimits, got.StopGracePeriod)
	}
	if h := got.Hosts.Elements(); len(h) != 2 || h[0].(types.String).ValueString() != "db.local:10.0.0.5" {
		t.Errorf("import hosts = %v", got.Hosts)
	}
}

func TestServiceHostsFromSwarm(t *testing.T) {
	// One daemon entry with aliases becomes one host:ip per name.
	got := svcHostsFromSwarm(types.ListNull(types.StringType), []string{"10.0.0.1 a b", "weird"})
	want := []string{"a:10.0.0.1", "b:10.0.0.1", "weird"}
	var gotS []string
	for _, e := range got.Elements() {
		gotS = append(gotS, e.(types.String).ValueString())
	}
	if !reflect.DeepEqual(gotS, want) {
		t.Errorf("got %v", gotS)
	}
	// Prior written in daemon format is kept.
	prior := stringListValue([]string{"10.0.0.1  a"}, false)
	if got := svcHostsFromSwarm(prior, []string{"10.0.0.1 a"}); !got.Equal(prior) {
		t.Errorf("daemon-format prior: %v", got)
	}
	if _, err := svcHostToSwarm("nocolon"); err == nil {
		t.Error("entry without ':' should fail")
	}
}

func TestServiceJobModesRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := svcTestModel()
	base.UpdateConfig, base.RollbackConfig = nil, nil

	// replicated_job with explicit values.
	m := base
	m.Mode = types.StringValue("replicated_job")
	m.MaxConcurrent, m.TotalCompletions = types.Int64Value(2), types.Int64Value(6)
	spec, diags := svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{FullName: "web"})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if j := spec.Mode.ReplicatedJob; j == nil || *j.MaxConcurrent != 2 || *j.TotalCompletions != 6 || spec.Mode.Replicated != nil {
		t.Fatalf("mode = %+v", spec.Mode)
	}
	if got := svcFlatten(svcSimulateDaemon(spec), m, svcTestLookup(), svcFlattenOpts{}); !reflect.DeepEqual(got, m) {
		svcDiffFields(t, got, m)
	}

	// replicated_job with daemon defaults filled in: stays null.
	m.MaxConcurrent, m.TotalCompletions = types.Int64Null(), types.Int64Null()
	spec, _ = svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{FullName: "web"})
	one := uint64(1)
	d := svcSimulateDaemon(spec)
	d.Mode.ReplicatedJob = &swarm.ReplicatedJob{MaxConcurrent: &one, TotalCompletions: &one}
	if got := svcFlatten(d, m, svcTestLookup(), svcFlattenOpts{}); !reflect.DeepEqual(got, m) {
		svcDiffFields(t, got, m)
	}

	// global_job.
	m = base
	m.Mode = types.StringValue("global_job")
	spec, diags = svcExpand(ctx, &m, svcTestLookup(), svcExpandOpts{FullName: "web"})
	if diags.HasError() || spec.Mode.GlobalJob == nil {
		t.Fatalf("global_job: %v %+v", diags, spec.Mode)
	}
	if got := svcFlatten(svcSimulateDaemon(spec), m, svcTestLookup(), svcFlattenOpts{}); !reflect.DeepEqual(got, m) {
		svcDiffFields(t, got, m)
	}
	if svcModeOf(spec) != "global_job" {
		t.Errorf("svcModeOf = %s", svcModeOf(spec))
	}

	// Validation.
	bad := svcTestModel() // replicated with update_config
	bad.MaxConcurrent = types.Int64Value(2)
	if _, diags := svcExpand(ctx, &bad, svcTestLookup(), svcExpandOpts{}); !diags.HasError() {
		t.Error("max_concurrent with replicated should fail")
	}
	bad = svcTestModel()
	bad.Mode = types.StringValue("global_job")
	if _, diags := svcExpand(ctx, &bad, svcTestLookup(), svcExpandOpts{}); !diags.HasError() {
		t.Error("update_config with a job mode should fail")
	}
}

func TestServiceNetworksMapRoundTrip(t *testing.T) {
	ctx := context.Background()
	nullNets := types.MapNull(types.ObjectType{AttrTypes: svcNetworkAttachAttrTypes})
	m := svcTestModel()
	m.Networks = svcNetworksValue(nullNets, map[string]svcNetworkAttachModel{
		"frontend": svcNetworkAttachment([]string{"www", "web-alias"}, nil),
		"net333":   svcNetworkAttachment(nil, nil),
	})
	lk := svcTestLookup()
	spec, diags := svcExpand(ctx, &m, lk, svcExpandOpts{FullName: "web"})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if n := spec.TaskTemplate.Networks; len(n) != 2 || n[0].Target != "net111" || len(n[0].Aliases) != 2 || n[1].Target != "net333" || n[1].Aliases != nil {
		t.Fatalf("networks = %+v", n)
	}
	if got := svcFlatten(svcSimulateDaemon(spec), m, lk, svcFlattenOpts{}); !reflect.DeepEqual(got, m) {
		svcDiffFields(t, got, m)
	}

	// Import: keys are the IDs (no prior spelling), aliases kept.
	got := svcFlatten(svcSimulateDaemon(spec), svcSpecModel{}, lk, svcFlattenOpts{})
	el := got.Networks.Elements()
	if len(el) != 2 || el["net111"] == nil || el["net333"] == nil {
		t.Errorf("import = %v", got.Networks)
	}

	// Stack: the short-name alias is added on expand and stripped on flatten
	// unless the user listed it.
	lk.stackPrefix = "app_"
	m.Name = types.StringValue("web")
	m.Networks = svcNetworksValue(nullNets, map[string]svcNetworkAttachModel{
		"frontend": svcNetworkAttachment([]string{"www", "web-alias"}, nil),
		"net333":   svcNetworkAttachment([]string{"web"}, nil),
	})
	spec, diags = svcExpand(ctx, &m, lk, svcExpandOpts{FullName: "app_web", Namespace: "app"})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if a := spec.TaskTemplate.Networks[0].Aliases; a[0] != "web" || len(a) != 3 || spec.TaskTemplate.Networks[0].Target != "net222" {
		t.Errorf("stack attachment = %+v", spec.TaskTemplate.Networks[0])
	}
	if a := spec.TaskTemplate.Networks[1].Aliases; len(a) != 1 {
		t.Errorf("stack aliases dedupe = %v", a)
	}
	if got := svcFlatten(svcSimulateDaemon(spec), m, lk, svcFlattenOpts{ShortName: "web", StripStack: true}); !got.Networks.Equal(m.Networks) {
		t.Errorf("stack flatten = %v", got.Networks)
	}

	// An explicitly empty map survives the round trip.
	m.Networks = svcNetworksValue(types.MapValueMust(types.ObjectType{AttrTypes: svcNetworkAttachAttrTypes}, map[string]attr.Value{}), nil)
	spec, _ = svcExpand(ctx, &m, lk, svcExpandOpts{FullName: "web"})
	if got := svcFlatten(svcSimulateDaemon(spec), m, lk, svcFlattenOpts{}); got.Networks.IsNull() || len(got.Networks.Elements()) != 0 {
		t.Errorf("empty map = %v", got.Networks)
	}
}

func TestServiceMountOptionTypeMismatch(t *testing.T) {
	m := svcTestModel()
	m.Mounts[0].TmpfsOptions = &svcTmpfsOptionsModel{SizeBytes: types.Int64Value(1), Mode: types.Int64Null()}
	m.Resources.Reservations.GenericResources = append(m.Resources.Reservations.GenericResources, svcGenericResourceModel{})
	m.Privileges.CredentialSpec = &svcCredentialSpecModel{File: types.StringValue("a.json"), Registry: types.StringValue("r"), Config: types.StringNull()}
	_, diags := svcExpand(context.Background(), &m, svcTestLookup(), svcExpandOpts{})
	if diags.ErrorsCount() != 3 {
		t.Errorf("want 3 errors (tmpfs_options on a volume, empty generic resource, two credential sources), got %v", diags)
	}
}

func TestServiceFlattenP5ImportDefaults(t *testing.T) {
	f := false
	spec := swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{Runtime: swarm.RuntimeContainer, ContainerSpec: &swarm.ContainerSpec{
		Image: "nginx", Init: &f, Isolation: "default", Privileges: &swarm.Privileges{},
		Mounts: []mount.Mount{{Type: mount.TypeVolume, Target: "/v", VolumeOptions: &mount.VolumeOptions{}}},
	}}}
	got := svcFlatten(spec, svcSpecModel{}, svcTestLookup(), svcFlattenOpts{})
	if !got.Init.IsNull() || !got.Isolation.IsNull() || !got.Runtime.IsNull() || got.Privileges != nil || !got.ReadOnly.IsNull() {
		t.Errorf("defaults should be null: init=%v iso=%v runtime=%v priv=%+v ro=%v", got.Init, got.Isolation, got.Runtime, got.Privileges, got.ReadOnly)
	}
	if got.Mounts[0].VolumeOptions != nil {
		t.Errorf("empty volume options should not show: %+v", got.Mounts[0].VolumeOptions)
	}
}

func svcJobTask(id string, iter uint64, state swarm.TaskState) swarm.Task {
	return swarm.Task{ID: id, DesiredState: swarm.TaskStateComplete, Status: swarm.TaskStatus{State: state}, JobIteration: &swarm.Version{Index: iter}}
}

func TestServiceJobConvergeCheck(t *testing.T) {
	two, three := uint64(2), uint64(3)
	rj := swarm.Service{Spec: swarm.ServiceSpec{Mode: swarm.ServiceMode{ReplicatedJob: &swarm.ReplicatedJob{MaxConcurrent: &two, TotalCompletions: &three}}},
		JobStatus: &swarm.JobStatus{JobIteration: swarm.Version{Index: 5}}}
	tasks := []swarm.Task{
		svcJobTask("a", 5, swarm.TaskStateComplete), svcJobTask("b", 5, swarm.TaskStateComplete),
		svcJobTask("old", 4, swarm.TaskStateComplete), svcJobTask("c", 5, swarm.TaskStateRunning),
	}
	if st := svcJobConvergeCheck(tasks, rj); st.Done || st.Running != 2 || st.Want != 3 || !st.Job {
		t.Errorf("in progress: %+v", st)
	}
	tasks[3].Status.State = swarm.TaskStateComplete
	if st := svcJobConvergeCheck(tasks, rj); !st.Done {
		t.Errorf("complete: %+v", st)
	}
	// Defaults: TotalCompletions nil -> MaxConcurrent.
	rj.Spec.Mode.ReplicatedJob.TotalCompletions = nil
	if st := svcJobConvergeCheck(tasks[:2], rj); !st.Done || st.Want != 2 {
		t.Errorf("default total: %+v", st)
	}

	gj := swarm.Service{Spec: swarm.ServiceSpec{Mode: swarm.ServiceMode{GlobalJob: &swarm.GlobalJob{}}}}
	if st := svcJobConvergeCheck(nil, gj); st.Done {
		t.Error("global job with no tasks is not done")
	}
	failed := svcJobTask("f", 1, swarm.TaskStateFailed)
	failed.Status.Err = "exit 1"
	gt := []swarm.Task{svcJobTask("a", 1, swarm.TaskStateComplete), svcJobTask("b", 1, swarm.TaskStateRunning)}
	if st := svcJobConvergeCheck(gt, gj); st.Done {
		t.Errorf("global job pending: %+v", st)
	}
	gt[1] = failed
	st := svcJobConvergeCheck(gt, gj)
	if !st.Done || len(st.TaskErrors) != 1 {
		t.Errorf("global job finished: %+v", st)
	}
}
