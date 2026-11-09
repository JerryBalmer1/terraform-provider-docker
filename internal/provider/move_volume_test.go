package provider

import (
	"strings"
	"testing"
)

// kwFixtureVolume is a kreuzwerker/docker v4.6.0 docker_volume state (SDKv2 schema version 1).
const kwFixtureVolume = `{
  "cluster": [],
  "driver": "local",
  "driver_opts": {"type": "tmpfs", "device": "tmpfs"},
  "id": "app_data",
  "labels": [{"label": "com.example.team", "value": "ops"}],
  "mountpoint": "/var/lib/docker/volumes/app_data/_data",
  "name": "app_data"
}`

func TestMoveVolume(t *testing.T) {
	got, typ, resp := kwMustMove(t, NewVolumeResource(), "docker_volume", 1, kwFixtureVolume)
	kwExpect(t, got, typ, "id", "app_data")
	kwExpect(t, got, typ, "name", "app_data")
	kwExpect(t, got, typ, "driver", "local")
	kwExpect(t, got, typ, "driver_opts", map[string]any{"type": "tmpfs", "device": "tmpfs"})
	kwExpect(t, got, typ, "labels", map[string]any{"com.example.team": "ops"})
	kwExpect(t, got, typ, "mountpoint", "/var/lib/docker/volumes/app_data/_data")
	if resp.TargetIdentity == nil {
		t.Fatal("docker_volume has an identity schema: want TargetIdentity")
	}
	var id string
	if d := resp.TargetIdentity.GetAttribute(t.Context(), pathRootID, &id); d.HasError() || id != "app_data" {
		t.Errorf("identity id = %q (%v)", id, d)
	}
}

// kwFixtureVolumeCluster is a kreuzwerker/docker v4.6.0 Swarm cluster (CSI) volume.
const kwFixtureVolumeCluster = `{
  "cluster": [{"availability": "active", "group": "g1", "id": "cv123", "limit_bytes": "2GiB", "required_bytes": "1GiB",
    "scope": "multi", "sharing": "all", "topology_preferred": "zone=a", "topology_required": "region=eu,zone=b", "type": "mount"}],
  "driver": "csi-plugin", "driver_opts": {}, "id": "csi_data", "labels": [], "mountpoint": "", "name": "csi_data"
}`

func TestMoveVolumeCluster(t *testing.T) {
	got, typ, _ := kwMustMove(t, NewVolumeResource(), "docker_volume", 1, kwFixtureVolumeCluster)
	kwExpect(t, got, typ, "cluster_volume_id", "cv123")
	kwExpect(t, got, typ, "cluster_volume_spec.availability", "active")
	kwExpect(t, got, typ, "cluster_volume_spec.group", "g1")
	kwExpect(t, got, typ, "cluster_volume_spec.capacity_range.limit_bytes", 2147483648.0)
	kwExpect(t, got, typ, "cluster_volume_spec.capacity_range.required_bytes", 1073741824.0)
	kwExpect(t, got, typ, "cluster_volume_spec.access_mode.scope", "multi")
	kwExpect(t, got, typ, "cluster_volume_spec.access_mode.sharing", "all")
	kwExpect(t, got, typ, "cluster_volume_spec.access_mode.block_volume", nil)
	kwExpect(t, got, typ, "cluster_volume_spec.accessibility_requirements.preferred", []any{map[string]any{"zone": "a"}})
	kwExpect(t, got, typ, "cluster_volume_spec.accessibility_requirements.requisite", []any{map[string]any{"region": "eu", "zone": "b"}})
	kwExpect(t, got, typ, "cluster_volume_spec.access_mode.mount_volume", nil) // mount is the default

	// type = block (kreuzwerker's default) -> block_volume.
	got, typ, _ = kwMustMove(t, NewVolumeResource(), "docker_volume", 1, strings.Replace(kwFixtureVolumeCluster, `"type": "mount"`, `"type": "block"`, 1))
	kwExpect(t, got, typ, "cluster_volume_spec.access_mode.block_volume", true)

	// No cluster block: no spec.
	got, typ, _ = kwMustMove(t, NewVolumeResource(), "docker_volume", 1, kwFixtureVolume)
	kwExpect(t, got, typ, "cluster_volume_spec", nil)
}
