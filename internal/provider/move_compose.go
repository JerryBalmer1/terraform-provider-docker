package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*composeResource)(nil)

// MoveState moves kreuzwerker/docker docker_compose state into docker_compose.
func (r *composeResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveCompose.stateMovers()
}

// kwMoveCompose maps kreuzwerker docker_compose (SDKv2 schema version 0).
var kwMoveCompose = kwMove{
	sourceType:    "docker_compose",
	schemaVersion: 0,
	rules: concatRules(
		same("profiles", "wait", "wait_timeout", "project_directory"),
		[]mvRule{
			cp("config_paths", "files"),
			cp("env_files", "env_file"),
			cp("project_name", "name"),
			note([2]string{"id", "id"}),            // finish
			cp("remove_orphans", "remove_orphans"), // renames.json gap
		},
	),
	// kreuzwerker id = the project name; so is ours (id = name).
	finish: func(src, dst map[string]any) {
		name := kwString(src["project_name"])
		if name == "" {
			name = kwString(src["id"])
		}
		kwSet(dst, "id", nonEmpty(name))
		if _, set := dst["name"]; !set {
			kwSet(dst, "name", nonEmpty(name))
		}
	},
}
