package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*dockerConfigResource)(nil)

// MoveState moves kreuzwerker/docker docker_config state into docker_config.
func (r *dockerConfigResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveConfig.stateMovers()
}

// kwMoveConfig maps kreuzwerker docker_config (SDKv2 schema version 0).
var kwMoveConfig = kwMove{
	sourceType:    "docker_config",
	schemaVersion: 0,
	rules: concatRules(
		same("id", "name", "data"),
		[]mvRule{
			labelsRule("labels", "labels"),
			// data_raw is the plain-text alternative to data: base64-encode it.
			custom([][2]string{{"data_raw", "data"}}, func(src, dst map[string]any) {
				if _, set := dst["data"]; !set {
					kwSet(dst, "data", tfBase64(src["data_raw"]))
				}
			}),
		},
	),
}
