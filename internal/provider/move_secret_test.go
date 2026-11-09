package provider

import "testing"

// kwFixtureSecret is a kreuzwerker/docker v4.6.0 docker_secret state (SDKv2 schema version 1).
const kwFixtureSecret = `{"data": "c2VjcmV0", "id": "s3cr3t1d", "labels": [{"label": "owner", "value": "ops"}], "name": "db_password"}`

func TestMoveSecret(t *testing.T) {
	got, typ, resp := kwMustMove(t, NewSecretResource(), "docker_secret", 1, kwFixtureSecret)
	kwExpect(t, got, typ, "id", "s3cr3t1d")
	kwExpect(t, got, typ, "name", "db_password")
	kwExpect(t, got, typ, "data", "c2VjcmV0")
	kwExpect(t, got, typ, "labels", map[string]any{"owner": "ops"})
	if resp.TargetIdentity == nil {
		t.Error("docker_secret has an identity schema: want TargetIdentity")
	}
}
