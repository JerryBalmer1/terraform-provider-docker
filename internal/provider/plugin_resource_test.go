package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/plugin"
	"github.com/moby/moby/client"
)

// pluginFakeEngine serves the /plugins endpoints PluginInstall and friends use.
type pluginFakeEngine struct {
	mu      sync.Mutex
	privs   plugin.Privileges
	plugins map[string]*plugin.Plugin // by ID
	calls   []string
	granted plugin.Privileges
	timeout string
}

func (f *pluginFakeEngine) find(ref string) *plugin.Plugin {
	for _, p := range f.plugins {
		if p.ID == ref || p.Name == ref || pluginFamiliar(p.Name) == pluginFamiliar(ref) {
			return p
		}
	}
	return nil
}

func (f *pluginFakeEngine) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if i := strings.Index(p, "/plugins"); i >= 0 {
		p = p[i+len("/plugins"):]
	}
	q := r.URL.Query()
	notFound := func(ref string) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"message":"plugin %q not found"}`, ref)
	}
	switch {
	case r.Method == http.MethodGet && p == "/privileges":
		f.calls = append(f.calls, "privileges")
		_ = json.NewEncoder(w).Encode(f.privs)
	case r.Method == http.MethodPost && p == "/pull":
		f.calls = append(f.calls, "pull")
		_ = json.NewDecoder(r.Body).Decode(&f.granted)
		name := q.Get("name")
		if name == "" {
			name = pluginFamiliar(q.Get("remote"))
		} else if !strings.Contains(name, ":") {
			name += ":latest"
		}
		f.plugins["pid1"] = &plugin.Plugin{ID: "pid1", Name: name, PluginReference: "docker.io/" + pluginFamiliar(q.Get("remote")),
			Settings: plugin.Settings{Env: []string{"DEBUG=0", "MODE=a"}}}
		w.Header().Set("Docker-Plugin-Name", name)
		_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"status":"Download complete"}`+"\n")
	case strings.HasSuffix(p, "/json") && r.Method == http.MethodGet:
		ref := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/json")
		pl := f.find(ref)
		if pl == nil {
			notFound(ref)
			return
		}
		_ = json.NewEncoder(w).Encode(pl)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/upgrade"):
		ref := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/upgrade")
		pl := f.find(ref)
		if pl == nil {
			notFound(ref)
			return
		}
		f.calls = append(f.calls, "upgrade")
		if pl.Enabled {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"plugin must be disabled before upgrading"}`)
			return
		}
		pl.PluginReference = "docker.io/" + pluginFamiliar(q.Get("remote"))
		_, _ = io.WriteString(w, `{"status":"Upgraded"}`+"\n")
	case r.Method == http.MethodPost && (strings.HasSuffix(p, "/set") || strings.HasSuffix(p, "/enable") || strings.HasSuffix(p, "/disable")):
		i := strings.LastIndex(p, "/")
		ref, op := strings.TrimPrefix(p[:i], "/"), p[i+1:]
		pl := f.find(ref)
		if pl == nil {
			notFound(ref)
			return
		}
		f.calls = append(f.calls, op)
		switch op {
		case "set":
			if pl.Enabled {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, `{"message":"cannot set on an active plugin, disable plugin before setting"}`)
				return
			}
			var args []string
			_ = json.NewDecoder(r.Body).Decode(&args)
			for _, a := range args {
				k, _, _ := strings.Cut(a, "=")
				for j, e := range pl.Settings.Env {
					if strings.HasPrefix(e, k+"=") {
						pl.Settings.Env[j] = a
					}
				}
			}
		case "enable":
			f.timeout = q.Get("timeout")
			pl.Enabled = true
		case "disable":
			pl.Enabled = false
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete:
		ref := strings.TrimPrefix(p, "/")
		pl := f.find(ref)
		if pl == nil {
			notFound(ref)
			return
		}
		if pl.Enabled && q.Get("force") != "1" {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"plugin is enabled"}`)
			return
		}
		f.calls = append(f.calls, "remove")
		delete(f.plugins, pl.ID)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(w, `{"message":"fake: %s %s"}`, r.Method, p)
	}
}

