package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*registryImageResource)(nil)

// MoveState moves kreuzwerker/docker docker_registry_image state into docker_registry_image.
func (r *registryImageResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveRegistryImage.stateMovers()
}

// kwMoveRegistryImage maps kreuzwerker docker_registry_image (SDKv2 schema version 0).
var kwMoveRegistryImage = kwMove{
	sourceType:    "docker_registry_image",
	schemaVersion: 0,
	rules: concatRules(
		same("name", "insecure_skip_verify", "keep_remotely", "triggers"),
		[]mvRule{
			cp("sha256_digest", "digest"),
			note([2]string{"id", "id"}), // finish
		},
	),
	// kreuzwerker id = the digest; ours = the pushed reference (name).
	// image_digest_ref = <name without tag>@<digest>.
	finish: func(src, dst map[string]any) {
		name, digest := kwString(src["name"]), kwString(src["sha256_digest"])
		kwSet(dst, "id", nonEmpty(name))
		if name != "" && digest != "" {
			kwSet(dst, "image_digest_ref", kwRepoOf(name)+"@"+digest)
		}
	},
}
