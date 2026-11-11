package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// TestStateUpgradeP6cOutputs: every resource whose structured outputs changed shape in Pass 6c has an upgrader for
// its previous schema version that keeps the arguments and nulls every contract output (old-typed values included).
func TestStateUpgradeP6cOutputs(t *testing.T) {
	cases := []struct {
		r      resource.ResourceWithUpgradeState
		itemID string
		from   int64
	}{
		{&imageResource{}, "engine:image", 0},
		{&tagResource{}, "engine:tag", 0},
		{&networkResource{}, "engine:network", 0},
		{&volumeResource{}, "engine:volume", 0},
		{&pluginResource{}, "engine:plugin", 0},
		{&containerResource{}, "engine:container", 1},
		{&serviceResource{}, "swarm:service", 0},
		{&stackResource{}, "cli-only:stack", 0},
		{&registryImageResource{}, "engine:registry-image", 0},
	}
	for _, c := range cases {
		t.Run(c.itemID, func(t *testing.T) {
			ctx := context.Background()
			var sr resource.SchemaResponse
			c.r.Schema(ctx, resource.SchemaRequest{}, &sr)
			if sr.Schema.Version != c.from+1 {
				t.Fatalf("schema version %d, want %d", sr.Schema.Version, c.from+1)
			}
			up, ok := c.r.UpgradeState(ctx)[c.from]
			if !ok {
				t.Fatalf("no upgrader for version %d", c.from)
			}
			prior := map[string]any{"id": "abc", "name": "keep"}
			outs := contractModelOutputs(contractMustItem(c.itemID))
			if len(outs) == 0 {
				t.Fatal("item has no contract outputs")
			}
			for _, o := range outs {
				prior[o.Name] = "{\"old\":\"json string\"}"
			}
			raw, _ := json.Marshal(prior)
			resp := &resource.UpgradeStateResponse{}
			up.StateUpgrader(ctx, resource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: raw}}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("upgrade: %v", resp.Diagnostics)
			}
			var got map[string]any
			if err := json.Unmarshal(resp.DynamicValue.JSON, &got); err != nil {
				t.Fatal(err)
			}
			if got["id"] != "abc" || got["name"] != "keep" {
				t.Errorf("arguments not kept: %v", got)
			}
			for _, o := range outs {
				if got[o.Name] != nil {
					t.Errorf("%s = %v, want null", o.Name, got[o.Name])
				}
			}
		})
	}
}
