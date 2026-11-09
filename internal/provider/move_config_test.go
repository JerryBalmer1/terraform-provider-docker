package provider

import "testing"

// kwFixtureConfig is a kreuzwerker/docker v4.6.0 docker_config state (SDKv2 schema version 0).
const kwFixtureConfig = `{"data": "c2VydmVyIHt9", "data_raw": null, "id": "cfg1d", "labels": [{"label": "app", "value": "web"}], "name": "nginx_conf"}`

func TestMoveConfig(t *testing.T) {
	got, typ, _ := kwMustMove(t, NewConfigResource(), "docker_config", 0, kwFixtureConfig)
	kwExpect(t, got, typ, "id", "cfg1d")
	kwExpect(t, got, typ, "name", "nginx_conf")
	kwExpect(t, got, typ, "data", "c2VydmVyIHt9")
	kwExpect(t, got, typ, "labels", map[string]any{"app": "web"})

	// data_raw (plain text) becomes base64 data.
	got, typ, _ = kwMustMove(t, NewConfigResource(), "docker_config", 0, `{"data": null, "data_raw": "server {}", "id": "c2", "labels": [], "name": "n"}`)
	kwExpect(t, got, typ, "data", "c2VydmVyIHt9")
	kwExpect(t, got, typ, "labels", nil)
}
