package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*volumeResource)(nil)

// MoveState moves kreuzwerker/docker docker_volume state into docker_volume.
func (r *volumeResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveVolume.stateMovers()
}

// kwMoveVolume maps kreuzwerker docker_volume (SDKv2 schema version 1).
var kwMoveVolume = kwMove{
	sourceType:    "docker_volume",
	schemaVersion: 1,
	rules: concatRules(
		same("id", "name", "driver", "driver_opts", "mountpoint"),
		[]mvRule{labelsRule("labels", "labels"), kwVolumeClusterRule()},
	),
}

// kwVolumeClusterRule maps the kreuzwerker `cluster` max-1 block to
// cluster_volume_spec (nested the Engine way) and cluster_volume_id.
// Sizes are human-readable strings there ("2GiB") and bytes here; a topology
// is a "k=v,k=v" string there and a list of segment maps here.
func kwVolumeClusterRule() mvRule {
	pairs := [][2]string{
		{"cluster", "cluster_volume_spec"},
		{"cluster.availability", "cluster_volume_spec.availability"},
		{"cluster.group", "cluster_volume_spec.group"},
		{"cluster.id", "cluster_volume_id"},
		{"cluster.limit_bytes", "cluster_volume_spec.capacity_range.limit_bytes"},
		{"cluster.required_bytes", "cluster_volume_spec.capacity_range.required_bytes"},
		{"cluster.scope", "cluster_volume_spec.access_mode.scope"},
		{"cluster.sharing", "cluster_volume_spec.access_mode.sharing"},
		{"cluster.topology_preferred", "cluster_volume_spec.accessibility_requirements.preferred"},
		{"cluster.topology_required", "cluster_volume_spec.accessibility_requirements.requisite"},
		{"cluster.type", "cluster_volume_spec.access_mode.mount_volume"},
		{"cluster.type", "cluster_volume_spec.access_mode.block_volume"},
	}
	topology := func(v any) any {
		s := strings.TrimSpace(kwString(v))
		if s == "" {
			return nil
		}
		seg := map[string]any{}
		for _, kv := range strings.Split(s, ",") {
			k, val, _ := strings.Cut(kv, "=")
			seg[k] = val
		}
		return []any{seg}
	}
	return custom(pairs, func(src, dst map[string]any) {
		c, _ := kwFirst(src["cluster"]).(map[string]any)
		if c == nil {
			return
		}
		const spec = "cluster_volume_spec"
		kwSet(dst, "cluster_volume_id", nonEmpty(kwString(c["id"])))
		kwSet(dst, spec+".availability", nonEmpty(kwString(c["availability"])))
		kwSet(dst, spec+".group", nonEmpty(kwString(c["group"])))
		for _, b := range []string{"limit_bytes", "required_bytes"} {
			if v := c[b]; !kwEmpty(v) {
				kwSet(dst, spec+".capacity_range."+b, tfMemoryString(v))
			}
		}
		kwSet(dst, spec+".access_mode.scope", nonEmpty(kwString(c["scope"])))
		kwSet(dst, spec+".access_mode.sharing", nonEmpty(kwString(c["sharing"])))
		// "mount" is the default here (no mount_volume options = MountVolume{});
		// kreuzwerker's default is "block".
		if t := kwString(c["type"]); t == "block" || t == "" {
			kwSet(dst, spec+".access_mode.block_volume", true)
		}
		kwSet(dst, spec+".accessibility_requirements.preferred", topology(c["topology_preferred"]))
		kwSet(dst, spec+".accessibility_requirements.requisite", topology(c["topology_required"]))
		if _, ok := dst[spec]; !ok {
			dst[spec] = map[string]any{}
		}
	})
}
