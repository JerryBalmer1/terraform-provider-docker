package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

func objSchema(t *testing.T, r resource.Resource) resource.SchemaResponse {
	t.Helper()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if d := sr.Schema.ValidateImplementation(context.Background()); d.HasError() {
		t.Fatal(d)
	}
	return sr
}

func objState(t *testing.T, sr resource.SchemaResponse, v any) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(context.Background()), nil)}
	if v != nil {
		if d := st.Set(context.Background(), v); d.HasError() {
			t.Fatal(d)
		}
	}
	return st
}

func objPlan(t *testing.T, sr resource.SchemaResponse, v any) tfsdk.Plan {
	st := objState(t, sr, v)
	return tfsdk.Plan{Schema: st.Schema, Raw: st.Raw}
}

func TestObjNetworkResourceCRUD(t *testing.T) {
	ctx := context.Background()
	f, pd := objFake(t)
	var created map[string]any
	insp := network.Inspect{Network: network.Network{ID: "nid", Name: "app", Driver: "bridge", Scope: "local", EnableIPv6: true,
		IPAM: network.IPAM{Driver: "default", Options: map[string]string{"foo": "bar"}}, Labels: map[string]string{labelManagedBy: labelManagedByValue}}}
	f.on("POST /networks/create", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&created)
		p3eWriteJSON(w, 201, network.CreateResponse{ID: "nid"})
	})
	gone := false
	f.on("GET /networks/nid", func(w http.ResponseWriter, _ *http.Request) {
		if gone {
			p3eWriteJSON(w, 404, map[string]string{"message": "network nid not found"})
			return
		}
		p3eWriteJSON(w, 200, insp)
	})
	removed := false
	f.on("DELETE /networks/nid", func(w http.ResponseWriter, _ *http.Request) { removed = true; w.WriteHeader(204) })

	r := &networkResource{pd: pd}
	sr := objSchema(t, r)
	for _, a := range []string{"enable_ipv6", "ipam_options", "name"} {
		if !strings.Contains(sr.Schema.Attributes[a].GetMarkdownDescription(), "Forces replacement") &&
			!strings.Contains(sr.Schema.Attributes[a].GetMarkdownDescription(), "forces a new resource") {
			t.Errorf("%s description lacks the replacement note", a)
		}
	}
	plan := networkResourceModel{
		ID: types.StringUnknown(), Name: types.StringValue("app"), Driver: types.StringValue("bridge"),
		Attachable: types.BoolValue(false), Internal: types.BoolValue(false), EnableIPv6: types.BoolValue(true), Ingress: types.BoolValue(false),
		IPAMDriver: types.StringValue("default"), IPAMOptions: stringMapValue(map[string]string{"foo": "bar"}, false),
		Options: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType), Scope: types.StringUnknown(),
	}
	plan.contractSetUnknown()
	cresp := resource.CreateResponse{State: objState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: objPlan(t, sr, plan)}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	ipam, _ := created["IPAM"].(map[string]any)
	if created["EnableIPv6"] != true || ipam == nil || fmt.Sprint(ipam["Options"]) != "map[foo:bar]" {
		t.Errorf("create body %v", created)
	}
	var st networkResourceModel
	cresp.State.Get(ctx, &st)
	if st.ID.ValueString() != "nid" || !st.EnableIPv6.ValueBool() || st.InspectJSON.IsUnknown() || !strings.Contains(st.InspectJSON.ValueString(), `"EnableIPv6":true`) ||
		st.ContentSHA256.ValueString() == "" || st.DockerServerVersion.ValueString() != "29.0.0" || len(st.IPAMOptions.Elements()) != 1 {
		t.Fatalf("state %+v", st)
	}

	// Update (no-op plan) re-reads and keeps the contract known.
	uresp := resource.UpdateResponse{State: objState(t, sr, &st)}
	r.Update(ctx, resource.UpdateRequest{Plan: objPlan(t, sr, st), State: objState(t, sr, &st)}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}

	// Read after the network vanished removes it from state.
	gone = true
	rresp := resource.ReadResponse{State: objState(t, sr, &st)}
	r.Read(ctx, resource.ReadRequest{State: objState(t, sr, &st)}, &rresp)
	if rresp.Diagnostics.HasError() || !rresp.State.Raw.IsNull() {
		t.Errorf("read of a gone network: %v %v", rresp.Diagnostics, rresp.State.Raw)
	}
	gone = false
	dresp := resource.DeleteResponse{State: objState(t, sr, &st)}
	r.Delete(ctx, resource.DeleteRequest{State: objState(t, sr, &st)}, &dresp)
	if dresp.Diagnostics.HasError() || !removed {
		t.Errorf("delete %v removed=%v", dresp.Diagnostics, removed)
	}

	// Create error path.
	f.on("POST /networks/create", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, 409, map[string]string{"message": "network with name app already exists"})
	})
	cresp = resource.CreateResponse{State: objState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: objPlan(t, sr, plan)}, &cresp)
	if !cresp.Diagnostics.HasError() || !strings.Contains(cresp.Diagnostics[0].Detail(), "already exists") {
		t.Errorf("create error: %v", cresp.Diagnostics)
	}
}

