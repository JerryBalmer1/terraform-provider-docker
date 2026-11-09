package provider

// docker_compose schema version 1 (Pass 5): project_name became name (Q41)
// and working_dir became project_directory (renames.json). The version-0
// state is upgraded by renaming the two keys in its JSON; every attribute
// added in Pass 5 starts null and is filled by the next refresh.

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

var _ resource.ResourceWithUpgradeState = (*composeResource)(nil)

// composeSchemaVersion is docker_compose's current schema version.
const composeSchemaVersion = 1

// composeStateRenames maps version-0 attribute names to version 1.
var composeStateRenames = map[string]string{"project_name": "name", "working_dir": "project_directory"}

// composeUpgradeV0JSON renames the version-0 keys of a raw state object.
func composeUpgradeV0JSON(raw []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for from, to := range composeStateRenames {
		if v, ok := m[from]; ok {
			if _, exists := m[to]; !exists {
				m[to] = v
			}
			delete(m, from)
		}
	}
	return json.Marshal(m)
}

func (r *composeResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
			if req.RawState == nil || req.RawState.JSON == nil {
				resp.Diagnostics.AddError("Cannot upgrade docker_compose state", "the version-0 state has no JSON form")
				return
			}
			b, err := composeUpgradeV0JSON(req.RawState.JSON)
			if err != nil {
				resp.Diagnostics.AddError("Cannot upgrade docker_compose state", err.Error())
				return
			}
			typ := resp.State.Schema.Type().TerraformType(ctx)
			v, err := tftypes.ValueFromJSONWithOpts(b, typ, tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true})
			if err != nil {
				resp.Diagnostics.AddError("Cannot upgrade docker_compose state", err.Error())
				return
			}
			resp.State.Raw = v
		}},
	}
}
