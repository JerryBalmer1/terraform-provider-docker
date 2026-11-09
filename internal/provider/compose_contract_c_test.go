package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// composeRichYAML touches most compose-spec service attributes so the
// project output's field mapping is exercised end to end.
const composeRichYAML = `
name: rich
services:
  web:
    image: nginx:1.27
    command: ["nginx", "-g", "daemon off;"]
    entrypoint: /docker-entrypoint.sh
    environment: {A: "1", B: two}
    env_file: [./app.env]
    labels: {tier: front}
    annotations: {a: b}
    ports: ["8080:80", {target: 443, published: "8443", protocol: tcp}]
    expose: [9000, "9001"]
    extra_hosts: ["db.local=10.0.0.5", "db.local=10.0.0.6"]
    dns: 1.1.1.1
    tmpfs: /run
    sysctls: {net.core.somaxconn: "1024"}
    ulimits: {nofile: {soft: 1024, hard: 2048}, nproc: 512}
    shm_size: 64m
    healthcheck: {test: ["CMD", "true"], interval: 5s, retries: 3}
    logging: {driver: json-file, options: {max-size: 1m}}
    restart: unless-stopped
    networks: {front: {aliases: [www]}}
    volumes: ["data:/data", "./site:/site:ro"]
    configs: [{source: site, target: /etc/site.conf}]
    secrets: [pw]
    depends_on: {cache: {condition: service_started}}
    deploy: {replicas: 2, resources: {limits: {pids: 100}}}
    build: {context: ., dockerfile: Dockerfile, args: {V: "1"}}
    pull_policy: missing
    stop_grace_period: 5s
    cap_add: [NET_ADMIN]
    security_opt: ["no-new-privileges:true"]
    devices: ["/dev/null:/dev/xnull"]
    blkio_config: {weight: 300}
    gpus: all
    models: [llm]
  cache:
    image: redis:7
networks:
  front: {driver: bridge, ipam: {config: [{subnet: 172.30.0.0/24}]}}
volumes:
  data: {driver_opts: {type: tmpfs, device: tmpfs}}
secrets:
  pw: {environment: PW}
configs:
  site: {content: "hello"}
models:
  llm: {model: ai/smollm2}
jobs:
  migrate:
    image: busybox
    command: ["true"]
    triggers: {manual: true}
`

func composeRichProject(t *testing.T) composeInputs {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte("X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return composeInputs{Content: composeRichYAML, WorkingDir: dir, Env: map[string]string{"PW": "s3cret"}}
}

// TestComposeProjectContractNoDrops fills the project output from a rich
// project and requires that no value is dropped for a shape mismatch.
func TestComposeProjectContractNoDrops(t *testing.T) {
	p, err := seamComposeImpl.Load(context.Background(), seamComposeLoadRequest{
		Content: composeRichYAML, WorkingDir: composeRichProject(t).WorkingDir, Env: map[string]string{"PW": "s3cret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var c contractComposeName
	diags := composeFillContract(context.Background(), nil, &c, p)
	for _, d := range diags {
		t.Errorf("%s: %s", d.Summary(), d.Detail())
	}
	if c.ComposeYAML.IsNull() || !strings.Contains(c.ComposeYAML.ValueString(), "nginx:1.27") {
		t.Fatalf("compose_yaml = %v", c.ComposeYAML)
	}
	if c.ContentSHA256.ValueString() != composeSHA256Hex([]byte(c.ComposeYAML.ValueString())) {
		t.Fatalf("content_sha256 mismatch")
	}
	if c.ComposeGoVersion.ValueString() != contractComposeGoVersion || c.ComposeSpecVersion.ValueString() != contractComposeSpecVersion {
		t.Fatalf("versions %v %v", c.ComposeGoVersion, c.ComposeSpecVersion)
	}
	if !c.EngineAPIVersion.IsNull() {
		t.Fatalf("engine_api_version without a daemon = %v", c.EngineAPIVersion)
	}
	attrs := c.Project.Attributes()
	if attrs["name"].String() != `"rich"` {
		t.Fatalf("project.name = %v", attrs["name"])
	}
	for _, k := range []string{"services", "networks", "volumes", "secrets", "configs", "models", "jobs"} {
		if attrs[k].IsNull() {
			t.Errorf("project.%s is null", k)
		}
	}
}
