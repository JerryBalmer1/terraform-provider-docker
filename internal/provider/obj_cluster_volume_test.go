package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

func objCVTestSpec(avail string) types.Object {
	return types.ObjectValueMust(objCVSpecAttrTypes, map[string]attr.Value{
		"group": types.StringValue("db"),
		"access_mode": types.ObjectValueMust(objCVAccessAttrTypes, map[string]attr.Value{
			"scope": types.StringValue("single"), "sharing": types.StringValue("none"),
			"mount_volume": types.ObjectValueMust(objCVMountAttrTypes, map[string]attr.Value{
				"fs_type": types.StringValue("ext4"), "mount_flags": types.ListNull(types.StringType)}),
			"block_volume": types.BoolNull(),
		}),
		"accessibility_requirements": types.ObjectValueMust(objCVAccessibilityAttrTypes, map[string]attr.Value{
			"requisite": types.ListValueMust(types.MapType{ElemType: types.StringType}, []attr.Value{stringMapValue(map[string]string{"zone": "a"}, false)}),
			"preferred": types.ListNull(types.MapType{ElemType: types.StringType}),
		}),
		"capacity_range": types.ObjectValueMust(objCVCapacityAttrTypes, map[string]attr.Value{
			"required_bytes": types.Int64Value(1 << 30), "limit_bytes": types.Int64Null()}),
		"secrets": types.ListValueMust(types.ObjectType{AttrTypes: objCVSecretAttrTypes}, []attr.Value{
			types.ObjectValueMust(objCVSecretAttrTypes, map[string]attr.Value{"key": types.StringValue("token"), "secret": types.StringValue("csi-token")})}),
		"availability": types.StringValue(avail),
	})
}

func TestObjClusterVolumeExpandFlatten(t *testing.T) {
	var d diag.Diagnostics
	spec := objExpandClusterVolumeSpec(context.Background(), objCVTestSpec("pause"), &d)
	if d.HasError() || spec == nil {
		t.Fatal(d)
	}
	if spec.Group != "db" || spec.AccessMode.Scope != "single" || spec.AccessMode.MountVolume.FsType != "ext4" ||
		spec.AccessibilityRequirements.Requisite[0].Segments["zone"] != "a" || spec.CapacityRange.RequiredBytes != 1<<30 ||
		spec.Secrets[0].Secret != "csi-token" || spec.Availability != "pause" {
		t.Fatalf("spec %+v", spec)
	}
	back := objFlattenClusterVolumeSpec(*spec)
	if !back.Equal(objCVTestSpec("pause")) {
		t.Errorf("round trip:\n%v\n%v", back, objCVTestSpec("pause"))
	}
	if objExpandClusterVolumeSpec(context.Background(), types.ObjectNull(objCVSpecAttrTypes), &d) != nil {
		t.Error("null spec must expand to nil")
	}
}

func TestObjClusterVolumeReplaceRule(t *testing.T) {
	run := func(state, plan types.Object) bool {
		var resp objectplanmodifier.RequiresReplaceIfFuncResponse
		objCVRequiresReplace(context.Background(), planmodifier.ObjectRequest{Path: path.Root("cluster_volume_spec"), StateValue: state, PlanValue: plan}, &resp)
		return resp.RequiresReplace
	}
	if run(objCVTestSpec("active"), objCVTestSpec("drain")) {
		t.Error("availability change must be in place")
	}
	other := objCVTestSpec("active").Attributes()
	other["group"] = types.StringValue("other")
	if !run(objCVTestSpec("active"), types.ObjectValueMust(objCVSpecAttrTypes, other)) {
		t.Error("group change must replace")
	}
	if !run(types.ObjectNull(objCVSpecAttrTypes), objCVTestSpec("active")) {
		t.Error("adding a spec must replace")
	}
}

