package provider

// Pass 6c: the structured outputs of these resources changed shape (JSON strings and dynamic values became nested
// attributes, list(string) became maps), and terraform-plugin-framework cannot decode prior state whose stored value
// has the old type. Each schema version was bumped and the upgrader nulls every contract output; the outputs are
// Computed and the next Read refills them. docker_container (ctr_p53.go) carries its own UpgradeState.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// p6cUpgradeOutputs is the shared StateUpgrader body: prior raw state in, every contract output of itemID null.
func p6cUpgradeOutputs(typeName, itemID string, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.RawState == nil {
		resp.Diagnostics.AddError("Cannot upgrade "+typeName+" state", "prior state is missing")
		return
	}
	b, err := contractUpgradeNullOutputs(itemID, req.RawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError("Cannot upgrade "+typeName+" state", err.Error())
		return
	}
	resp.DynamicValue = &tfprotov6.DynamicValue{JSON: b}
}

func p6cOutputsUpgrader(typeName, itemID string) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: func(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
			p6cUpgradeOutputs(typeName, itemID, req, resp)
		}},
	}
}

var (
	_ resource.ResourceWithUpgradeState = (*imageResource)(nil)
	_ resource.ResourceWithUpgradeState = (*tagResource)(nil)
	_ resource.ResourceWithUpgradeState = (*networkResource)(nil)
	_ resource.ResourceWithUpgradeState = (*volumeResource)(nil)
	_ resource.ResourceWithUpgradeState = (*pluginResource)(nil)
	_ resource.ResourceWithUpgradeState = (*serviceResource)(nil)
	_ resource.ResourceWithUpgradeState = (*stackResource)(nil)
	_ resource.ResourceWithUpgradeState = (*registryImageResource)(nil)
)

// UpgradeState upgrades docker_image state from schema version 0 (Pass 6c output shapes).
func (r *imageResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return p6cOutputsUpgrader("docker_image", "engine:image")
}

// UpgradeState upgrades docker_tag state from schema version 0 (Pass 6c output shapes).
func (r *tagResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return p6cOutputsUpgrader("docker_tag", "engine:tag")
}

// UpgradeState upgrades docker_network state from schema version 0 (Pass 6c output shapes).
func (r *networkResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return p6cOutputsUpgrader("docker_network", "engine:network")
}

// UpgradeState upgrades docker_volume state from schema version 0 (Pass 6c output shapes).
func (r *volumeResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return p6cOutputsUpgrader("docker_volume", "engine:volume")
}

// UpgradeState upgrades docker_service state from schema version 0 (Pass 6c output shapes).
func (r *serviceResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return p6cOutputsUpgrader("docker_service", "swarm:service")
}

// UpgradeState upgrades docker_plugin state from schema version 0 (Pass 6c output shapes).
func (r *pluginResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return p6cOutputsUpgrader("docker_plugin", "engine:plugin")
}

// UpgradeState upgrades docker_stack state from schema version 0 (Pass 6c verification: service_status[].spec, a
// JSON string, became the ServiceSpec object).
func (r *stackResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return p6cOutputsUpgrader("docker_stack", "cli-only:stack")
}

// UpgradeState upgrades docker_registry_image state from schema version 0 (Pass 6c verification:
// distribution.descriptor.platform, a dynamic value, became the OCIPlatform object).
func (r *registryImageResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return p6cOutputsUpgrader("docker_registry_image", "engine:registry-image")
}
