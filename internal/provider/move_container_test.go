package provider

import (
	"testing"
)

// kwFixtureContainer is a kreuzwerker/docker v4.6.0 docker_container state
// (SDKv2 schema version 2): every attribute present, blocks as lists, unset
// values as SDKv2 writes them.
const kwFixtureContainer = `{
  "attach": false,
  "bridge": "",
  "capabilities": [{"add": ["NET_ADMIN"], "drop": ["MKNOD"]}],
  "cgroup_parent": "",
  "cgroupns_mode": null,
  "command": ["nginx", "-g", "daemon off;"],
  "container_logs": null,
  "container_read_refresh_timeout_milliseconds": 15000,
  "cpu_period": null,
  "cpu_quota": null,
  "cpu_set": "",
  "cpu_shares": 512,
  "cpus": "1.5",
  "destroy_grace_seconds": null,
  "device_cgroup_rules": [],
  "device_read_bps": [],
  "device_read_iops": [],
  "device_requests": [],
  "device_write_bps": [],
  "device_write_iops": [],
  "devices": [{"container_path": "/dev/fuse", "host_path": "/dev/fuse", "permissions": "rwm"}],
  "dns": ["1.1.1.1"],
  "dns_opts": [],
  "dns_search": ["example.com"],
  "domainname": "",
  "entrypoint": ["/docker-entrypoint.sh"],
  "env": ["APP_ENV=prod", "EMPTY=", "URL=http://x?a=b"],
  "exit_code": null,
  "gpus": null,
  "group_add": [],
  "healthcheck": [{"interval": "30s", "retries": 3, "start_interval": "0s", "start_period": "5s", "test": ["CMD", "curl", "-f", "http://localhost/"], "timeout": "10s"}],
  "host": [{"host": "db.internal", "ip": "10.0.0.5"}],
  "hostname": "web",
  "id": "4b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c",
  "image": "sha256:3b25b682ea82b2db3cc4fd48db818be788ee3f902ac7378090cf2624ec2442df",
  "init": false,
  "ipc_mode": "private",
  "labels": [{"label": "app", "value": "web"}, {"label": "tier", "value": "front"}],
  "log_driver": "json-file",
  "log_opts": {"max-size": "10m"},
  "logs": false,
  "max_retry_count": 0,
  "memory": 512,
  "memory_reservation": 0,
  "memory_swap": -1,
  "mounts": [{"bind_options": [], "read_only": false, "source": "", "target": "/cache", "tmpfs_options": [{"mode": 0, "size_bytes": 67108864}], "type": "tmpfs", "volume_options": []}],
  "must_run": true,
  "name": "web",
  "network_data": [{"gateway": "172.18.0.1", "global_ipv6_address": "", "global_ipv6_prefix_length": 0, "ip_address": "172.18.0.2", "ip_prefix_length": 16, "ipv6_gateway": "", "mac_address": "02:42:ac:12:00:02", "network_name": "app_net"}],
  "network_mode": "bridge",
  "networks_advanced": [{"aliases": ["web", "www"], "driver_opts": ["com.example.opt=1"], "gw_priority": 0, "ipv4_address": "", "ipv6_address": "", "link_local_ips": [], "mac_address": "", "name": "app_net"}],
  "pid_mode": "",
  "platform": null,
  "ports": [{"external": 8080, "internal": 80, "ip": "0.0.0.0", "protocol": "tcp"}],
  "privileged": false,
  "publish_all_ports": false,
  "read_only": false,
  "remove_volumes": true,
  "restart": "unless-stopped",
  "rm": false,
  "runtime": "runc",
  "security_opts": ["no-new-privileges"],
  "shm_size": 64,
  "start": true,
  "stdin_open": false,
  "stop_signal": "",
  "stop_timeout": 0,
  "storage_opts": null,
  "sysctls": {"net.ipv4.ip_forward": "1"},
  "timeouts": null,
  "tmpfs": null,
  "tty": false,
  "ulimit": [{"hard": 65536, "name": "nofile", "soft": 65536}],
  "upload": [
    {"content": "server {}", "content_base64": "", "executable": false, "file": "/etc/nginx/conf.d/default.conf", "group": "", "owner": "", "permissions": "0644", "source": "", "source_hash": ""},
    {"content": "#!/bin/sh\n", "content_base64": "", "executable": true, "file": "/run.sh", "group": "1000", "owner": "1000", "permissions": "", "source": "", "source_hash": ""}
  ],
  "user": "",
  "userns_mode": "",
  "volumes": [
    {"container_path": "/usr/share/nginx/html", "from_container": "", "host_path": "/srv/html", "read_only": true, "selinux_relabel": "", "volume_name": ""},
    {"container_path": "/data", "from_container": "", "host_path": "", "read_only": null, "selinux_relabel": "", "volume_name": "web_data"},
    {"container_path": "", "from_container": "other", "host_path": "", "read_only": null, "selinux_relabel": "", "volume_name": ""}
  ],
  "wait": false,
  "wait_timeout": 60,
  "working_dir": "/"
}`

