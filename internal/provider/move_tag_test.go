package provider

import "testing"

// kwFixtureTag is a kreuzwerker/docker v4.6.0 docker_tag state (SDKv2 schema version 0).
const kwFixtureTag = `{
  "id": "nginx:1.27.registry.example.com/nginx:prod",
  "source_image": "nginx:1.27",
  "source_image_id": "sha256:3b25b682ea82b2db3cc4fd48db818be788ee3f902ac7378090cf2624ec2442df",
  "tag_triggers": {"v": "1"},
  "target_image": "registry.example.com/nginx:prod"
}`

func TestMoveTag(t *testing.T) {
	got, typ, _ := kwMustMove(t, NewTagResource(), "docker_tag", 0, kwFixtureTag)
	kwExpect(t, got, typ, "id", "sha256:3b25b682ea82b2db3cc4fd48db818be788ee3f902ac7378090cf2624ec2442df.registry.example.com/nginx:prod")
	kwExpect(t, got, typ, "source_image", "nginx:1.27")
	kwExpect(t, got, typ, "source_image_id", "sha256:3b25b682ea82b2db3cc4fd48db818be788ee3f902ac7378090cf2624ec2442df")
	kwExpect(t, got, typ, "target_image", "registry.example.com/nginx:prod")
	kwExpect(t, got, typ, "triggers", map[string]any{"v": "1"})

	// No source_image_id: the kreuzwerker id is kept (the refresh recomputes it).
	got, typ, _ = kwMustMove(t, NewTagResource(), "docker_tag", 0, `{"id":"a.b","source_image":"a","target_image":"b"}`)
	kwExpect(t, got, typ, "id", "a.b")
}
