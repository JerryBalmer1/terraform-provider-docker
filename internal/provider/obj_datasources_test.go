package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/plugin"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/api/types/volume"
)

// objReadDS configures d against pd, validates its schema and runs Read with cfg.
func objReadDS(t *testing.T, d datasource.DataSource, pd *providerData, cfg map[string]tftypes.Value) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	if dc, ok := d.(datasource.DataSourceWithConfigure); ok {
		dc.Configure(ctx, datasource.ConfigureRequest{ProviderData: pd}, &datasource.ConfigureResponse{})
	}
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	if diags := sr.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema: %v", diags)
	}
	typ := sr.Schema.Type().TerraformType(ctx)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(typ, cfg)}}, &resp)
	return resp.State, resp.Diagnostics
}

// objAttr returns the Go string of a top-level string attribute.
func objAttr(t *testing.T, st tfsdk.State, name string) string {
	t.Helper()
	var v types.String
	if d := st.GetAttribute(context.Background(), path.Root(name), &v); d.HasError() {
		t.Fatalf("%s: %v", name, d)
	}
	return v.ValueString()
}

func objStrings(t *testing.T, st tfsdk.State, name string) []string {
	t.Helper()
	var v []string
	if d := st.GetAttribute(context.Background(), path.Root(name), &v); d.HasError() {
		t.Fatalf("%s: %v", name, d)
	}
	return v
}

// objFake is newP3eFake with GET /version and GET /info answered.
func objFake(t *testing.T) (*p3eFake, *providerData) {
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47", "Platform": map[string]string{"Name": "Docker Engine - Community"},
		"Components": []map[string]any{{"Name": "Engine", "Version": "29.0.0", "Details": map[string]string{"GitCommit": "abc", "KernelVersion": "6.1"}}}})
	f.json("GET /info", system.Info{ID: "daemon-1", Name: "host-a", NCPU: 4})
	return f, pd
}

func TestObjNetworksDataSource(t *testing.T) {
	f, pd := objFake(t)
	var query string
	f.on("GET /networks", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("filters")
		p3eWriteJSON(w, 200, []network.Summary{{Network: network.Network{ID: "n2", Name: "b", Driver: "bridge"}}, {Network: network.Network{ID: "n1", Name: "a", Driver: "overlay"}}})
	})
	st, d := objReadDS(t, NewNetworksDataSource(), pd, map[string]tftypes.Value{"driver": p3eStr("bridge"), "labels": p3eStrMap(map[string]string{"team": "x"})})
	if d.HasError() {
		t.Fatal(d)
	}
	if !strings.Contains(query, `"driver":{"bridge":true}`) || !strings.Contains(query, `"label":{"team=x":true}`) {
		t.Errorf("filters %q", query)
	}
	if got := objStrings(t, st, "names"); strings.Join(got, ",") != "a,b" {
		t.Errorf("names %v", got)
	}
	if got := objStrings(t, st, "ids"); strings.Join(got, ",") != "n1,n2" {
		t.Errorf("ids %v", got)
	}
	raw := objAttr(t, st, "networks_json")
	var back []map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil || len(back) != 2 || back[0]["Name"] != "a" {
		t.Errorf("networks_json %s (%v)", raw, err)
	}
	if objAttr(t, st, "content_sha256") == "" || objAttr(t, st, "docker_server_version") != "29.0.0" {
		t.Errorf("hash/version not filled")
	}
	var nets types.List
	st.GetAttribute(context.Background(), path.Root("networks"), &nets)
	if len(nets.Elements()) != 2 {
		t.Errorf("structured networks: %v", nets)
	}
}

func TestObjNetworksDataSourceError(t *testing.T) {
	f, pd := objFake(t)
	f.on("GET /networks", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, 500, map[string]string{"message": "boom"})
	})
	if _, d := objReadDS(t, NewNetworksDataSource(), pd, nil); !d.HasError() || !strings.Contains(d[0].Detail(), "boom") {
		t.Errorf("want error, got %v", d)
	}
	if _, d := objReadDS(t, NewNetworksDataSource(), nil, nil); !d.HasError() {
		t.Error("unconfigured provider must fail")
	}
}

