package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*buildxBuilderResource)(nil)

// MoveState moves kreuzwerker/docker docker_buildx_builder state into docker_buildx_builder.
func (r *buildxBuilderResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveBuildxBuilder.stateMovers()
}

// kwMoveBuildxBuilder maps kreuzwerker docker_buildx_builder (SDKv2 schema version 0).
var kwMoveBuildxBuilder = kwMove{
	sourceType:    "docker_buildx_builder",
	schemaVersion: 0,
	rules: concatRules(
		same("id", "name", "bootstrap", "driver", "endpoint"),
		[]mvRule{
			cp("buildkit_config", "buildkitd_config"),
			cp("buildkit_flags", "buildkitd_flags"),
			blocks("docker_container", "docker_container",
				cp("env", "env"),
				cp("image", "image"),
				cp("memory", "memory", tfMemoryString),
				cp("network", "network"),
				cp("restart_policy", "restart_policy"),
				// renames.json gaps (applied when the schema has them).
				cp("cgroup_parent", "cgroup_parent"),
				cp("cpu_period", "cpu_period"),
				cp("cpu_quota", "cpu_quota"),
				cp("cpu_shares", "cpu_shares"),
				cp("cpuset_cpus", "cpuset_cpus"),
				cp("cpuset_mems", "cpuset_mems"),
				cp("default_load", "default_load"),
				cp("memory_swap", "memory_swap", tfMemoryString)),
			cp("append", "append"),     // renames.json gap
			cp("node", "node"),         // renames.json gap
			cp("platform", "platform"), // renames.json gap (Pass 5)
		},
	),
}
