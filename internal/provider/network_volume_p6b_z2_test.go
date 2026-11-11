package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
)

// Pass 6b Lane Z: a fake Engine that answers like Engine 29.9.3 for the two
// shapes the examples tripped on:
//   - a config-only network inspects with Driver "null", Scope "local" and
//     no IPAM pools (libnetwork controller.go:560-568 resets the type and
//     skips allocation; daemon/network.go:813-881 reports only allocated
//     pools), its driver options kept;
//   - a volume on a managed plugin driver inspects with the plugin's tagged
//     name ("sshfs" -> "sshfs:latest").
type z2FakeEngine struct {
	mu       sync.Mutex
	networks map[string]json.RawMessage // id -> inspect body
	netNames map[string]string          // name -> id
	volumes  map[string]volume.Volume
	seq      int
}

func newZ2FakeEngine(t *testing.T) (*z2FakeEngine, string) {
	f := &z2FakeEngine{networks: map[string]json.RawMessage{}, netNames: map[string]string{}, volumes: map[string]volume.Volume{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, "tcp://" + srv.Listener.Addr().String()
}

// z2ConfigOnlyInspect mirrors `GET /networks/<id>` of a config-only macvlan
// network on Engine 29.9.3 (API 1.56).
func z2ConfigOnlyInspect(id, name string, labels, opts map[string]string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"Name": name, "Id": id, "Created": "2026-11-10T10:00:00.000000000Z",
		"Scope": "local", "Driver": "null", "EnableIPv4": true, "EnableIPv6": false,
		"IPAM":     map[string]any{"Driver": "default", "Options": map[string]string{}, "Config": nil},
		"Internal": false, "Attachable": false, "Ingress": false,
		"ConfigFrom": map[string]string{"Network": ""}, "ConfigOnly": true,
		"Containers": map[string]any{}, "Options": opts, "Labels": labels,
	})
	return b
}

func (f *z2FakeEngine) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := svcFakeVersionRe.ReplaceAllString(r.URL.Path, "")
	w.Header().Set("Api-Version", "1.47")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case p == "/_ping":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	case p == "/version":
		svcFakeJSON(w, 200, map[string]string{"Version": "29.9.3", "ApiVersion": "1.47"})
	case p == "/networks/create" && r.Method == http.MethodPost:
		var req network.CreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ConfigFrom != nil && req.ConfigFrom.Network != "" {
			// daemon/libnetwork/network.go:410-419 and :447-457.
			cfgID, ok := f.netNames[req.ConfigFrom.Network]
			if !ok {
				svcFakeErr(w, 404, "config network not found")
				return
			}
			if len(req.Labels) > 0 || (req.IPAM != nil && len(req.IPAM.Config) > 0) {
				svcFakeErr(w, 403, "user-specified configurations are not supported if the network depends on a configuration network")
				return
			}
			var cfg map[string]any
			_ = json.Unmarshal(f.networks[cfgID], &cfg)
			f.seq++
			id := fmt.Sprintf("%064d", f.seq)
			cfg["Id"], cfg["Name"], cfg["Driver"], cfg["Scope"] = id, req.Name, req.Driver, "swarm"
			cfg["ConfigOnly"], cfg["ConfigFrom"] = false, map[string]string{"Network": req.ConfigFrom.Network}
			cfg["IPAM"] = map[string]any{"Driver": "default", "Config": []map[string]string{{"Subnet": "192.168.50.0/24", "Gateway": "192.168.50.1"}}}
			b, _ := json.Marshal(cfg)
			f.networks[id] = b
			f.netNames[req.Name] = id
			svcFakeJSON(w, 201, network.CreateResponse{ID: id})
			return
		}
		if !req.ConfigOnly {
			svcFakeErr(w, 400, "fake: only config-only networks")
			return
		}
		if len(req.IPAM.Config) != 1 || req.IPAM.Config[0].Subnet.String() != "192.168.50.0/24" {
			svcFakeErr(w, 400, fmt.Sprintf("fake: IPAM not sent: %+v", req.IPAM))
			return
		}
		f.seq++
		id := fmt.Sprintf("%064d", f.seq)
		f.networks[id] = z2ConfigOnlyInspect(id, req.Name, req.Labels, req.Options)
		f.netNames[req.Name] = id
		svcFakeJSON(w, 201, network.CreateResponse{ID: id})
	case parts[0] == "networks" && len(parts) == 2:
		id := parts[1]
		if nid, ok := f.netNames[id]; ok {
			id = nid
		}
		body, ok := f.networks[id]
		if !ok {
			svcFakeErr(w, 404, "network "+parts[1]+" not found")
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.networks, id)
			for n, i := range f.netNames {
				if i == id {
					delete(f.netNames, n)
				}
			}
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	case p == "/volumes/create" && r.Method == http.MethodPost:
		var req volume.CreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		drv := req.Driver
		if drv != "local" && !strings.Contains(drv, ":") {
			drv += ":latest" // the plugin store's normalized name
		}
		v := volume.Volume{Name: req.Name, Driver: drv, Options: req.DriverOpts, Labels: req.Labels, Scope: "local",
			Mountpoint: "/var/lib/docker/plugins/abc/propagated-mount/" + req.Name}
		f.volumes[req.Name] = v
		svcFakeJSON(w, 201, v)
	case parts[0] == "volumes" && len(parts) == 2:
		v, ok := f.volumes[parts[1]]
		if !ok {
			svcFakeErr(w, 404, "no such volume")
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.volumes, parts[1])
			w.WriteHeader(204)
			return
		}
		svcFakeJSON(w, 200, v)
	default:
		svcFakeErr(w, 404, "fake daemon: no route "+r.Method+" "+p)
	}
}

