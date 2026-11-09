package provider

import "testing"

// kwFixturePlugin is a kreuzwerker/docker v4.6.0 docker_plugin state (SDKv2 schema version 0).
const kwFixturePlugin = `{
  "alias": "sshfs:latest", "enable_timeout": 60, "enabled": true, "env": ["DEBUG=1"], "force_destroy": true,
  "force_disable": false, "grant_all_permissions": null,
  "grant_permissions": [{"name": "network", "value": ["host"]}, {"name": "capabilities", "value": ["CAP_SYS_ADMIN"]}],
  "id": "a1b2c3d4e5f6", "name": "docker.io/vieux/sshfs:latest", "plugin_reference": "docker.io/vieux/sshfs:latest"
}`

func TestMovePlugin(t *testing.T) {
	got, typ, _ := kwMustMove(t, NewPluginResource(), "docker_plugin", 0, kwFixturePlugin)
	kwExpect(t, got, typ, "id", "a1b2c3d4e5f6")
	kwExpect(t, got, typ, "name", "docker.io/vieux/sshfs:latest")
	kwExpect(t, got, typ, "alias", "sshfs:latest")
	kwExpect(t, got, typ, "enable_timeout", 60.0)
	kwExpect(t, got, typ, "enabled", true)
	kwExpect(t, got, typ, "env", []any{"DEBUG=1"})
	kwExpect(t, got, typ, "force_destroy", true)
	kwExpect(t, got, typ, "grant_all_permissions", nil)
	kwExpect(t, got, typ, "plugin_reference", "docker.io/vieux/sshfs:latest")
	if l, _ := kwGoAt(got, "grant_permissions").([]any); kwTypeHas(typ, "grant_permissions") && len(l) != 2 {
		t.Errorf("grant_permissions = %v", l)
	}
}