func TestObjVolumeResourceCRUD(t *testing.T) {
	ctx := context.Background()
	f, pd := objFake(t)
	vol := volume.Volume{Name: "data", Driver: "local", Mountpoint: "/m", Labels: map[string]string{labelManagedBy: labelManagedByValue}}
	exists := false
	f.on("GET /volumes/data", func(w http.ResponseWriter, _ *http.Request) {
		if !exists {
			p3eWriteJSON(w, 404, map[string]string{"message": "no such volume"})
			return
		}
		p3eWriteJSON(w, 200, vol)
	})
	f.on("POST /volumes/create", func(w http.ResponseWriter, _ *http.Request) { exists = true; p3eWriteJSON(w, 201, vol) })
	f.on("DELETE /volumes/data", func(w http.ResponseWriter, _ *http.Request) { exists = false; w.WriteHeader(204) })

	r := &volumeResource{pd: pd}
	sr := objSchema(t, r)
	plan := volumeResourceModel{ID: types.StringUnknown(), Name: types.StringValue("data"), Driver: types.StringValue("local"),
		DriverOpts: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType), Mountpoint: types.StringUnknown(),
		ClusterVolumeSpec: types.ObjectNull(objCVSpecAttrTypes), ClusterVolumeID: types.StringUnknown()}
	plan.contractSetUnknown()
	cresp := resource.CreateResponse{State: objState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: objPlan(t, sr, plan)}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	var st volumeResourceModel
	cresp.State.Get(ctx, &st)
	if st.Mountpoint.ValueString() != "/m" || !strings.Contains(st.InspectJSON.ValueString(), `"Mountpoint":"/m"`) || st.ContentSHA256.IsNull() {
		t.Fatalf("state %+v", st)
	}
	// Sensitive raw output.
	if a := sr.Schema.Attributes["inspect_json"]; !a.IsSensitive() {
		t.Error("inspect_json must be sensitive")
	}
	// A second create of the same name is refused (the daemon would adopt it).
	cresp = resource.CreateResponse{State: objState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: objPlan(t, sr, plan)}, &cresp)
	if !cresp.Diagnostics.HasError() {
		t.Error("existing volume must be refused")
	}
	// Import + read.
	f.json("GET /volumes", volume.ListResponse{Volumes: []volume.Volume{vol}})
	iresp := resource.ImportStateResponse{State: objState(t, sr, nil)}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "data"}, &iresp)
	if iresp.Diagnostics.HasError() {
		t.Fatal(iresp.Diagnostics)
	}
	rresp := resource.ReadResponse{State: iresp.State}
	r.Read(ctx, resource.ReadRequest{State: iresp.State}, &rresp)
	var imp volumeResourceModel
	rresp.State.Get(ctx, &imp)
	if rresp.Diagnostics.HasError() || imp.Name.ValueString() != "data" || imp.InspectJSON.IsNull() {
		t.Errorf("import read %v %+v", rresp.Diagnostics, imp)
	}
	dresp := resource.DeleteResponse{State: objState(t, sr, &st)}
	r.Delete(ctx, resource.DeleteRequest{State: objState(t, sr, &st)}, &dresp)
	if dresp.Diagnostics.HasError() || exists {
		t.Errorf("delete %v", dresp.Diagnostics)
	}
}

func TestObjPluginUpgradeInPlace(t *testing.T) {
	ctx := context.Background()
	f, pd := pluginFake(t)
	r := &pluginResource{pd: pd}
	sr := objSchema(t, r)
	plan := pluginTestModel(pluginGrant("network", "host"), pluginGrant("capabilities", "CAP_SYS_ADMIN"))
	cresp := resource.CreateResponse{State: objState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: objPlan(t, sr, plan)}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	var st pluginResourceModel
	cresp.State.Get(ctx, &st)
	if st.InspectJSON.IsNull() || !strings.Contains(st.InspectJSON.ValueString(), `"Id":"pid1"`) {
		t.Fatalf("inspect_json %v", st.InspectJSON)
	}
	f.calls = nil
	next := st
	next.Name = types.StringValue("vieux/sshfs:next")
	next.contractSetUnknown()
	uresp := resource.UpdateResponse{State: objState(t, sr, &st)}
	r.Update(ctx, resource.UpdateRequest{Plan: objPlan(t, sr, next), State: objState(t, sr, &st)}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "disable,privileges,upgrade,enable" {
		t.Errorf("upgrade calls = %s", got)
	}
	uresp.State.Get(ctx, &st)
	if st.PluginReference.ValueString() != "docker.io/vieux/sshfs:next" || st.Alias.ValueString() != "sshfs" || !st.Enabled.ValueBool() {
		t.Errorf("after upgrade %+v", st)
	}
}

func TestObjPluginNameReplaceRule(t *testing.T) {
	ctx := context.Background()
	sr := objSchema(t, NewPluginResource())
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	cfg := func(alias tftypes.Value) tfsdk.Config {
		return tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(typ, map[string]tftypes.Value{"name": p3eStr("a/b:2"), "alias": alias})}
	}
	for _, tc := range []struct {
		alias tftypes.Value
		want  bool
	}{{tftypes.NewValue(tftypes.String, nil), true}, {p3eStr("b"), false}} {
		req := planmodifier.StringRequest{Config: cfg(tc.alias), StateValue: types.StringValue("a/b:1"), PlanValue: types.StringValue("a/b:2")}
		resp := stringplanmodifier.RequiresReplaceIfFuncResponse{}
		pluginNameRequiresReplace(ctx, req, &resp)
		if resp.RequiresReplace != tc.want {
			t.Errorf("alias %v: replace=%v", tc.alias, resp.RequiresReplace)
		}
	}
}

var _ = client.NetworkCreateOptions{}
