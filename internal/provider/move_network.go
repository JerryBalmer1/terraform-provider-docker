package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*networkResource)(nil)

// MoveState moves kreuzwerker/docker docker_network state into docker_network.
func (r *networkResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveNetwork.stateMovers()
}

// kwMoveNetwork maps kreuzwerker docker_network (SDKv2 schema version 1).
var kwMoveNetwork = kwMove{
	sourceType:    "docker_network",
	schemaVersion: 1,
	rules: concatRules(
		same("id", "name", "attachable", "driver", "ingress", "internal", "ipam_driver", "options", "scope"),
		[]mvRule{
			blocks("ipam_config", "ipam_config", same("aux_address", "gateway", "ip_range", "subnet")...),
			cp("ipv6", "enable_ipv6"),
			labelsRule("labels", "labels"),
			cp("ipam_options", "ipam_options"), // renames.json gap
		},
	),
}