// kwPass5ContainerType is docker_container's target shape after the Pass 5
// renames (reference/_gen/renames.json), limited to the mapped attributes.
var kwPass5ContainerType = tObj(
	"id", tS, "name", tS, "image", tS, "command", tList(tS), "entrypoint", tList(tS),
	"cap_add", tSet(tS), "cap_drop", tSet(tS), "cpu_shares", tN, "nano_cpus", tN,
	"devices", tList(tObj("path_in_container", tS, "path_on_host", tS, "cgroup_permissions", tS)),
	"dns", tList(tS), "dns_search", tList(tS), "env", tMap(tS), "gpus", tS,
	"healthcheck", tObj("interval", tS, "retries", tN, "start_period", tS, "test", tList(tS), "timeout", tS),
	"extra_hosts", tList(tS), "hostname", tS, "labels", tMap(tS),
	"logging", tObj("driver", tS, "options", tMap(tS)),
	"maximum_retry_count", tN, "memory", tN, "memory_swap", tN, "shm_size", tN,
	"mounts", tList(tObj("read_only", tB, "source", tS, "target", tS, "type", tS, "selinux_relabel", tS,
		"tmpfs_options", tObj("size_bytes", tN))),
	"must_run", tB,
	"networks", tMap(tObj("aliases", tSet(tS), "driver_opts", tMap(tS), "gw_priority", tN, "ipv4_address", tS,
		"ipv6_address", tS, "link_local_ips", tSet(tS), "mac_address", tS, "gateway", tS, "ip_address", tS,
		"ip_prefix_len", tN, "global_ipv6_address", tS, "global_ipv6_prefix_len", tN, "ipv6_gateway", tS)),
	"ports", tList(tObj("host_port", tN, "container_port", tN, "host_ip", tS, "protocol", tS)),
	"privileged", tB, "read_only", tB, "restart", tS, "security_opt", tSet(tS), "stdin_open", tB,
	"stop_signal", tS, "stop_timeout", tN, "sysctls", tMap(tS), "tty", tB,
	"ulimits", tList(tObj("hard", tN, "name", tS, "soft", tN)),
	"files", tList(tObj("content", tS, "content_base64", tS, "mode", tS, "path", tS, "gid", tN, "uid", tN,
		"source", tS, "source_hash", tS)),
	"user", tS, "wait", tB, "wait_timeout", tS, "working_dir", tS, "ipc_mode", tS, "runtime", tS,
)

