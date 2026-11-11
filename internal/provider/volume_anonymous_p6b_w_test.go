package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/volume"
)

// TestVolumeFromInspectAnonymousLabel: a volume created without a name gets
// com.docker.volume.anonymous from the Engine; it must not reach the labels
// unless the configuration sets it (Pass 6b Lane W pairwise finding).
func TestVolumeFromInspectAnonymousLabel(t *testing.T) {
	v := volume.Volume{Name: "3f2a", Driver: "local", Labels: map[string]string{volumeAnonymousLabel: "", labelManagedBy: labelManagedByValue}}
	got := volumeFromInspect(v, volumeResourceModel{Labels: types.MapNull(types.StringType), DriverOpts: types.MapNull(types.StringType)})
	if !got.Labels.IsNull() {
		t.Errorf("labels unset in config: got %v, want null", got.Labels)
	}
	v.Labels["pw.a"] = "1"
	got = volumeFromInspect(v, volumeResourceModel{Labels: stringMapValue(map[string]string{"pw.a": "1"}, false), DriverOpts: types.MapNull(types.StringType)})
	if e := got.Labels.Elements(); len(e) != 1 || e["pw.a"] == nil {
		t.Errorf("labels = %v, want only pw.a", got.Labels)
	}
	// Set in the configuration: kept.
	got = volumeFromInspect(v, volumeResourceModel{Labels: stringMapValue(map[string]string{"pw.a": "1", volumeAnonymousLabel: ""}, false), DriverOpts: types.MapNull(types.StringType)})
	if e := got.Labels.Elements(); len(e) != 2 {
		t.Errorf("configured anonymous label dropped: %v", got.Labels)
	}
	if out := volumeStripAnonymous(map[string]string{"a": "1"}, types.MapNull(types.StringType)); len(out) != 1 {
		t.Errorf("no anonymous label: %v", out)
	}
}

// TestAccVolumeP6bW_unnamed: a docker_volume without name (and with or
// without labels) applies and plans empty (before the fix: "Provider produced
// inconsistent result after apply: .labels: new element
// \"com.docker.volume.anonymous\" has appeared").
func TestAccVolumeP6bW_unnamed(t *testing.T) {
	testAccPreCheck(t)
	hAccTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config: `resource "docker_volume" "v" {}`,
				Check:  tfresource.TestCheckNoResourceAttr("docker_volume.v", "labels.%"),
			},
			{
				Config: `
resource "docker_volume" "v" {
  labels = { "pw.a" = "1" }
}
`,
				Check: tfresource.TestCheckResourceAttr("docker_volume.v", "labels.%", "1"),
			},
		},
	})
}
