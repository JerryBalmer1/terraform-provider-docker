# Pass 6c open findings

Supersedes reference/_gen/pass6b/open-findings.md. Its findings 1-5 are closed in Pass 6c (BUILD_REPORT.md, Pass 6c):
1 `docker_exec.stdin_path`; 2 `docker_service.replicas` null outside replicated mode, TestAccServiceP6cF_globalGenerateConfig;
3 filesystem mirror instead of dev_overrides, Terraform 1.14.9 init verified; 4 plain HTTP to a TLS port and unreachable
SSH hosts covered, the rest in deferred.json kind `diagnostic`; 5 every compose v5.1.4 citation now points at v5.7.0.
What follows was found in Pass 6c and is not closed.

## 1. Docker Build Cloud is not verified against the live service

`docker_buildx_builder` with `driver = "cloud"` is tested only against a fake auth server and a fake BuildKit
(TestBuildxBuilderCloud, TestBKCloud*). Three behaviours differ from buildx v0.38.2 by assumption: the provider dials the
builder group at the default proxy (buildx looks up per-platform instances through the Hub API), sends
`buildx-version: v0.38.2`, and loads results through the build session (buildx's "cloud pull" is not implemented). The
inventory item carries `PARTIAL(pass5) ... not verified against the live service`. Reproduce (needs a Build Cloud
subscription):

```hcl
provider "docker" {
  registry_auth {
    address  = "docker.io"
    username = var.hub_user
    password = var.hub_pat
  }
}
resource "docker_buildx_builder" "cloud" {
  name     = "cloud-builder"
  driver   = "cloud"
  endpoint = "<org>/<builder>"
}
```

`TF_ACC=1 TF_ACC_BUILD_CLOUD=1 DOCKER_HUB_USER=... DOCKER_HUB_PAT=... go test -run TestAccBuildxBuilderCloud_live ./internal/provider`.

## 2. `docker_compose` pre_start with `per_replica` differs from compose v5.7.0

compose v5.7.0 (pkg/compose/pre_start.go:115-147) rejects `per_replica: true` and keeps a failed step container; the
provider (compose_hooks.go) runs per-replica steps and always removes step containers. Reproduce:

```yaml
services:
  web:
    image: nginx:alpine
    deploy: { replicas: 2 }
    pre_start:
      - command: ["sh", "-c", "exit 0"]
        per_replica: true
```

`docker compose up` fails; `docker_compose` with this `compose_yaml` applies.

## 3. `use_api_socket` config-hash parity with the compose CLI

compose v5.7.0 adds DOCKER_CONFIG and the config file only when the host has credentials and the service does not set
DOCKER_CONFIG (mode 0600); compose_local_k.go does not make that distinction, so the `com.docker.compose.config-hash`
label differs from the CLI's in those two cases (a container created by one is recreated by the other). Reproduce: a
service with `use_api_socket: true` and `environment: { DOCKER_CONFIG: /cfg }`, `docker compose up -d`, then apply the
same project with `docker_compose`: the plan recreates the container.

## 4. Structured output round trip on the harness covers resources and the two image data sources only

TestAccP6cSContainerRoundTrip / TestAccP6cSService compare raw and structured outputs leaf by leaf for container, image,
network, volume and service; TestAccP6cImageDataRoundTrip (Pass 6c verification) does the same for data.docker_image
(`inspect` and `config` against `inspect_json`) and data.docker_registry_image (`inspect`). The other data sources and
the list resources are covered at unit level only: TestContractSwaggerShapes / TestContractSwaggerRoundTrip now
enumerate every structured and result output of every non-skipped inventory item (181: 43 swagger-full, 27
swagger-subset, 10 compose-go, 101 fragment), but a unit round trip feeds a synthesized body, not what the provider
code builds at run time, and outputs with no machine-readable upstream schema (buildx, Dockerfile, CLI-shaped compose
and stack outputs, most action results) are checked against their own fragment tree only. data.docker_registry_image
`config` is never filled (always null). Reproduce the gap: no test applies `data "docker_network" "x" { name =
"bridge" }` on the harness and compares `inspect_json` with `inspect`.

## 5. A compose project with `build.ssh` `id: path` loses its whole structured `project`

compose-go v2.16.2 `SSHKey.MarshalJSON` (types/ssh.go:49-54) writes `"id": path` for a key with a path, which is not
valid JSON as a list element, so `json.Marshal(types.Project)` fails and `composeProjectValue` falls back to
`{"name": ...}`: every other field of `project` is null (no "Outputs contract value dropped" is raised, because the
value never reaches the contract). Reproduce:

```hcl
data "docker_compose_config" "x" {
  content = <<-YAML
    services:
      app:
        build:
          context: .
          ssh:
            default: /home/me/.ssh/id_ed25519
  YAML
}
output "svc" { value = data.docker_compose_config.x.project.services }
```

`svc` is null. Fix: marshal SSHKey entries in a fixup (as `{"id", "path"}` or `"id=path"`) before encoding, and make a
failed encode a diagnostic instead of a silent fallback.
