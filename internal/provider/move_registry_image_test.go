package provider

import "testing"

// kwFixtureRegistryImage is a kreuzwerker/docker v4.6.0 docker_registry_image state (SDKv2 schema version 0).
const kwFixtureRegistryImage = `{
  "auth_config": [],
  "build": [],
  "id": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
  "insecure_skip_verify": false,
  "keep_remotely": true,
  "name": "registry.example.com:5000/team/app:1.2",
  "sha256_digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
  "timeouts": null,
  "triggers": {"image": "sha256:abc"}
}`

func TestMoveRegistryImage(t *testing.T) {
	typ := tObj("id", tS, "name", tS, "insecure_skip_verify", tB, "keep_remotely", tB, "triggers", tMap(tS), "digest", tS, "image_digest_ref", tS)
	got := kwValueGo(t, kwMoveRegistryImage, typ, kwFixtureRegistryImage)
	for k, w := range map[string]any{
		"id":                   "registry.example.com:5000/team/app:1.2",
		"name":                 "registry.example.com:5000/team/app:1.2",
		"insecure_skip_verify": false,
		"keep_remotely":        true,
		"triggers":             map[string]any{"image": "sha256:abc"},
		"digest":               "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		"image_digest_ref":     "registry.example.com:5000/team/app@sha256:2222222222222222222222222222222222222222222222222222222222222222",
	} {
		kwExpect(t, got, typ, k, w)
	}
	cur, ctyp, _ := kwMustMove(t, NewRegistryImageResource(), "docker_registry_image", 0, kwFixtureRegistryImage)
	kwExpect(t, cur, ctyp, "id", "registry.example.com:5000/team/app:1.2")
	kwExpect(t, cur, ctyp, "keep_remotely", true)
	kwExpect(t, cur, ctyp, "digest", "sha256:2222222222222222222222222222222222222222222222222222222222222222")
}
