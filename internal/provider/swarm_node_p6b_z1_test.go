package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
)

// TestSwarmNodeRoleFollowsLiveRole (Pass 6b Lane Z): docker_swarm_node.role
// has no default, so an unset role adopts the live role (a docker_node_promote
// action leaves no drift) instead of planning a demote back to worker.
func TestSwarmNodeRoleFollowsLiveRole(t *testing.T) {
	var resp resource.SchemaResponse
	NewSwarmNodeResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	a, ok := resp.Schema.Attributes["role"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("role attribute: %T", resp.Schema.Attributes["role"])
	}
	if !a.Optional || !a.Computed || a.Default != nil {
		t.Errorf("role: optional=%v computed=%v default=%v; want optional, computed, no default", a.Optional, a.Computed, a.Default)
	}
	if len(a.PlanModifiers) == 0 {
		t.Errorf("role: want UseStateForUnknown so updates of other attributes keep the live role")
	}

	// Unset role: the plan value is unknown on create, and nothing is sent.
	var diags = resp.Diagnostics
	ch := swarmNodeChange(context.Background(), swarmNodeModel{
		Availability: types.StringValue("active"), Role: types.StringUnknown(), Labels: types.MapNull(types.StringType),
	}, &diags)
	if ch.Role != nil {
		t.Errorf("unset role must not be applied: %v", *ch.Role)
	}

	// Refresh after an out-of-band promote: state follows the manager.
	m := swarmNodeModel{Role: types.StringValue("worker"), Labels: types.MapNull(types.StringType)}
	swarmNodeFlatten(&m, swarm.Node{ID: "n1", Spec: swarm.NodeSpec{Role: swarm.NodeRoleManager, Availability: swarm.NodeAvailabilityActive}})
	if m.Role.ValueString() != "manager" {
		t.Errorf("role after refresh = %v, want manager", m.Role)
	}
}