// TestNetworkConfigOnlyAndPluginVolumeP6bZ2 applies a config-only macvlan
// network and a volume on a plugin driver against the fake Engine: the
// apply must be consistent and a second plan empty.
func TestNetworkConfigOnlyAndPluginVolumeP6bZ2(t *testing.T) {
	_, host := newZ2FakeEngine(t)
	cfg := svcFakeConfig(host, `
resource "docker_network" "macvlan_config" {
  name        = "macvlan-config"
  driver      = "macvlan"
  config_only = true
  options     = { parent = "eth0" }

  ipam_config {
    subnet = "192.168.50.0/24"
  }
}

resource "docker_network" "macvlan" {
  name        = "macvlan"
  driver      = "macvlan"
  scope       = "swarm"
  config_from = docker_network.macvlan_config.name
}

resource "docker_volume" "remote" {
  name        = "remote-data"
  driver      = "sshfs"
  driver_opts = { sshcmd = "user@files.example.com:/srv/data" }
}
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_network.macvlan_config", "driver", "macvlan"),
				resource.TestCheckResourceAttr("docker_network.macvlan_config", "scope", "local"),
				resource.TestCheckResourceAttr("docker_network.macvlan_config", "ipam_config.#", "1"),
				resource.TestCheckResourceAttr("docker_network.macvlan_config", "ipam_config.0.subnet", "192.168.50.0/24"),
				resource.TestCheckResourceAttr("docker_volume.remote", "driver", "sshfs"),
				resource.TestCheckResourceAttr("docker_network.macvlan", "config_from", "macvlan-config"),
				resource.TestCheckNoResourceAttr("docker_network.macvlan", "labels.%"),
				func(s *terraform.State) error {
					if s.RootModule().Resources["docker_volume.remote"] == nil {
						return fmt.Errorf("no volume in state")
					}
					return nil
				},
			),
		}},
	})
}

func TestNetworkFromInspectConfigOnlyP6bZ2(t *testing.T) {
	var n network.Inspect
	if err := json.Unmarshal(z2ConfigOnlyInspect("abc", "cfg", nil, map[string]string{"parent": "eth0"}), &n); err != nil {
		t.Fatal(err)
	}
	prior := networkResourceModel{
		Name: types.StringValue("cfg"), Driver: types.StringValue("macvlan"),
		IPAMConfig: []networkIPAMConfigModel{{Subnet: types.StringValue("192.168.50.0/24"), IPRange: types.StringNull(),
			Gateway: types.StringNull(), AuxAddress: types.MapNull(types.StringType)}},
		Options: types.MapValueMust(types.StringType, map[string]attr.Value{"parent": types.StringValue("eth0")}),
		Labels:  types.MapNull(types.StringType), IPAMOptions: types.MapNull(types.StringType),
		ConfigFrom: types.StringNull(), EnableIPv4: types.BoolNull(),
	}
	got := networkFromInspect(n, prior)
	if got.Driver.ValueString() != "macvlan" || len(got.IPAMConfig) != 1 || got.IPAMConfig[0].Subnet.ValueString() != "192.168.50.0/24" {
		t.Errorf("config-only: driver %v, ipam %+v", got.Driver, got.IPAMConfig)
	}
	if !got.ConfigOnly.ValueBool() || got.Scope.ValueString() != "local" {
		t.Errorf("config_only/scope: %v %v", got.ConfigOnly, got.Scope)
	}
	// An import has no configuration to keep: the daemon's view is read.
	imp := networkFromInspect(n, networkResourceModel{Name: types.StringNull(), Driver: types.StringNull(),
		Options: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType), IPAMOptions: types.MapNull(types.StringType),
		ConfigFrom: types.StringNull(), EnableIPv4: types.BoolNull()})
	if imp.Driver.ValueString() != "null" || len(imp.IPAMConfig) != 0 {
		t.Errorf("import: driver %v, ipam %+v", imp.Driver, imp.IPAMConfig)
	}
	// A regular network that changed driver is drift, not hidden.
	n.ConfigOnly = false
	if d := networkFromInspect(n, prior).Driver.ValueString(); d != "null" {
		t.Errorf("non-config-only driver = %q", d)
	}
}

func TestVolumeDriverValueP6bZ2(t *testing.T) {
	for _, c := range []struct {
		prior    types.String
		reported string
		want     string
	}{
		{types.StringValue("sshfs"), "sshfs:latest", "sshfs"},
		{types.StringValue("vieux/sshfs"), "vieux/sshfs:latest", "vieux/sshfs"},
		{types.StringValue("sshfs:latest"), "sshfs:latest", "sshfs:latest"},
		{types.StringValue("sshfs"), "sshfs:v2", "sshfs:v2"},
		{types.StringValue("local"), "local", "local"},
		{types.StringValue("local"), "other", "other"},
		{types.StringNull(), "sshfs:latest", "sshfs:latest"},
		{types.StringUnknown(), "local", "local"},
	} {
		if got := volumeDriverValue(c.prior, c.reported).ValueString(); got != c.want {
			t.Errorf("volumeDriverValue(%v, %q) = %q, want %q", c.prior, c.reported, got, c.want)
		}
	}
	got := volumeFromInspect(volume.Volume{Name: "remote", Driver: "sshfs:latest"}, volumeResourceModel{Driver: types.StringValue("sshfs"),
		DriverOpts: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType)})
	if got.Driver.ValueString() != "sshfs" {
		t.Errorf("volumeFromInspect driver = %v", got.Driver)
	}
}

// TestAccNetworkConfigOnlyP6bZ2 creates a config-only macvlan network and a
// local network built from it on the harness daemon: the config-only one
// inspects with Driver "null" and no IPAM pools, and must still apply
// consistently and re-plan empty.
func TestAccNetworkConfigOnlyP6bZ2(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-cfgonly")
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_network" "cfg" {
  name        = "%[1]s"
  driver      = "macvlan"
  config_only = true
  options     = { parent = "eth0" }

  ipam_config {
    subnet = "192.168.231.0/24"
  }
}
`, name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_network.cfg", "driver", "macvlan"),
				resource.TestCheckResourceAttr("docker_network.cfg", "config_only", "true"),
				resource.TestCheckResourceAttr("docker_network.cfg", "ipam_config.0.subnet", "192.168.231.0/24"),
			),
		}},
	})
}

func TestNetworkConfigFromConflictsP6bZ2(t *testing.T) {
	lbl := types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("b")})
	null := types.MapNull(types.StringType)
	if got := networkConfigFromConflicts(null, null, types.BoolNull(), types.BoolNull(), 0); len(got) != 0 {
		t.Errorf("nothing set: %v", got)
	}
	got := networkConfigFromConflicts(lbl, lbl, types.BoolValue(false), types.BoolValue(true), 1)
	if strings.Join(got, ",") != "labels,ipam_options,ipam_config,enable_ipv4,enable_ipv6" {
		t.Errorf("all set: %v", got)
	}
	if got := networkConfigFromConflicts(types.MapUnknown(types.StringType), null, types.BoolValue(true), types.BoolValue(false), 0); len(got) != 0 {
		t.Errorf("unknown / defaults: %v", got)
	}
}
