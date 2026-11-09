package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ resource.ResourceWithMoveState = (*containerResource)(nil)

// MoveState moves kreuzwerker/docker docker_container state into docker_container.
func (r *containerResource) MoveState(context.Context) []resource.StateMover {
	return kwMoveContainer.stateMovers()
}

// kwMoveContainer maps kreuzwerker docker_container (SDKv2 schema version 2).
var kwMoveContainer = kwMove{
	sourceType:    "docker_container",
	schemaVersion: 2,
	rules: concatRules(
		same("id", "name", "image", "command", "entrypoint", "cpu_shares", "gpus", "hostname", "must_run",
			"privileged", "read_only", "restart", "stdin_open", "stop_signal", "stop_timeout", "sysctls", "tty",
			"user", "wait", "working_dir", "dns", "dns_search"),
		[]mvRule{
			cp("capabilities.add", "cap_add"),
			cp("capabilities.drop", "cap_drop"),
			cp("cpus", "nano_cpus", tfCPUsToNano),
			blocks("devices", "devices",
				cp("container_path", "path_in_container"),
				cp("host_path", "path_on_host"),
				cp("permissions", "cgroup_permissions")),
			cp("env", "env", tfKVListToMap),
			blocks("healthcheck", "healthcheck", same("interval", "retries", "start_period", "test", "timeout", "start_interval")...),
			kwHostsRule("host", "extra_hosts", func(host, ip string) string { return host + ":" + ip }),
			labelsRule("labels", "labels"),
			cp("log_driver", "logging.driver"),
			cp("log_opts", "logging.options"),
			cp("max_retry_count", "maximum_retry_count"),
			cp("memory", "memory", tfMBToBytes(false)),
			cp("memory_swap", "memory_swap", tfMBToBytes(true)),
			kwMountsRule("mounts", "mounts"),
			kwVolumesRule(),
			blocksMap("networks_advanced", "networks", "name",
				cp("aliases", "aliases"),
				cp("driver_opts", "driver_opts", tfKVListToMap),
				cp("gw_priority", "gw_priority"),
				cp("ipv4_address", "ipv4_address"),
				cp("ipv6_address", "ipv6_address"),
				cp("link_local_ips", "link_local_ips"),
				cp("mac_address", "mac_address")),
			kwNetworkDataRule(),
			blocks("ports", "ports",
				cp("external", "host_port"),
				cp("internal", "container_port"),
				cp("ip", "host_ip"),
				cp("protocol", "protocol")),
			cp("security_opts", "security_opt"),
			blocks("ulimit", "ulimits", same("hard", "name", "soft")...),
			kwUploadRule(),
			cp("wait_timeout", "wait_timeout", tfSecondsToDuration),

			// Forward mappings for kreuzwerker attributes renames.json lists as
			// gaps ("if added, name it ..."): applied when the schema has them.
			cp("cgroup_parent", "cgroup_parent"),
			cp("cgroupns_mode", "cgroupns_mode"),
			cp("cpu_period", "cpu_period"),
			cp("cpu_quota", "cpu_quota"),
			cp("cpu_set", "cpuset_cpus"),
			cp("device_cgroup_rules", "device_cgroup_rules"),
			blocks("device_read_bps", "blkio_device_read_bps", same("path", "rate")...),
			blocks("device_read_iops", "blkio_device_read_iops", same("path", "rate")...),
			blocks("device_write_bps", "blkio_device_write_bps", same("path", "rate")...),
			blocks("device_write_iops", "blkio_device_write_iops", same("path", "rate")...),
			blocks("device_requests", "device_requests", same("capabilities", "count", "device_ids", "driver", "options")...),
			cp("dns_opts", "dns_options"),
			cp("domainname", "domainname"),
			cp("exit_code", "exit_code"),
			cp("group_add", "group_add"),
			cp("init", "init"),
			cp("ipc_mode", "ipc_mode"),
			cp("memory_reservation", "memory_reservation", tfMBToBytes(false)),
			cp("network_mode", "network_mode"),
			cp("pid_mode", "pid_mode"),
			cp("platform", "platform"),
			cp("publish_all_ports", "publish_all_ports"),
			cp("rm", "auto_remove"),
			cp("runtime", "runtime"),
			cp("shm_size", "shm_size", tfMBToBytes(false)),
			cp("storage_opts", "storage_opt"),
			cp("tmpfs", "tmpfs"),
			cp("userns_mode", "userns_mode"),
			custom([][2]string{{"volumes.from_container", "volumes_from"}}, func(src, dst map[string]any) {
				var out []any
				for _, e := range kwObjects(src["volumes"]) {
					if fc := kwString(e["from_container"]); fc != "" {
						out = append(out, fc)
					}
				}
				if len(out) > 0 {
					dst["volumes_from"] = out
				}
			}),
		},
	),
}

