package provider

// Pass 5: the outputs contract of docker_container (engine:container):
// inspect_json (the exact GET /containers/{id}/json body), inspect (the same
// as a nested object), content_sha256, engine_api_version and
// docker_server_version. The attributes are added by
// contractAddResourceAttributes in Schema; they are Computed without plan
// modifiers, so any planned change shows them as known after apply.

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/moby/moby/api/types/container"
)

// ctrFillContract fills m's contract outputs from the raw inspect body
// (re-encoded from insp when the raw body is missing).
func ctrFillContract(ctx context.Context, pd *providerData, m *containerResourceModel, raw json.RawMessage, insp container.InspectResponse) diag.Diagnostics {
	if len(raw) == 0 {
		b, err := json.Marshal(insp)
		if err != nil {
			var d diag.Diagnostics
			d.AddError("Cannot encode container inspect", err.Error())
			m.contractSetNull()
			return d
		}
		raw = b
	}
	return m.contractFill(ctx, pd, contractFillInput{Raw: map[string][]byte{"inspect_json": raw}})
}