func pluginFake(t *testing.T) (*pluginFakeEngine, *providerData) {
	t.Helper()
	f := &pluginFakeEngine{plugins: map[string]*plugin.Plugin{}, privs: plugin.Privileges{
		{Name: "network", Value: []string{"host"}}, {Name: "capabilities", Value: []string{"CAP_SYS_ADMIN"}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	return f, &providerData{Client: c}
}

func pluginTestModel(grants ...pluginGrantModel) pluginResourceModel {
	m := pluginResourceModel{
		ID: types.StringUnknown(), Name: types.StringValue("vieux/sshfs"), PluginReference: types.StringUnknown(),
		Alias: types.StringValue("sshfs"), Enabled: types.BoolValue(true), GrantAllPermissions: types.BoolNull(),
		GrantPermissions: grants, Env: stringSetValue([]string{"DEBUG=1"}, false),
		EnableTimeout: types.Int64Value(90), ForceDestroy: types.BoolValue(false), ForceDisable: types.BoolValue(false),
	}
	m.contractSetUnknown()
	return m
}

func pluginGrant(name string, values ...string) pluginGrantModel {
	return pluginGrantModel{Name: types.StringValue(name), Value: stringSetValue(values, false)}
}

func TestPluginAcceptFunc(t *testing.T) {
	ctx := context.Background()
	privs := plugin.Privileges{{Name: "network", Value: []string{"host"}}, {Name: "mount", Value: []string{"/a", "/b"}}}
	if ok, err := pluginAcceptFunc(true, nil)(ctx, privs); !ok || err != nil {
		t.Errorf("grant all: %v %v", ok, err)
	}
	if ok, err := pluginAcceptFunc(false, []pluginGrantModel{pluginGrant("network", "host"), pluginGrant("mount", "/b", "/a")})(ctx, privs); !ok || err != nil {
		t.Errorf("exact grants: %v %v", ok, err)
	}
	ok, err := pluginAcceptFunc(false, []pluginGrantModel{pluginGrant("network", "host"), pluginGrant("mount", "/a")})(ctx, privs)
	if ok || err == nil || !strings.Contains(err.Error(), "mount=/b") {
		t.Errorf("missing value: %v %v", ok, err)
	}
	if _, err := pluginAcceptFunc(false, nil)(ctx, privs); err == nil || !strings.Contains(err.Error(), "network=host") {
		t.Errorf("no grants: %v", err)
	}
}

func TestPluginEnvRead(t *testing.T) {
	got := []string{"DEBUG=1", "MODE=a"}
	if v := pluginEnvRead(types.SetUnknown(types.StringType), got); len(v.Elements()) != 2 {
		t.Errorf("unknown prior reports all: %v", v)
	}
	if v := pluginEnvRead(stringSetValue([]string{"DEBUG=0"}, false), got); len(v.Elements()) != 1 ||
		!v.Equal(stringSetValue([]string{"DEBUG=1"}, false)) {
		t.Errorf("prior keys only: %v", v)
	}
}

func TestPluginResourceFakeEngine(t *testing.T) {
	ctx := context.Background()
	f, pd := pluginFake(t)
	r := &pluginResource{pd: pd}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)

	// Missing a grant: install fails before pulling.
	cresp := resource.CreateResponse{State: tfState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan(t, sr, pluginTestModel(pluginGrant("network", "host")))}, &cresp)
	if !cresp.Diagnostics.HasError() || !strings.Contains(fmt.Sprint(cresp.Diagnostics), "capabilities=CAP_SYS_ADMIN") {
		t.Fatalf("want a missing-privilege error, got %v", cresp.Diagnostics)
	}
	if strings.Join(f.calls, ",") != "privileges" {
		t.Errorf("calls after denied install: %v", f.calls)
	}

	// Fully granted: pull (disabled) with env args, then enable with the timeout.
	f.calls = nil
	plan := pluginTestModel(pluginGrant("network", "host"), pluginGrant("capabilities", "CAP_SYS_ADMIN"))
	cresp = resource.CreateResponse{State: tfState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan(t, sr, plan)}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "privileges,pull,set,enable" {
		t.Errorf("install calls = %s", got)
	}
	if f.timeout != "90" || len(f.granted) != 2 {
		t.Errorf("timeout=%q granted=%v", f.timeout, f.granted)
	}
	var st pluginResourceModel
	cresp.State.Get(ctx, &st)
	if st.ID.ValueString() != "pid1" || st.Alias.ValueString() != "sshfs" || !st.Enabled.ValueBool() ||
		st.PluginReference.ValueString() != "docker.io/vieux/sshfs:latest" || !st.Env.Equal(stringSetValue([]string{"DEBUG=1"}, false)) {
		t.Fatalf("state = %+v", st)
	}

	// Env change on an enabled plugin: disable, set, re-enable.
	f.calls = nil
	next := st
	next.Env = stringSetValue([]string{"DEBUG=2"}, false)
	uresp := resource.UpdateResponse{State: tfState(t, sr, &st)}
	r.Update(ctx, resource.UpdateRequest{Plan: tfPlan(t, sr, next), State: tfState(t, sr, &st)}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "disable,set,enable" {
		t.Errorf("update calls = %s", got)
	}
	uresp.State.Get(ctx, &st)
	if !st.Env.Equal(stringSetValue([]string{"DEBUG=2"}, false)) || !st.Enabled.ValueBool() {
		t.Errorf("after update: %+v", st)
	}

	// Data source by alias.
	ds := &pluginDataSource{pd: pd}
	var dsr datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &dsr)
	dm := pluginDataSourceModel{ID: types.StringNull(), Alias: types.StringValue("sshfs"), Name: types.StringUnknown(),
		PluginReference: types.StringUnknown(), Enabled: types.BoolUnknown(), Env: types.SetUnknown(types.StringType)}
	dm.contractSetUnknown()
	cfg := tfsdk.Config{Schema: dsr.Schema, Raw: tftypes.NewValue(dsr.Schema.Type().TerraformType(ctx), nil)}
	dst := tfsdk.State{Schema: dsr.Schema, Raw: tftypes.NewValue(dsr.Schema.Type().TerraformType(ctx), nil)}
	if d := dst.Set(ctx, &dm); d.HasError() {
		t.Fatal(d)
	}
	cfg.Raw = dst.Raw
	dresp := datasource.ReadResponse{State: dst}
	ds.Read(ctx, datasource.ReadRequest{Config: cfg}, &dresp)
	if dresp.Diagnostics.HasError() {
		t.Fatal(dresp.Diagnostics)
	}
	var got pluginDataSourceModel
	dresp.State.Get(ctx, &got)
	if got.ID.ValueString() != "pid1" || got.Name.ValueString() != "vieux/sshfs:latest" || got.Alias.ValueString() != "sshfs" || len(got.Env.Elements()) != 2 {
		t.Errorf("data source = %+v", got)
	}

	// Destroy: disable then remove (no force).
	f.calls = nil
	delResp := resource.DeleteResponse{State: tfState(t, sr, &st)}
	r.Delete(ctx, resource.DeleteRequest{State: tfState(t, sr, &st)}, &delResp)
	if delResp.Diagnostics.HasError() || strings.Join(f.calls, ",") != "disable,remove" || len(f.plugins) != 0 {
		t.Errorf("delete: %v calls=%v", delResp.Diagnostics, f.calls)
	}
}

func TestPluginExamplePlan(t *testing.T) {
	testExamplePlan(t, filepath.Join("resources", "docker_plugin", "resource.tf"))
}

func TestPluginGrantConflictPlan(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{{
			PlanOnly: true,
			Config: `
resource "docker_plugin" "p" {
  name                  = "vieux/sshfs"
  grant_all_permissions = true
  grant_permissions {
    name  = "network"
    value = ["host"]
  }
}`,
			ExpectError: mustRegexpT(t, `Invalid Attribute Combination`),
		}},
	})
}