func TestMoveContainerPass5Schema(t *testing.T) {
	got := kwValueGo(t, kwMoveContainer, kwPass5ContainerType, kwFixtureContainer)
	want := map[string]any{
		"id":         "4b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c",
		"name":       "web",
		"image":      "sha256:3b25b682ea82b2db3cc4fd48db818be788ee3f902ac7378090cf2624ec2442df",
		"command":    []any{"nginx", "-g", "daemon off;"},
		"entrypoint": []any{"/docker-entrypoint.sh"},
		"cap_add":    []any{"NET_ADMIN"},
		"cap_drop":   []any{"MKNOD"},
		"cpu_shares": 512.0,
		"nano_cpus":  1.5e9,
		"devices":    []any{map[string]any{"path_in_container": "/dev/fuse", "path_on_host": "/dev/fuse", "cgroup_permissions": "rwm"}},
		"dns":        []any{"1.1.1.1"},
		"dns_search": []any{"example.com"},
		"env":        map[string]any{"APP_ENV": "prod", "URL": "http://x?a=b"},
		"gpus":       nil,
		"healthcheck": map[string]any{"interval": "30s", "retries": 3.0, "start_period": "5s",
			"test": []any{"CMD", "curl", "-f", "http://localhost/"}, "timeout": "10s"},
		"extra_hosts":         []any{"db.internal:10.0.0.5"},
		"hostname":            "web",
		"labels":              map[string]any{"app": "web", "tier": "front"},
		"logging":             map[string]any{"driver": "json-file", "options": map[string]any{"max-size": "10m"}},
		"maximum_retry_count": 0.0,
		"memory":              536870912.0,
		"memory_swap":         -1.0,
		"shm_size":            67108864.0,
		"mounts": []any{
			map[string]any{"read_only": false, "source": nil, "target": "/cache", "type": "tmpfs", "selinux_relabel": nil,
				"tmpfs_options": map[string]any{"size_bytes": 67108864.0}},
			map[string]any{"read_only": true, "source": "/srv/html", "target": "/usr/share/nginx/html", "type": "bind", "selinux_relabel": nil, "tmpfs_options": nil},
			map[string]any{"read_only": nil, "source": "web_data", "target": "/data", "type": "volume", "selinux_relabel": nil, "tmpfs_options": nil},
		},
		"must_run": true,
		"networks": map[string]any{"app_net": map[string]any{
			"aliases": []any{"web", "www"}, "driver_opts": map[string]any{"com.example.opt": "1"}, "gw_priority": 0.0,
			"ipv4_address": nil, "ipv6_address": nil, "link_local_ips": nil, "mac_address": "02:42:ac:12:00:02",
			"gateway": "172.18.0.1", "ip_address": "172.18.0.2", "ip_prefix_len": 16.0,
			"global_ipv6_address": nil, "global_ipv6_prefix_len": 0.0, "ipv6_gateway": nil,
		}},
		"ports":        []any{map[string]any{"host_port": 8080.0, "container_port": 80.0, "host_ip": "0.0.0.0", "protocol": "tcp"}},
		"privileged":   false,
		"read_only":    false,
		"restart":      "unless-stopped",
		"security_opt": []any{"no-new-privileges"},
		"stdin_open":   false,
		"stop_signal":  nil,
		"stop_timeout": 0.0,
		"sysctls":      map[string]any{"net.ipv4.ip_forward": "1"},
		"tty":          false,
		"ulimits":      []any{map[string]any{"hard": 65536.0, "name": "nofile", "soft": 65536.0}},
		"files": []any{
			map[string]any{"content": "server {}", "content_base64": nil, "mode": "0644", "path": "/etc/nginx/conf.d/default.conf",
				"gid": nil, "uid": nil, "source": nil, "source_hash": nil},
			map[string]any{"content": "#!/bin/sh\n", "content_base64": nil, "mode": "0744", "path": "/run.sh",
				"gid": 1000.0, "uid": 1000.0, "source": nil, "source_hash": nil},
		},
		"user":         nil,
		"wait":         false,
		"wait_timeout": "60s",
		"working_dir":  "/",
		"ipc_mode":     "private",
		"runtime":      "runc",
	}
	for k, w := range want {
		kwExpect(t, got, kwPass5ContainerType, k, w)
	}
	// env EMPTY= keeps an empty value only if the map element is not null:
	// empty strings become null, so the key is dropped from the map.
	if env, _ := got["env"].(map[string]any); len(env) != 2 {
		t.Errorf("env = %v", env)
	}
}

func TestMoveContainerCurrentSchema(t *testing.T) {
	got, typ, resp := kwMustMove(t, NewContainerResource(), "docker_container", 2, kwFixtureContainer)
	kwExpect(t, got, typ, "id", "4b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c")
	kwExpect(t, got, typ, "name", "web")
	kwExpect(t, got, typ, "nano_cpus", 1.5e9)
	kwExpect(t, got, typ, "memory", 536870912.0)
	kwExpect(t, got, typ, "env", map[string]any{"APP_ENV": "prod", "URL": "http://x?a=b"})
	kwExpect(t, got, typ, "labels", map[string]any{"app": "web", "tier": "front"})
	kwExpect(t, got, typ, "extra_hosts", []any{"db.internal:10.0.0.5"})
	kwExpect(t, got, typ, "security_opt", []any{"no-new-privileges"})
	kwExpect(t, got, typ, "wait_timeout", "60s")
	kwExpect(t, got, typ, "mounts.0.target", "/cache")
	kwExpect(t, got, typ, "mounts.1.type", "bind")
	kwExpect(t, got, typ, "mounts.2.source", "web_data")
	kwExpect(t, got, typ, "ulimits.0.name", "nofile")
	kwExpect(t, got, typ, "healthcheck.retries", 3.0)
	kwExpect(t, got, typ, "cap_add", []any{"NET_ADMIN"})
	kwExpect(t, got, typ, "logging.driver", "json-file")
	kwExpect(t, got, typ, "networks.app_net.ip_address", "172.18.0.2")
	kwExpect(t, got, typ, "ports.0.container_port", 80.0)
	kwExpect(t, got, typ, "files.1.mode", "0744")
	if resp.TargetIdentity != nil {
		var id string
		if d := resp.TargetIdentity.GetAttribute(t.Context(), pathRootID, &id); d.HasError() || id != got["id"] {
			t.Errorf("identity id = %q (%v), want %v", id, d, got["id"])
		}
	}
}

// Without networks_advanced, network_data has nowhere to go (left to refresh).
func TestMoveContainerNetworkDataOnly(t *testing.T) {
	raw := `{"id":"c1","name":"c","image":"nginx","network_data":[{"network_name":"bridge","ip_address":"172.17.0.2"}],"networks_advanced":[]}`
	got := kwValueGo(t, kwMoveContainer, kwPass5ContainerType, raw)
	if got["networks"] != nil {
		t.Errorf("networks = %v, want null", got["networks"])
	}
}
