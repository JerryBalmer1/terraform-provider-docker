package provider

import "testing"

// kwFixtureService is a kreuzwerker/docker v4.6.0 docker_service state (SDKv2 schema version 2).
const kwFixtureService = `{
  "auth": [],
  "converge_config": [{"delay": "7s", "timeout": "3m"}],
  "endpoint_spec": [{"mode": "vip", "ports": [{"name": "http", "protocol": "tcp", "publish_mode": "ingress", "published_port": 8080, "target_port": 80}]}],
  "id": "x8r4k2m1n0p9q7s6t5u4v3w2y",
  "labels": [{"label": "com.example.stack", "value": "web"}],
  "mode": [{"global": false, "replicated": [{"replicas": 3}]}],
  "name": "web",
  "rollback_config": [{"delay": "0s", "failure_action": "pause", "max_failure_ratio": "0.1", "monitor": "5s", "order": "stop-first", "parallelism": 1}],
  "task_spec": [{
    "container_spec": [{
      "args": ["--port", "80"],
      "cap_add": ["NET_ADMIN"],
      "cap_drop": [],
      "command": ["/app"],
      "configs": [{"config_id": "cfg123", "config_name": "app_cfg", "file_gid": "0", "file_mode": 292, "file_name": "/etc/app.conf", "file_uid": "0"}],
      "dir": "/srv",
      "dns_config": [{"nameservers": ["8.8.8.8"], "options": ["timeout:2"], "search": ["example.com"]}],
      "env": {"MODE": "prod"},
      "groups": [],
      "healthcheck": [{"interval": "10s", "retries": 2, "start_period": "0s", "test": ["CMD", "true"], "timeout": "2s"}],
      "hostname": "",
      "hosts": [{"host": "db.internal", "ip": "10.0.0.5"}],
      "image": "nginx:1.27@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "init": false,
      "isolation": "default",
      "labels": [{"label": "role", "value": "frontend"}],
      "mounts": [{"bind_options": [], "read_only": false, "source": "web_data", "target": "/data", "tmpfs_options": [], "type": "volume",
        "volume_options": [{"driver_name": "local", "driver_options": {"o": "bind"}, "labels": [{"label": "backup", "value": "yes"}], "no_copy": true}]}],
      "privileges": [],
      "read_only": true,
      "secrets": [{"file_gid": "0", "file_mode": 256, "file_name": "/run/secrets/pw", "file_uid": "0", "secret_id": "sec123", "secret_name": "pw"}],
      "stop_grace_period": "10s",
      "stop_signal": "SIGTERM",
      "sysctl": {"net.core.somaxconn": "1024"},
      "user": "nginx"
    }],
    "force_update": 2,
    "log_driver": [{"name": "json-file", "options": {"max-size": "10m"}}],
    "networks_advanced": [{"aliases": ["web"], "driver_opts": ["com.example.o=1"], "id": "", "name": "frontend"}, {"aliases": [], "driver_opts": [], "id": "n0ab12", "name": ""}],
    "placement": [{"constraints": ["node.role==worker"], "max_replicas": 2, "platforms": [{"architecture": "amd64", "os": "linux"}], "prefs": ["spread=node.labels.zone"]}],
    "resources": [{"limits": [{"memory_bytes": 268435456, "nano_cpus": 500000000}], "reservation": [{"generic_resources": [], "memory_bytes": 134217728, "nano_cpus": 250000000}]}],
    "restart_policy": [{"condition": "on-failure", "delay": "5s", "max_attempts": 3, "window": "1m"}],
    "runtime": "container"
  }],
  "update_config": [{"delay": "10s", "failure_action": "rollback", "max_failure_ratio": "0.2", "monitor": "5s", "order": "start-first", "parallelism": 2}]
}`