// TestAccPlugin_basic installs vieux/sshfs. It needs TF_ACC, DOCKER_HOST and
// TF_ACC_PLUGIN=1 (the daemon must reach Docker Hub and allow managed plugins).
func TestAccPlugin_basic(t *testing.T) {
	testAccPreCheck(t)
	testAccPluginPreCheck(t)
	c := testAccDockerClient(t)
	cfg := func(debug string, enabled bool) string {
		return fmt.Sprintf(`
resource "docker_plugin" "sshfs" {
  name                  = "vieux/sshfs:latest"
  alias                 = "tfacc-sshfs"
  grant_all_permissions = true
  env                   = ["DEBUG=%s"]
  enabled               = %t
  force_destroy         = true
}

data "docker_plugin" "by_alias" {
  alias = docker_plugin.sshfs.alias
}
`, debug, enabled)
	}
	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if _, err := c.PluginInspect(context.Background(), "tfacc-sshfs", client.PluginInspectOptions{}); !isNotFound(err) {
				return fmt.Errorf("plugin still installed (%v)", err)
			}
			return nil
		},
		Steps: []tfresource.TestStep{
			{
				Config: cfg("0", true),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("docker_plugin.sshfs", "enabled", "true"),
					tfresource.TestCheckResourceAttrPair("data.docker_plugin.by_alias", "id", "docker_plugin.sshfs", "id"),
				),
			},
			{
				Config: cfg("1", false),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("docker_plugin.sshfs", "enabled", "false"),
					tfresource.TestCheckTypeSetElemAttr("docker_plugin.sshfs", "env.*", "DEBUG=1"),
				),
			},
			{
				ResourceName:            "docker_plugin.sshfs",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"grant_all_permissions", "env", "force_destroy", "alias", "name"},
			},
		},
	})
}

// testAccPluginPreCheck skips plugin acceptance tests unless TF_ACC_PLUGIN is
// set: they pull from Docker Hub and need a daemon that runs managed plugins.
func testAccPluginPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC_PLUGIN") == "" {
		t.Skip("TF_ACC_PLUGIN not set; skipping docker_plugin acceptance test")
	}
}
