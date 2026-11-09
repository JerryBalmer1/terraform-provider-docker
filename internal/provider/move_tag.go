package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*tagResource)(nil)

// MoveState moves kreuzwerker/docker docker_tag state into docker_tag.
func (r *tagResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveTag.stateMovers()
}

// kwMoveTag maps kreuzwerker docker_tag (SDKv2 schema version 0).
var kwMoveTag = kwMove{
	sourceType:    "docker_tag",
	schemaVersion: 0,
	rules: concatRules(
		same("source_image", "source_image_id", "target_image"),
		[]mvRule{
			note([2]string{"id", "id"}),    // finish
			cp("tag_triggers", "triggers"), // renames.json gap
		},
	),
	// kreuzwerker id = <source_image>.<target_image>; ours = <source_image_id>.<target_image>.
	finish: func(src, dst map[string]any) {
		sid, target := kwString(src["source_image_id"]), kwString(src["target_image"])
		if sid != "" && target != "" {
			dst["id"] = sid + "." + target
		} else {
			kwSet(dst, "id", src["id"])
		}
	},
}