// kwPass5ServiceType is docker_service's target shape after the Pass 5
// renames, limited to the mapped attributes.
var kwPass5ServiceType = tObj(
	"id", tS, "name", tS, "labels", tMap(tS), "image", tS, "args", tList(tS), "command", tList(tS),
	"cap_add", tSet(tS), "cap_drop", tSet(tS), "dir", tS, "env", tMap(tS), "hostname", tS, "hosts", tList(tS),
	"container_labels", tMap(tS), "stop_grace_period", tS, "sysctls", tMap(tS), "user", tS, "force_update", tN,
	"mode", tS, "replicas", tN, "read_only", tB, "stop_signal", tS,
	"converge_config", tObj("delay", tS, "timeout", tS),
	"endpoint_spec", tObj("mode", tS, "ports", tList(tObj("protocol", tS, "publish_mode", tS, "published_port", tN, "target_port", tN))),
	"rollback_config", tObj("delay", tS, "failure_action", tS, "max_failure_ratio", tN, "monitor", tS, "order", tS, "parallelism", tN),
	"update_config", tObj("delay", tS, "failure_action", tS, "max_failure_ratio", tN, "monitor", tS, "order", tS, "parallelism", tN),
	"configs", tList(tObj("config_id", tS, "config_name", tS, "gid", tS, "mode", tN, "file_name", tS, "uid", tS)),
	"secrets", tList(tObj("secret_id", tS, "secret_name", tS, "gid", tS, "mode", tN, "file_name", tS, "uid", tS)),
	"dns_config", tObj("nameservers", tList(tS), "options", tList(tS), "search", tList(tS)),
	"healthcheck", tObj("interval", tS, "retries", tN, "start_period", tS, "test", tList(tS), "timeout", tS),
	"mounts", tList(tObj("read_only", tB, "source", tS, "target", tS, "type", tS, "tmpfs_options", tObj("size_bytes", tN),
		"volume_options", tObj("driver_config", tObj("name", tS, "options", tMap(tS)), "labels", tMap(tS)))),
	"logging", tObj("driver", tS, "options", tMap(tS)),
	"networks", tMap(tObj("aliases", tSet(tS), "driver_opts", tMap(tS))),
	"placement", tObj("constraints", tList(tS), "max_replicas", tN, "preferences", tList(tS)),
	"resources", tObj("limits", tObj("memory_bytes", tN, "nano_cpus", tN), "reservations", tObj("memory_bytes", tN, "nano_cpus", tN)),
	"restart_policy", tObj("condition", tS, "delay", tS, "max_attempts", tN, "window", tS),
)

func TestMoveServicePass5Schema(t *testing.T) {
	got := kwValueGo(t, kwMoveService, kwPass5ServiceType, kwFixtureService)
	want := map[string]any{
		"id": "x8r4k2m1n0p9q7s6t5u4v3w2y", "name": "web",
		"labels":            map[string]any{"com.example.stack": "web"},
		"image":             "nginx:1.27@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"args":              []any{"--port", "80"},
		"command":           []any{"/app"},
		"cap_add":           []any{"NET_ADMIN"},
		"cap_drop":          nil,
		"dir":               "/srv",
		"env":               map[string]any{"MODE": "prod"},
		"hostname":          nil,
		"hosts":             []any{"10.0.0.5 db.internal"},
		"container_labels":  map[string]any{"role": "frontend"},
		"stop_grace_period": "10s",
		"sysctls":           map[string]any{"net.core.somaxconn": "1024"},
		"user":              "nginx",
		"force_update":      2.0,
		"mode":              "replicated",
		"replicas":          3.0,
		"read_only":         true,
		"stop_signal":       "SIGTERM",
		"converge_config":   map[string]any{"delay": "7s", "timeout": "3m"},
		"endpoint_spec": map[string]any{"mode": "vip", "ports": []any{map[string]any{
			"protocol": "tcp", "publish_mode": "ingress", "published_port": 8080.0, "target_port": 80.0}}},
		"rollback_config": map[string]any{"delay": "0s", "failure_action": "pause", "max_failure_ratio": 0.1, "monitor": "5s", "order": "stop-first", "parallelism": 1.0},
		"update_config":   map[string]any{"delay": "10s", "failure_action": "rollback", "max_failure_ratio": 0.2, "monitor": "5s", "order": "start-first", "parallelism": 2.0},
		"configs":         []any{map[string]any{"config_id": "cfg123", "config_name": "app_cfg", "gid": "0", "mode": 292.0, "file_name": "/etc/app.conf", "uid": "0"}},
		"secrets":         []any{map[string]any{"secret_id": "sec123", "secret_name": "pw", "gid": "0", "mode": 256.0, "file_name": "/run/secrets/pw", "uid": "0"}},
		"dns_config":      map[string]any{"nameservers": []any{"8.8.8.8"}, "options": []any{"timeout:2"}, "search": []any{"example.com"}},
		"healthcheck":     map[string]any{"interval": "10s", "retries": 2.0, "start_period": "0s", "test": []any{"CMD", "true"}, "timeout": "2s"},
		"mounts": []any{map[string]any{"read_only": false, "source": "web_data", "target": "/data", "type": "volume", "tmpfs_options": nil,
			"volume_options": map[string]any{"driver_config": map[string]any{"name": "local", "options": map[string]any{"o": "bind"}},
				"labels": map[string]any{"backup": "yes"}}}},
		"logging": map[string]any{"driver": "json-file", "options": map[string]any{"max-size": "10m"}},
		"networks": map[string]any{
			"frontend": map[string]any{"aliases": []any{"web"}, "driver_opts": map[string]any{"com.example.o": "1"}},
			"n0ab12":   map[string]any{"aliases": nil, "driver_opts": nil},
		},
		"placement":      map[string]any{"constraints": []any{"node.role==worker"}, "max_replicas": 2.0, "preferences": []any{"spread=node.labels.zone"}},
		"resources":      map[string]any{"limits": map[string]any{"memory_bytes": 268435456.0, "nano_cpus": 5e8}, "reservations": map[string]any{"memory_bytes": 134217728.0, "nano_cpus": 2.5e8}},
		"restart_policy": map[string]any{"condition": "on-failure", "delay": "5s", "max_attempts": 3.0, "window": "1m"},
	}
	for k, w := range want {
		kwExpect(t, got, kwPass5ServiceType, k, w)
	}
}