func TestObjClusterVolumeCreateUpdate(t *testing.T) {
	ctx := context.Background()
	f, pd := objFake(t)
	var created map[string]json.RawMessage
	var updated map[string]any
	var updateQuery string
	vol := volume.Volume{Name: "csi", Driver: "csi-plug", Labels: map[string]string{labelManagedBy: labelManagedByValue}}
	exists := false
	f.on("GET /volumes/csi", func(w http.ResponseWriter, _ *http.Request) {
		if !exists {
			p3eWriteJSON(w, 404, map[string]string{"message": "no such volume"})
			return
		}
		p3eWriteJSON(w, 200, vol)
	})
	f.on("POST /volumes/create", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&created)
		var spec volume.ClusterVolumeSpec
		_ = json.Unmarshal(created["ClusterVolumeSpec"], &spec)
		vol.ClusterVolume = &volume.ClusterVolume{ID: "cv1", Meta: swarm.Meta{Version: swarm.Version{Index: 7}}, Spec: spec}
		exists = true
		p3eWriteJSON(w, 201, vol)
	})
	f.on("PUT /volumes/cv1", func(w http.ResponseWriter, r *http.Request) {
		updateQuery = r.URL.Query().Get("version")
		_ = json.NewDecoder(r.Body).Decode(&updated)
		vol.ClusterVolume.Spec.Availability = "drain"
		w.WriteHeader(200)
	})
	r := &volumeResource{pd: pd}
	sr := objSchema(t, r)
	plan := volumeResourceModel{ID: types.StringUnknown(), Name: types.StringValue("csi"), Driver: types.StringValue("csi-plug"),
		DriverOpts: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType), Mountpoint: types.StringUnknown(),
		ClusterVolumeSpec: objCVTestSpec("active"), ClusterVolumeID: types.StringUnknown()}
	plan.contractSetUnknown()
	cresp := resource.CreateResponse{State: objState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: objPlan(t, sr, plan)}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	if len(created["ClusterVolumeSpec"]) == 0 {
		t.Fatal("ClusterVolumeSpec not sent")
	}
	var st volumeResourceModel
	cresp.State.Get(ctx, &st)
	if st.ClusterVolumeID.ValueString() != "cv1" {
		t.Fatalf("cluster_volume_id %v", st.ClusterVolumeID)
	}
	next := st
	next.ClusterVolumeSpec = objCVTestSpec("drain")
	uresp := resource.UpdateResponse{State: objState(t, sr, &st)}
	r.Update(ctx, resource.UpdateRequest{Plan: objPlan(t, sr, next), State: objState(t, sr, &st)}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	if updateQuery != "7" || updated["Spec"].(map[string]any)["Availability"] != "drain" {
		t.Errorf("update version=%q body=%v", updateQuery, updated)
	}
	var after volumeResourceModel
	uresp.State.Get(ctx, &after)
	if after.ClusterVolumeSpec.Attributes()["availability"].(types.String).ValueString() != "drain" {
		t.Errorf("availability after update %v", after.ClusterVolumeSpec)
	}
	// Import of a cluster volume reads the whole spec back.
	imp := volumeFromInspect(vol, volumeResourceModel{DriverOpts: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType),
		ClusterVolumeSpec: types.ObjectNull(objCVSpecAttrTypes)})
	if imp.ClusterVolumeSpec.IsNull() || imp.ClusterVolumeSpec.Attributes()["group"].(types.String).ValueString() != "db" {
		t.Errorf("import spec %v", imp.ClusterVolumeSpec)
	}
}

// TestAccObjClusterVolume creates a Swarm cluster (CSI) volume with the CSI
// plugin named by TF_ACC_CSI_PLUGIN (installed and enabled on every node)
// and then drains it in place. It needs a swarm manager (the -SwarmInit
// harness pass) and skips unless TF_ACC_CSI_PLUGIN is set and enabled there.
// The harness plugin is tfacc-csi (test/harness/tfaccplugin, controller-only:
// create, drain and delete work; publishing to a task does not, so no task
// mounts the volume here).
func TestAccObjClusterVolume(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	env := p3Env(t, "TF_ACC_CSI_PLUGIN")
	if p, err := testAccDockerClient(t).PluginInspect(context.Background(), env[0], client.PluginInspectOptions{}); err != nil || !p.Plugin.Enabled {
		t.Skipf("CSI plugin %s is not enabled on DOCKER_HOST (test/harness/scripts/harness-swarm-init enables it): %v", env[0], err)
	}
	name := acctest.RandomWithPrefix("tfacc-csi")
	cfg := func(avail string) string {
		return fmt.Sprintf(`
resource "docker_volume" "csi" {
  name   = %q
  driver = %q
  cluster_volume_spec = {
    group        = "tfacc"
    availability = %q
    access_mode = {
      scope   = "single"
      sharing = "none"
    }
  }
}
`, name, env[0], avail)
	}
	hAccTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{
			{Config: cfg("active"), Check: tfresource.ComposeAggregateTestCheckFunc(
				tfresource.TestCheckResourceAttrSet("docker_volume.csi", "cluster_volume_id"),
				tfresource.TestCheckResourceAttr("docker_volume.csi", "cluster_volume_spec.availability", "active"),
			)},
			{Config: cfg("drain"), Check: tfresource.TestCheckResourceAttr("docker_volume.csi", "cluster_volume_spec.availability", "drain")},
		},
	})
}
