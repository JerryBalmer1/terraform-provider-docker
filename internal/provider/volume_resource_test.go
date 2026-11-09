package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

func TestVolumeFromInspect(t *testing.T) {
	v := volume.Volume{
		Name: "data", Driver: "local", Mountpoint: "/var/lib/docker/volumes/data/_data",
		Options: map[string]string{"type": "tmpfs", "device": "tmpfs"},
		Labels:  map[string]string{"team": "a", labelManagedBy: labelManagedByValue},
	}
	got := volumeFromInspect(v, volumeResourceModel{DriverOpts: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType)})
	if got.ID.ValueString() != "data" || got.Name.ValueString() != "data" || got.Mountpoint.IsNull() {
		t.Errorf("identity: %+v", got)
	}
	if len(got.DriverOpts.Elements()) != 2 || len(got.Labels.Elements()) != 1 {
		t.Errorf("opts/labels: %v %v", got.DriverOpts, got.Labels)
	}
	empty := volumeFromInspect(volume.Volume{Name: "x", Driver: "local"}, volumeResourceModel{DriverOpts: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType)})
	if !empty.DriverOpts.IsNull() || !empty.Labels.IsNull() {
		t.Errorf("unset maps stay null: %+v", empty)
	}
}

// TestVolumeFakeDaemon drives volume inspect/remove and the data source model
// against a fake Engine API.
func TestVolumeFakeDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.47")
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/volumes/data") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(volume.Volume{Name: "data", Driver: "local", Mountpoint: "/m", Scope: "local"})
		case strings.Contains(r.URL.Path, "/volumes/"):
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "no such volume"})
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	defer srv.Close()
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + srv.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name, err := resolveVolumeName(ctx, c, "data")
	if err != nil || name != "data" {
		t.Fatalf("resolve: %q %v", name, err)
	}
	if _, err := c.VolumeInspect(ctx, "gone", client.VolumeInspectOptions{}); !isNotFound(err) {
		t.Errorf("missing volume should be not-found, got %v", err)
	}
	if ok, err := volumeExists(ctx, c, "gone"); ok || err != nil {
		t.Errorf("volumeExists(gone) = %v, %v", ok, err)
	}
	v, _ := c.VolumeInspect(ctx, "data", client.VolumeInspectOptions{})
	ds := volumeDataFromInspect(v.Volume)
	if ds.Mountpoint.ValueString() != "/m" || ds.Scope.ValueString() != "local" || ds.DriverOpts.IsNull() {
		t.Errorf("data source model: %+v", ds)
	}
}

func TestAccVolume_basic(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-vol")
	c := testAccDockerClient(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "docker_volume" {
					continue
				}
				if _, err := c.VolumeInspect(context.Background(), rs.Primary.ID, client.VolumeInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("volume %s still exists (%v)", rs.Primary.ID, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "docker_volume" "test" {
  name        = %q
  driver_opts = { type = "tmpfs", device = "tmpfs", o = "size=10m" }
  labels      = { team = "acc" }
}
`, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_volume.test", "id", name),
					resource.TestCheckResourceAttr("docker_volume.test", "driver", "local"),
					resource.TestCheckResourceAttrSet("docker_volume.test", "mountpoint"),
				),
			},
			{
				ResourceName:      "docker_volume.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}
