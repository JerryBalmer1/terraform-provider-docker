package provider

import "testing"

// kwFixtureImage is a kreuzwerker/docker v4.6.0 docker_image state (SDKv2 schema version 0).
const kwFixtureImage = `{
  "build": [{
    "additional_contexts": [], "auth_config": [], "build_args": {"VERSION": "1.2"}, "build_id": "", "build_log_file": "",
    "builder": "", "cache_from": ["app:cache"], "cache_to": [], "cgroup_parent": "", "context": "./app", "cpu_period": 0,
    "cpu_quota": 0, "cpu_set_cpus": "", "cpu_set_mems": "", "cpu_shares": 0, "dockerfile": "Dockerfile", "extra_hosts": [],
    "force_remove": false, "isolation": "", "label": {"old": "1", "both": "label"}, "labels": {"org.opencontainers.image.title": "app", "both": "labels"},
    "memory": 0, "memory_swap": 0, "network_mode": "", "no_cache": true, "platform": "linux/amd64", "provenance": "", "pull_parent": true,
    "remote_context": "", "remove": true, "sbom": "", "secrets": [{"env": "", "id": "npmrc", "src": "/home/u/.npmrc"}],
    "security_opt": [], "session_id": "", "shm_size": 0, "squash": false, "suppress_output": false, "tag": ["app:extra"],
    "target": "runtime", "ulimit": [{"hard": 1024, "name": "nofile", "soft": 1024}], "use_legacy_builder": false, "version": ""
  }],
  "force_remove": false,
  "id": "sha256:9b1fb7d1f1c3e2a4b6d8f0a2c4e6b8d0f2a4c6e8b0d2f4a6c8e0b2d4f6a8c0e2app:1.2",
  "image_id": "sha256:9b1fb7d1f1c3e2a4b6d8f0a2c4e6b8d0f2a4c6e8b0d2f4a6c8e0b2d4f6a8c0e2",
  "keep_locally": true,
  "name": "app:1.2",
  "platform": "",
  "pull_triggers": ["v1"],
  "repo_digest": "app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
  "timeouts": null,
  "triggers": {"src": "abc123"}
}`

var kwPass5ImageType = tObj(
	"id", tS, "name", tS, "force_remove", tB, "keep_locally", tB, "platform", tS, "triggers", tMap(tS), "image_digest_ref", tS,
	"build", tObj("args", tMap(tS), "builder", tS, "cache_from", tList(tS), "context", tS, "dockerfile", tS,
		"labels", tMap(tS), "no_cache", tB, "platform", tS, "pull", tB, "target", tS,
		"secrets", tList(tObj("env", tS, "id", tS, "src", tS)),
		"ulimits", tMap(tObj("hard", tN, "soft", tN))),
)

func TestMoveImagePass5Schema(t *testing.T) {
	got := kwValueGo(t, kwMoveImage, kwPass5ImageType, kwFixtureImage)
	want := map[string]any{
		"id":               "sha256:9b1fb7d1f1c3e2a4b6d8f0a2c4e6b8d0f2a4c6e8b0d2f4a6c8e0b2d4f6a8c0e2",
		"name":             "app:1.2",
		"force_remove":     false,
		"keep_locally":     true,
		"platform":         nil,
		"triggers":         map[string]any{"src": "abc123"},
		"image_digest_ref": "app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"build": map[string]any{
			"args": map[string]any{"VERSION": "1.2"}, "builder": nil, "cache_from": []any{"app:cache"}, "context": "./app",
			"dockerfile": "Dockerfile", "labels": map[string]any{"org.opencontainers.image.title": "app", "both": "labels", "old": "1"},
			"no_cache": true, "platform": "linux/amd64", "pull": true, "target": "runtime",
			"secrets": []any{map[string]any{"env": nil, "id": "npmrc", "src": "/home/u/.npmrc"}},
			"ulimits": map[string]any{"nofile": map[string]any{"hard": 1024.0, "soft": 1024.0}},
		},
	}
	for k, w := range want {
		kwExpect(t, got, kwPass5ImageType, k, w)
	}
}

func TestMoveImageCurrentSchema(t *testing.T) {
	got, typ, resp := kwMustMove(t, NewImageResource(), "docker_image", 0, kwFixtureImage)
	kwExpect(t, got, typ, "id", "sha256:9b1fb7d1f1c3e2a4b6d8f0a2c4e6b8d0f2a4c6e8b0d2f4a6c8e0b2d4f6a8c0e2")
	kwExpect(t, got, typ, "name", "app:1.2")
	kwExpect(t, got, typ, "build.args", map[string]any{"VERSION": "1.2"})
	kwExpect(t, got, typ, "build.pull", true)
	kwExpect(t, got, typ, "build.secrets.0.id", "npmrc")
	kwExpect(t, got, typ, "image_digest_ref", "app@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if resp.TargetIdentity == nil {
		t.Error("docker_image has an identity schema: want TargetIdentity")
	}
}
