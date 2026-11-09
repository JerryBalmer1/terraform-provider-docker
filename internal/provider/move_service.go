package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*serviceResource)(nil)

// MoveState moves kreuzwerker/docker docker_service state into docker_service.
func (r *serviceResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveService.stateMovers()
}

// kreuzwerker nests the container spec in task_spec.container_spec; ours is
// flattened onto the resource.
const (
	kwTS  = "task_spec."
	kwCSP = "task_spec.container_spec."
)

// kwMoveService maps kreuzwerker docker_service (SDKv2 schema version 2).
var kwMoveService = kwMove{
	sourceType:    "docker_service",
	schemaVersion: 2,
	rules: concatRules(
		same("id", "name"),
		[]mvRule{
			labelsRule("labels", "labels"),
			blocks("converge_config", "converge_config", same("delay", "timeout")...),
			blocks("endpoint_spec", "endpoint_spec",
				cp("mode", "mode"),
				blocks("ports", "ports", same("protocol", "publish_mode", "published_port", "target_port", "name")...)),
			kwServiceModeRule(),
			blocks("rollback_config", "rollback_config", same("delay", "failure_action", "max_failure_ratio", "monitor", "order", "parallelism")...),
			blocks("update_config", "update_config", same("delay", "failure_action", "max_failure_ratio", "monitor", "order", "parallelism")...),

			// task_spec.container_spec.* -> top level
			cp(kwCSP+"args", "args"),
			cp(kwCSP+"cap_add", "cap_add"),
			cp(kwCSP+"cap_drop", "cap_drop"),
			cp(kwCSP+"command", "command"),
			blocks(kwCSP+"configs", "configs",
				cp("config_id", "config_id"),
				cp("config_name", "config_name"),
				cp("file_gid", "gid"),
				cp("file_mode", "mode"),
				cp("file_name", "file_name"),
				cp("file_uid", "uid")),
			cp(kwCSP+"dir", "dir"),
			blocks(kwCSP+"dns_config", "dns_config", same("nameservers", "options", "search")...),
			cp(kwCSP+"env", "env"),
			blocks(kwCSP+"healthcheck", "healthcheck", same("interval", "retries", "start_period", "test", "timeout")...),
			cp(kwCSP+"hostname", "hostname"),
			kwHostsRule(kwCSP+"hosts", "hosts", func(host, ip string) string { return ip + " " + host }),
			cp(kwCSP+"image", "image"),
			labelsRule(kwCSP+"labels", "container_labels"),
			kwMountsRule(kwCSP+"mounts", "mounts"),
			blocks(kwCSP+"secrets", "secrets",
				cp("file_gid", "gid"),
				cp("file_mode", "mode"),
				cp("file_name", "file_name"),
				cp("file_uid", "uid"),
				cp("secret_id", "secret_id"),
				cp("secret_name", "secret_name")),
			cp(kwCSP+"stop_grace_period", "stop_grace_period"),
			cp(kwCSP+"sysctl", "sysctls"),
			cp(kwCSP+"user", "user"),

			// task_spec.* -> top level
			cp(kwTS+"force_update", "force_update"),
			blocks(kwTS+"log_driver", "logging", cp("name", "driver"), cp("options", "options")),
			blocksMap(kwTS+"networks_advanced", "networks", "name|id",
				cp("aliases", "aliases"),
				cp("driver_opts", "driver_opts", tfKVListToMap)),
			blocks(kwTS+"placement", "placement",
				cp("constraints", "constraints"),
				cp("max_replicas", "max_replicas"),
				cp("prefs", "preferences"),
				blocks("platforms", "platforms", same("architecture", "os")...)),
			blocks(kwTS+"resources", "resources",
				blocks("limits", "limits", same("memory_bytes", "nano_cpus")...),
				blocks("reservation", "reservations",
					cp("memory_bytes", "memory_bytes"),
					cp("nano_cpus", "nano_cpus"),
					blocks("generic_resources", "generic_resources", // renames.json gap
						cp("discrete_resources_spec", "discrete_resource_spec"),
						cp("named_resources_spec", "named_resource_spec")))),
			blocks(kwTS+"restart_policy", "restart_policy", same("condition", "delay", "max_attempts", "window")...),

			// Forward mappings for renames.json gaps (applied when the schema has them).
			cp(kwCSP+"groups", "groups"),
			cp(kwCSP+"init", "init"),
			cp(kwCSP+"isolation", "isolation"),
			cp(kwCSP+"read_only", "read_only"),
			cp(kwCSP+"stop_signal", "stop_signal"),
			cp(kwTS+"runtime", "runtime"),
			blocks(kwCSP+"privileges", "privileges",
				blocks("credential_spec", "credential_spec", same("file", "registry")...),
				blocks("se_linux_context", "selinux_context", same("disable", "level", "role", "type", "user")...)),
		},
	),
}

// kwServiceModeRule maps mode {global, replicated {replicas}} to mode =
// "global" | "replicated" plus the top-level replicas.
func kwServiceModeRule() mvRule {
	return custom([][2]string{
		{"mode", "mode"},
		{"mode.global", "mode"},
		{"mode.replicated", "mode"},
		{"mode.replicated.replicas", "replicas"},
	}, func(src, dst map[string]any) {
		mode, ok := kwFirst(src["mode"]).(map[string]any)
		if !ok {
			return
		}
		if g, _ := mode["global"].(bool); g {
			dst["mode"] = "global"
			return
		}
		if rep, ok := kwFirst(mode["replicated"]).(map[string]any); ok {
			dst["mode"] = "replicated"
			if r := rep["replicas"]; r != nil {
				dst["replicas"] = r
			}
		}
	})
}
