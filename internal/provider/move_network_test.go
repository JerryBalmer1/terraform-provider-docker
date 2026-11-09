package provider

import "testing"

// kwFixtureNetwork is a kreuzwerker/docker v4.6.0 docker_network state (SDKv2 schema version 1).
const kwFixtureNetwork = `{
  "attachable": true,
  "check_duplicate": null,
  "driver": "overlay",
  "id": "q1w2e3r4t5y6u7i8o9p0",
  "ingress": false,
  "internal": false,
  "ipam_config": [{"aux_address": {"router": "10.10.0.2"}, "gateway": "10.10.0.1", "ip_range": "10.10.0.0/25", "subnet": "10.10.0.0/24"}],
  "ipam_driver": "default",
  "ipam_options": {},
  "ipv6": true,
  "labels": [{"label": "env", "value": "prod"}],
  "name": "app_net",
  "options": {"encrypted": "true"},
  "scope": "swarm"
}`

func TestMoveNetwork(t *testing.T) {
	typ := tObj("id", tS, "name", tS, "attachable", tB, "driver", tS, "ingress", tB, "internal", tB, "ipam_driver", tS,
		"options", tMap(tS), "scope", tS, "enable_ipv6", tB, "labels", tMap(tS),
		"ipam_config", tList(tObj("aux_address", tMap(tS), "gateway", tS, "ip_range", tS, "subnet", tS)))
	got := kwValueGo(t, kwMoveNetwork, typ, kwFixtureNetwork)
	for k, w := range map[string]any{
		"id": "q1w2e3r4t5y6u7i8o9p0", "name": "app_net", "attachable": true, "driver": "overlay", "ingress": false,
		"internal": false, "ipam_driver": "default", "options": map[string]any{"encrypted": "true"}, "scope": "swarm",
		"enable_ipv6": true, "labels": map[string]any{"env": "prod"},
		"ipam_config": []any{map[string]any{"aux_address": map[string]any{"router": "10.10.0.2"}, "gateway": "10.10.0.1", "ip_range": "10.10.0.0/25", "subnet": "10.10.0.0/24"}},
	} {
		kwExpect(t, got, typ, k, w)
	}
	cur, ctyp, resp := kwMustMove(t, NewNetworkResource(), "docker_network", 1, kwFixtureNetwork)
	kwExpect(t, cur, ctyp, "name", "app_net")
	kwExpect(t, cur, ctyp, "labels", map[string]any{"env": "prod"})
	kwExpect(t, cur, ctyp, "ipam_config.0.subnet", "10.10.0.0/24")
	kwExpect(t, cur, ctyp, "enable_ipv6", true)
	if resp.TargetIdentity == nil {
		t.Error("docker_network has an identity schema: want TargetIdentity")
	}
}
