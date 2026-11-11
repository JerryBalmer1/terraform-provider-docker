package provider

// Pass 5.3 Lane A (D1): kreuzwerker start / attach / must_run map onto
// state; our own version 0 state upgrades to version 1.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func TestMoveContainerState(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"default running", "", "", ctrStateRunning},
		{"start false", `"start": true`, `"start": false`, ctrStateCreated},
		{"attach true", `"attach": false`, `"attach": true`, ctrStateExited},
		{"must_run false is still running", `"must_run": true`, `"must_run": false`, ctrStateRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := kwFixtureContainer
			if c.from != "" {
				if !strings.Contains(raw, c.from) {
					t.Fatalf("fixture lacks %s", c.from)
				}
				raw = strings.Replace(raw, c.from, c.to, 1)
			}
			got, typ, _ := kwMustMove(t, NewContainerResource(), "docker_container", 2, raw)
			kwExpect(t, got, typ, "state", c.want)
			if _, ok := got["must_run"]; ok {
				t.Error("must_run must not be in the moved state")
			}
		})
	}
}

func TestContainerUpgradeStateV0(t *testing.T) {
	ctx := context.Background()
	r := NewContainerResource().(*containerResource)
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	if sr.Schema.Version != 2 { // Pass 6c: 1 -> 2, structured output shapes
		t.Fatalf("schema version = %d, want 2", sr.Schema.Version)
	}
	v0 := `{"id":"c1","name":"web","image":"nginx","must_run":false,"wait_timeout":"60s","running":true,
	  "networks_advanced":[{"name":"x"}],"mounts":[{"type":"tmpfs","target":"/t","tmpfs_size":5,"read_only":false}]}`
	b, err := ctrUpgradeV0(ctx, []byte(v0), sr.Schema.Type().TerraformType(ctx))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["state"] != ctrStateRunning || got["wait_timeout"] != "60s" || got["logs_tail"] != float64(logDefaultTail) {
		t.Errorf("upgraded: %v", got)
	}
	for _, gone := range []string{"must_run", "networks_advanced"} {
		if _, ok := got[gone]; ok {
			t.Errorf("%s survived the upgrade", gone)
		}
	}
	if m := got["mounts"].([]any)[0].(map[string]any); m["tmpfs_size"] != nil || m["target"] != "/t" {
		t.Errorf("nested prune: %v", m)
	}
	ups := r.UpgradeState(ctx)
	up, ok := ups[0]
	if !ok {
		t.Fatal("no upgrader for version 0")
	}
	var resp resource.UpgradeStateResponse
	up.StateUpgrader(ctx, resource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: []byte(v0)}}, &resp)
	if resp.Diagnostics.HasError() || resp.DynamicValue == nil {
		t.Fatalf("upgrader: %v", resp.Diagnostics)
	}
	if _, err := resp.DynamicValue.Unmarshal(sr.Schema.Type().TerraformType(ctx)); err != nil {
		t.Fatalf("upgraded state does not fit the schema: %v", err)
	}
}
