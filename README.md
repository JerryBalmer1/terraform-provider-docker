# terraform-provider-docker

One Terraform provider for the whole Docker chain, built on
`terraform-plugin-framework` and the Docker Engine API (`github.com/docker/docker/client`).
No compose files, no `docker` CLI.

| Layer | Resources | Data sources |
|---|---|---|
| 1. Image | `docker_image`, `docker_registry_image` | `docker_image`, `docker_registry_image` |
| 2. Single-host primitives | `docker_network`, `docker_volume`, `docker_container` | `docker_network`, `docker_volume` |
| 3. Swarm topology | `docker_swarm_init`, `docker_swarm_node` | `docker_swarm_tokens`, `docker_node`, `docker_nodes` |
| 4. Swarm workload | `docker_secret`, `docker_config`, `docker_service`, `docker_stack` | |

## Agent skill

A Claude skill generated from the provider schema lives in
[skills/terraform-provider-docker/SKILL.md](skills/terraform-provider-docker/SKILL.md).
It covers the four layers, their dependency order, the attributes that
force replacement, and a worked example that goes from a cold start to a
three-replica service across two nodes. `make skill` regenerates it, and
`make docs` runs `make skill` first.

## Provider configuration

```hcl
provider "docker" {
  host = "tcp://10.0.0.5:2376" # default unix:///var/run/docker.sock, or DOCKER_HOST

  tls {
    ca   = file("~/.docker/ca.pem")
    cert = file("~/.docker/cert.pem")
    key  = file("~/.docker/key.pem")
  }

  registry_auth {
    address     = "ghcr.io"
    config_file = "~/.docker/config.json"
  }
}
```

## Development

```
make build     # provider binary
make test      # unit tests (no daemon)
make testacc   # acceptance tests against DOCKER_HOST
make e2e       # dind manager + 2 workers (test/harness), full end-to-end run
make docs      # skill + tfplugindocs
```

See [BUILD_REPORT.md](BUILD_REPORT.md) for the current build state.
