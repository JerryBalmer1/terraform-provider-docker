package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*imageResource)(nil)

// MoveState moves kreuzwerker/docker docker_image state into docker_image.
func (r *imageResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveImage.stateMovers()
}

// kwMoveImage maps kreuzwerker docker_image (SDKv2 schema version 0).
var kwMoveImage = kwMove{
	sourceType:    "docker_image",
	schemaVersion: 0,
	rules: concatRules(
		same("name", "force_remove", "keep_locally", "platform", "triggers"),
		[]mvRule{
			blocks("build", "build",
				cp("build_args", "args"),
				cp("builder", "builder"),
				cp("cache_from", "cache_from"),
				cp("context", "context"),
				cp("dockerfile", "dockerfile"),
				cp("labels", "labels"),
				// label is the deprecated duplicate of labels: fills the keys labels lacks.
				custom([][2]string{{"label", "labels"}}, func(src, dst map[string]any) {
					l, _ := src["label"].(map[string]any)
					if len(l) == 0 {
						return
					}
					m, _ := dst["labels"].(map[string]any)
					if m == nil {
						m = map[string]any{}
					}
					for k, v := range l {
						if _, set := m[k]; !set {
							m[k] = v
						}
					}
					dst["labels"] = m
				}),
				cp("no_cache", "no_cache"),
				cp("platform", "platform"),
				cp("pull_parent", "pull"),
				blocks("secrets", "secrets", same("env", "id", "src")...),
				cp("target", "target"),
				// renames.json gaps (applied when the schema has them).
				cp("additional_contexts", "additional_contexts"),
				cp("cache_to", "cache_to"),
				cp("cgroup_parent", "cgroup_parent"),
				cp("cpu_period", "cpu_period"),
				cp("cpu_quota", "cpu_quota"),
				cp("cpu_set_cpus", "cpuset_cpus"),
				cp("cpu_set_mems", "cpuset_mems"),
				cp("cpu_shares", "cpu_shares"),
				cp("extra_hosts", "extra_hosts"),
				cp("force_remove", "force_rm"),
				cp("isolation", "isolation"),
				cp("memory", "memory"),
				cp("memory_swap", "memswap"),
				cp("network_mode", "network"),
				cp("provenance", "provenance"),
				cp("remote_context", "remote"),
				cp("sbom", "sbom"),
				cp("shm_size", "shm_size"),
				cp("squash", "squash"),
				cp("tag", "tags"),
				blocksMap("ulimit", "ulimits", "name", same("hard", "soft")...),
			),
			cp("repo_digest", "image_digest_ref"),
			// pull_triggers is config-only (a set where ours is a map): nothing to move.
			note([2]string{"pull_triggers", "triggers"}),
			note([2]string{"id", "id"}, [2]string{"image_id", "id"}), // finish
		},
	),
	// kreuzwerker id = image ID + name; ours = the image ID (kreuzwerker image_id).
	finish: func(src, dst map[string]any) {
		if iid := kwString(src["image_id"]); iid != "" {
			dst["id"] = iid
		}
	},
}
