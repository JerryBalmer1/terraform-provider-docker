package provider

import "testing"

// kwFixtureBuildxBuilder is a kreuzwerker/docker v4.6.0 docker_buildx_builder state (SDKv2 schema version 0).
const kwFixtureBuildxBuilder = `{
  "append": false, "bootstrap": true, "buildkit_config": "/etc/buildkitd.toml", "buildkit_flags": "--debug",
  "docker_container": [{"cgroup_parent": "", "cpu_period": "", "cpu_quota": "", "cpu_shares": "", "cpuset_cpus": "", "cpuset_mems": "",
    "default_load": true, "env": {"BUILDKIT_STEP_LOG_MAX_SIZE": "10485760"}, "image": "moby/buildkit:v0.18.0", "memory": "2g",
    "memory_swap": "", "network": "host", "restart_policy": "unless-stopped"}],
  "driver": "docker-container", "driver_options": {}, "endpoint": "unix:///var/run/docker.sock", "id": "ci-builder",
  "kubernetes": [], "name": "ci-builder", "node": "", "platform": [], "remote": [], "use": false
}`

func TestMoveBuildxBuilder(t *testing.T) {
	typ := tObj("id", tS, "name", tS, "bootstrap", tB, "driver", tS, "endpoint", tS, "buildkitd_config", tS, "buildkitd_flags", tS,
		"docker_container", tObj("env", tMap(tS), "image", tS, "memory", tN, "network", tS, "restart_policy", tS))
	got := kwValueGo(t, kwMoveBuildxBuilder, typ, kwFixtureBuildxBuilder)
	for k, w := range map[string]any{
		"id": "ci-builder", "name": "ci-builder", "bootstrap": true, "driver": "docker-container", "endpoint": "unix:///var/run/docker.sock",
		"buildkitd_config": "/etc/buildkitd.toml", "buildkitd_flags": "--debug",
		"docker_container": map[string]any{"env": map[string]any{"BUILDKIT_STEP_LOG_MAX_SIZE": "10485760"}, "image": "moby/buildkit:v0.18.0",
			"memory": 2147483648.0, "network": "host", "restart_policy": "unless-stopped"},
	} {
		kwExpect(t, got, typ, k, w)
	}
	cur, ctyp, _ := kwMustMove(t, NewBuildxBuilderResource(), "docker_buildx_builder", 0, kwFixtureBuildxBuilder)
	kwExpect(t, cur, ctyp, "name", "ci-builder")
	kwExpect(t, cur, ctyp, "docker_container.memory", 2147483648.0)
}
