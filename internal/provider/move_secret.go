package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*secretResource)(nil)

// MoveState moves kreuzwerker/docker docker_secret state into docker_secret.
func (r *secretResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveSecret.stateMovers()
}

// kwMoveSecret maps kreuzwerker docker_secret (SDKv2 schema version 1).
var kwMoveSecret = kwMove{
	sourceType:    "docker_secret",
	schemaVersion: 1,
	rules: concatRules(
		same("id", "name", "data"),
		[]mvRule{labelsRule("labels", "labels")},
	),
}
