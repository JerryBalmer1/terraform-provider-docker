package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*pluginResource)(nil)

// MoveState moves kreuzwerker/docker docker_plugin state into docker_plugin.
func (r *pluginResource) MoveState(context.Context) []resource.StateMover {
	return kwMovePlugin.stateMovers()
}

// kwMovePlugin maps kreuzwerker docker_plugin (SDKv2 schema version 0).
var kwMovePlugin = kwMove{
	sourceType:    "docker_plugin",
	schemaVersion: 0,
	rules: concatRules(
		same("id", "name", "alias", "enable_timeout", "enabled", "env", "force_destroy", "force_disable",
			"grant_all_permissions", "plugin_reference"),
		[]mvRule{blocks("grant_permissions", "grant_permissions", same("name", "value")...)},
	),
}