func TestObjVolumesDataSource(t *testing.T) {
	f, pd := objFake(t)
	var query string
	f.on("GET /volumes", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("filters")
		p3eWriteJSON(w, 200, volume.ListResponse{Volumes: []volume.Volume{{Name: "z", Driver: "local"}, {Name: "a", Driver: "local", Options: map[string]string{"o": "secret"}}}})
	})
	st, d := objReadDS(t, NewVolumesDataSource(), pd, map[string]tftypes.Value{"dangling": tftypes.NewValue(tftypes.Bool, true)})
	if d.HasError() {
		t.Fatal(d)
	}
	if !strings.Contains(query, `"dangling":{"true":true}`) {
		t.Errorf("filters %q", query)
	}
	if got := objStrings(t, st, "names"); strings.Join(got, ",") != "a,z" {
		t.Errorf("names %v", got)
	}
	if raw := objAttr(t, st, "volumes_json"); !strings.Contains(raw, `"Warnings":[]`) || !strings.Contains(raw, `"secret"`) {
		t.Errorf("volumes_json %s", raw)
	}
}

func TestObjPluginsDataSource(t *testing.T) {
	f, pd := objFake(t)
	f.json("GET /plugins", []plugin.Plugin{{ID: "p2", Name: "vieux/sshfs:latest", Enabled: true}, {ID: "p1", Name: "a/b:1"}})
	st, d := objReadDS(t, NewPluginsDataSource(), pd, map[string]tftypes.Value{"enabled": tftypes.NewValue(tftypes.Bool, true)})
	if d.HasError() {
		t.Fatal(d)
	}
	if got := objStrings(t, st, "ids"); strings.Join(got, ",") != "p1,p2" {
		t.Errorf("ids %v", got)
	}
	if got := objStrings(t, st, "names"); got[1] != "vieux/sshfs:latest" {
		t.Errorf("names %v", got)
	}
	if !strings.Contains(strings.Join(f.requests(), " "), "enabled") {
		t.Errorf("enabled filter not sent: %v", f.requests())
	}
}

func TestObjInfoVersionDf(t *testing.T) {
	f, pd := objFake(t)
	st, d := objReadDS(t, NewInfoDataSource(), pd, nil)
	if d.HasError() {
		t.Fatal(d)
	}
	if objAttr(t, st, "id") != "daemon-1" || objAttr(t, st, "name") != "host-a" || !strings.Contains(objAttr(t, st, "info_json"), `"NCPU":4`) {
		t.Errorf("info: %s", objAttr(t, st, "info_json"))
	}

	st, d = objReadDS(t, NewVersionDataSource(), pd, nil)
	if d.HasError() {
		t.Fatal(d)
	}
	if objAttr(t, st, "id") != "daemon-1" || objAttr(t, st, "platform_name") != "Docker Engine - Community" {
		t.Errorf("version identity")
	}
	raw := objAttr(t, st, "version_json")
	if !strings.Contains(raw, `"GitCommit":"abc"`) || !strings.Contains(raw, `"ApiVersion":"1.47"`) {
		t.Errorf("version_json %s", raw)
	}

	var dfQuery string
	f.on("GET /system/df", func(w http.ResponseWriter, r *http.Request) {
		dfQuery = r.URL.RawQuery
		p3eWriteJSON(w, 200, map[string]any{"LayersSize": 10, "Images": []any{}, "Containers": []any{}, "Volumes": []any{}, "BuildCache": []any{}})
	})
	st, d = objReadDS(t, NewSystemDfDataSource(), pd, map[string]tftypes.Value{"verbose": tftypes.NewValue(tftypes.Bool, true)})
	if d.HasError() {
		t.Fatal(d)
	}
	if !strings.Contains(dfQuery, "verbose=1") || objAttr(t, st, "id") != "daemon-1" || !strings.Contains(objAttr(t, st, "df_json"), `"ImageUsage"`) {
		t.Errorf("df: %q %s", dfQuery, objAttr(t, st, "df_json"))
	}
}