// concatRules flattens rule groups.
func concatRules(groups ...[]mvRule) []mvRule {
	var out []mvRule
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// kwHostsRule maps a set of {host, ip} blocks to a list(string).
func kwHostsRule(from, to string, format func(host, ip string) string) mvRule {
	return custom([][2]string{{from, to}, {from + ".host", to}, {from + ".ip", to}}, func(src, dst map[string]any) {
		var out []any
		for _, e := range kwObjects(kwGet(src, from)) {
			h, ip := kwString(e["host"]), kwString(e["ip"])
			if h != "" && ip != "" {
				out = append(out, format(h, ip))
			}
		}
		if len(out) > 0 {
			kwSet(dst, to, out)
		}
	})
}

// kwMountsRule maps kreuzwerker mounts (container and service share the
// shape; volume_options and bind_options apply when the schema has them).
func kwMountsRule(from, to string) mvRule {
	return blocks(from, to,
		cp("read_only", "read_only"),
		cp("source", "source"),
		cp("target", "target"),
		cp("type", "type"),
		blocks("tmpfs_options", "tmpfs_options", cp("size_bytes", "size_bytes"), cp("mode", "mode")),
		blocks("bind_options", "bind_options", cp("propagation", "propagation")),
		blocks("volume_options", "volume_options",
			cp("driver_name", "driver_config.name"),
			cp("driver_options", "driver_config.options"),
			labelsRule("labels", "labels"),
			cp("no_copy", "no_copy"),
			cp("subpath", "subpath")),
	)
}

// kwVolumesRule appends kreuzwerker volumes blocks to mounts: type bind with a
// host_path, type volume with a volume_name (or an anonymous volume).
// from_container has no mount equivalent and is skipped.
func kwVolumesRule() mvRule {
	return custom([][2]string{
		{"volumes", "mounts"},
		{"volumes.container_path", "mounts.target"},
		{"volumes.host_path", "mounts.source"},
		{"volumes.read_only", "mounts.read_only"},
		{"volumes.selinux_relabel", "mounts.selinux_relabel"},
		{"volumes.volume_name", "mounts.source"},
	}, func(src, dst map[string]any) {
		existing, _ := dst["mounts"].([]any)
		for _, e := range kwObjects(src["volumes"]) {
			target := kwString(e["container_path"])
			if target == "" {
				continue
			}
			m := map[string]any{"target": target}
			if hp := kwString(e["host_path"]); hp != "" {
				m["type"], m["source"] = "bind", hp
			} else {
				m["type"] = "volume"
				if vn := kwString(e["volume_name"]); vn != "" {
					m["source"] = vn
				}
			}
			if ro, ok := e["read_only"].(bool); ok {
				m["read_only"] = ro
			}
			if sl := kwString(e["selinux_relabel"]); sl != "" {
				m["selinux_relabel"] = sl
			}
			existing = append(existing, m)
		}
		if len(existing) > 0 {
			dst["mounts"] = existing
		}
	})
}

// kwNetworkDataRule merges kreuzwerker's computed network_data (keyed by
// network_name) into the networks map entries that networks_advanced created.
// Networks not in networks_advanced are left to the refresh.
func kwNetworkDataRule() mvRule {
	fields := [][2]string{
		{"gateway", "gateway"},
		{"global_ipv6_address", "global_ipv6_address"},
		{"global_ipv6_prefix_length", "global_ipv6_prefix_len"},
		{"ip_address", "ip_address"},
		{"ip_prefix_length", "ip_prefix_len"},
		{"ipv6_gateway", "ipv6_gateway"},
		{"mac_address", "mac_address"},
	}
	pairs := [][2]string{{"network_data", "networks"}, {"network_data.network_name", "networks"}}
	for _, f := range fields {
		pairs = append(pairs, [2]string{"network_data." + f[0], "networks." + f[1]})
	}
	return custom(pairs, func(src, dst map[string]any) {
		nets, _ := dst["networks"].(map[string]any)
		if nets == nil {
			return
		}
		for _, e := range kwObjects(src["network_data"]) {
			n, _ := nets[kwString(e["network_name"])].(map[string]any)
			if n == nil {
				continue
			}
			for _, f := range fields {
				if kwString(n[f[1]]) == "" && e[f[0]] != nil {
					n[f[1]] = e[f[0]]
				}
			}
		}
	})
}

// kwUploadRule maps kreuzwerker upload blocks to files: executable = true
// becomes mode "0744" unless permissions is set.
func kwUploadRule() mvRule {
	sub := []mvRule{
		cp("content", "content"),
		cp("content_base64", "content_base64"),
		cp("file", "path"),
		cp("group", "gid"),
		cp("owner", "uid"),
		cp("permissions", "mode"),
		cp("source", "source"),
		cp("source_hash", "source_hash"),
		custom([][2]string{{"executable", "mode"}}, func(src, dst map[string]any) {
			if ex, _ := src["executable"].(bool); ex && kwString(src["permissions"]) == "" {
				dst["mode"] = "0744"
			}
		}),
	}
	return blocks("upload", "files", sub...)
}
