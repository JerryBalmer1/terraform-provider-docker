package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestContractUpgradeNullOutputs: a pre-6c docker_container state (structured
// output stored with the old JSON-string shape) does not decode against the
// new schema, and does after contractUpgradeNullOutputs.
func TestContractUpgradeNullOutputs(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	NewContainerResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx)
	st := map[string]any{}
	for name := range sr.Schema.Attributes {
		st[name] = nil
	}
	st["id"] = "abc"
	st["name"] = "web"
	st["inspect"] = map[string]any{"host_config": map[string]any{"port_bindings": map[string]any{"80/tcp": []any{`{"host_ip":"","host_port":"8080"}`}}}}
	old, _ := json.Marshal(st)
	opts := tfprotov6.UnmarshalOpts{ValueFromJSONOpts: tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true}}
	if _, err := (&tfprotov6.RawState{JSON: old}).UnmarshalWithOpts(typ, opts); err == nil || !strings.Contains(err.Error(), "port_bindings") {
		t.Fatalf("old state must not decode against the new schema: %v", err)
	}
	up, err := contractUpgradeNullOutputs("engine:container", old)
	if err != nil {
		t.Fatal(err)
	}
	v, err := (&tfprotov6.RawState{JSON: up}).UnmarshalWithOpts(typ, opts)
	if err != nil {
		t.Fatalf("upgraded state: %v", err)
	}
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		t.Fatal(err)
	}
	if !m["inspect"].IsNull() || m["id"].IsNull() {
		t.Errorf("inspect must be null and id kept: %v", m["id"])
	}
	if _, err := contractUpgradeNullOutputs("engine:container", []byte("null")); err == nil {
		t.Error("null state must fail")
	}
}