// A network keyed only by id (kreuzwerker's newer networks_advanced.id) is
// kept, and so is one with no aliases or driver_opts (all-null value).
func TestMoveServiceNetworkByID(t *testing.T) {
	typ := tObj("id", tS, "networks", tMap(tObj("aliases", tSet(tS), "driver_opts", tMap(tS))))
	got := kwValueGo(t, kwMoveService, typ, `{"id":"s1","task_spec":[{"networks_advanced":[{"aliases":["a"],"id":"n0ab12","name":""},{"aliases":[],"id":"","name":"bare"}]}]}`)
	kwExpect(t, got, typ, "networks", map[string]any{
		"n0ab12": map[string]any{"aliases": []any{"a"}, "driver_opts": nil},
		"bare":   map[string]any{"aliases": nil, "driver_opts": nil},
	})
}

func TestMoveServiceGlobalMode(t *testing.T) {
	got := kwValueGo(t, kwMoveService, kwPass5ServiceType, `{"id":"s1","name":"s","mode":[{"global":true,"replicated":[]}],"task_spec":[{"container_spec":[{"image":"nginx"}]}]}`)
	if got["mode"] != "global" || got["replicas"] != nil {
		t.Errorf("mode = %v, replicas = %v", got["mode"], got["replicas"])
	}
	if got["image"] != "nginx" {
		t.Errorf("image = %v", got["image"])
	}
}

func TestMoveServiceCurrentSchema(t *testing.T) {
	got, typ, resp := kwMustMove(t, NewServiceResource(), "docker_service", 2, kwFixtureService)
	kwExpect(t, got, typ, "id", "x8r4k2m1n0p9q7s6t5u4v3w2y")
	kwExpect(t, got, typ, "name", "web")
	kwExpect(t, got, typ, "image", "nginx:1.27@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	kwExpect(t, got, typ, "mode", "replicated")
	kwExpect(t, got, typ, "replicas", 3.0)
	kwExpect(t, got, typ, "hosts", []any{"10.0.0.5 db.internal"})
	kwExpect(t, got, typ, "container_labels", map[string]any{"role": "frontend"})
	kwExpect(t, got, typ, "logging.driver", "json-file")
	kwExpect(t, got, typ, "resources.reservations.nano_cpus", 2.5e8)
	kwExpect(t, got, typ, "update_config.max_failure_ratio", 0.2)
	kwExpect(t, got, typ, "placement.preferences", []any{"spread=node.labels.zone"})
	kwExpect(t, got, typ, "secrets.0.mode", 256.0)
	if resp.TargetIdentity == nil {
		t.Error("docker_service has an identity schema: want TargetIdentity")
	}
}
