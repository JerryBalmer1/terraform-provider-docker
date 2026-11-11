package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestServiceValidateReplicasInvalidMode: replicas with a non-replicated mode
// is an attribute error carrying the docker CLI's message (Pass 6b Lane Z).
func TestServiceValidateReplicasInvalidMode(t *testing.T) {
	r := &serviceResource{}
	cases := []struct {
		mode     any
		replicas any
		fail     bool
	}{
		{"global", int64(1), true},
		{"global", int64(2), true},
		{"global_job", int64(2), true},
		{"replicated_job", int64(2), true},
		{"replicated", int64(2), false},
		{nil, int64(2), false},
		{"global", nil, false},
		{tftypes.UnknownValue, int64(2), false},
	}
	for _, tc := range cases {
		set := map[string]tftypes.Value{
			"name":  tftypes.NewValue(tftypes.String, "s"),
			"image": tftypes.NewValue(tftypes.String, "busybox"),
		}
		set["mode"] = tftypes.NewValue(tftypes.String, tc.mode)
		if tc.replicas != nil {
			set["replicas"] = tftypes.NewValue(tftypes.Number, tc.replicas)
		}
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: pwConfig(t, r, set)}, &resp)
		if resp.Diagnostics.WarningsCount() > 0 {
			t.Errorf("mode %v replicas %v: unexpected warnings %v", tc.mode, tc.replicas, resp.Diagnostics)
		}
		got := resp.Diagnostics.HasError()
		if got != tc.fail {
			t.Fatalf("mode %v replicas %v: error = %v, want %v (%v)", tc.mode, tc.replicas, got, tc.fail, resp.Diagnostics)
		}
		if !got {
			continue
		}
		d := resp.Diagnostics.Errors()[0]
		if d.Summary() != "replicas is not valid for this mode" || !strings.Contains(d.Detail(), svcReplicasModeCLIError) {
			t.Errorf("mode %v: diag = %q / %q", tc.mode, d.Summary(), d.Detail())
		}
		if tc.mode == "replicated_job" && !strings.Contains(d.Detail(), "total_completions") {
			t.Errorf("replicated_job hint missing: %q", d.Detail())
		}
	}
}
