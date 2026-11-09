package provider

import "testing"

// kwFixtureCompose is a kreuzwerker/docker v4.6.0 docker_compose state
// (a terraform-plugin-framework resource there, schema version 0).
const kwFixtureCompose = `{
  "config_paths": ["./compose.yaml", "./compose.prod.yaml"],
  "env_files": [".env.prod"],
  "id": "shop",
  "profiles": ["web"],
  "project_directory": "/srv/shop",
  "project_name": "shop",
  "remove_orphans": true,
  "wait": true,
  "wait_timeout": "2m"
}`

func TestMoveCompose(t *testing.T) {
	typ := tObj("id", tS, "name", tS, "files", tList(tS), "env_file", tList(tS), "profiles", tList(tS),
		"project_directory", tS, "wait", tB, "wait_timeout", tS, "remove_orphans", tB)
	got := kwValueGo(t, kwMoveCompose, typ, kwFixtureCompose)
	for k, w := range map[string]any{
		"id": "shop", "name": "shop", "files": []any{"./compose.yaml", "./compose.prod.yaml"}, "env_file": []any{".env.prod"},
		"profiles": []any{"web"}, "project_directory": "/srv/shop", "wait": true, "wait_timeout": "2m", "remove_orphans": true,
	} {
		kwExpect(t, got, typ, k, w)
	}
	// project_name unset: the id (the derived project name) is the name.
	got = kwValueGo(t, kwMoveCompose, typ, `{"config_paths":["c.yml"],"id":"derived","project_name":null}`)
	kwExpect(t, got, typ, "name", "derived")
	kwExpect(t, got, typ, "id", "derived")

	cur, ctyp, _ := kwMustMove(t, NewComposeResource(), "docker_compose", 0, kwFixtureCompose)
	kwExpect(t, cur, ctyp, "id", "shop")
	kwExpect(t, cur, ctyp, "files", []any{"./compose.yaml", "./compose.prod.yaml"})
	kwExpect(t, cur, ctyp, "env_file", []any{".env.prod"})
	kwExpect(t, cur, ctyp, "wait_timeout", "2m")
}