func TestObjNetworkDataSourceContract(t *testing.T) {
	f, pd := objFake(t)
	insp := network.Inspect{Network: network.Network{ID: "nid", Name: "app", Driver: "bridge", Scope: "local", EnableIPv6: true},
		Containers: map[string]network.EndpointResource{"cid": {Name: "web", EndpointID: "ep1", IPv4Address: mustPrefix("172.19.0.2/16")}}}
	f.json("GET /networks/app", insp)
	f.json("GET /networks/nid", insp)
	f.json("GET /networks", []network.Summary{{Network: network.Network{ID: "nid", Name: "app"}}})
	st, d := objReadDS(t, NewNetworkDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("app")})
	if d.HasError() {
		t.Fatal(d)
	}
	var m networkDataSourceModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	if !m.EnableIPv6.ValueBool() || m.InspectJSON.IsNull() || m.ContentSHA256.IsNull() {
		t.Errorf("model %+v", m)
	}
	c := m.Containers.Elements()["cid"]
	if c == nil || !strings.Contains(c.String(), `"ipv4_address":"172.19.0.2/16"`) || !strings.Contains(c.String(), `"name":"web"`) {
		t.Errorf("containers %v", m.Containers)
	}
}

func TestObjVolumeDataSourceContract(t *testing.T) {
	f, pd := objFake(t)
	f.json("GET /volumes/data", volume.Volume{Name: "data", Driver: "local", Mountpoint: "/var/lib/docker/volumes/data/_data"})
	st, d := objReadDS(t, NewVolumeDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("data")})
	if d.HasError() {
		t.Fatal(d)
	}
	if objAttr(t, st, "mountpoint") == "" || !strings.Contains(objAttr(t, st, "inspect_json"), `"Name":"data"`) {
		t.Errorf("inspect_json %s", objAttr(t, st, "inspect_json"))
	}
	f.on("GET /volumes/gone", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, 404, map[string]string{"message": "no such volume"})
	})
	if _, d := objReadDS(t, NewVolumeDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("gone")}); !d.HasError() {
		t.Error("missing volume must fail")
	}
}

// objIdentityOutputsPresent checks that each schema has its item's identity outputs.
func TestObjIdentityOutputs(t *testing.T) {
	ctx := context.Background()
	dss := map[string]datasource.DataSource{
		"engine:networks": NewNetworksDataSource(), "engine:volumes": NewVolumesDataSource(), "engine:plugins": NewPluginsDataSource(),
		"engine:info": NewInfoDataSource(), "engine:version": NewVersionDataSource(), "engine:system-df": NewSystemDfDataSource(),
		"engine:network-data": NewNetworkDataSource(), "engine:volume-data": NewVolumeDataSource(), "engine:plugin-data": NewPluginDataSource(),
	}
	for id, d := range dss {
		var sr datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &sr)
		for _, n := range contractIdentityOutputNames(id) {
			if _, ok := sr.Schema.Attributes[n]; !ok {
				t.Errorf("%s: identity output %q missing", id, n)
			}
		}
	}
	rs := map[string]resource.Resource{"engine:network": NewNetworkResource(), "engine:volume": NewVolumeResource(), "engine:plugin": NewPluginResource()}
	for id, r := range rs {
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s: %v", id, d)
		}
		for _, n := range contractIdentityOutputNames(id) {
			if _, ok := sr.Schema.Attributes[n]; !ok {
				t.Errorf("%s: identity output %q missing", id, n)
			}
		}
	}
}
