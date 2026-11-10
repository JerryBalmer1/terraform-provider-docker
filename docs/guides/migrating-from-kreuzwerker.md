---
page_title: "Migrating from kreuzwerker/docker"
subcategory: ""
description: |-
  Move existing kreuzwerker/docker v4.6.0 resources into this provider with moved blocks, without recreating the Docker objects.
---

# Migrating from kreuzwerker/docker

This provider moves the state of every kreuzwerker/docker v4.6.0 managed resource with a `moved` block: the Docker objects stay as they are, Terraform hands the kreuzwerker state to this provider, and this provider rewrites it into its own schema (renamed attributes, MB to bytes, blocks to maps, ...). Data sources and actions hold no state; only their configuration changes.

## Requirements

- **Terraform 1.8 or later.** See the next section.
- **kreuzwerker/docker v4.6.0 state.** The move reads the state at kreuzwerker's latest schema version for each type (the table below). Run `terraform apply -refresh-only` once with kreuzwerker/docker v4.6.0 before migrating so older states are upgraded; a move from an older schema version fails with that instruction.
- **Different from and to addresses.** Terraform rejects a `moved` block whose `from` and `to` are the same address ("Redundant move statement"), so the resource label changes (`docker_container.web` to `docker_container.web_moved`). You can move it back to the old label later with an ordinary `moved` block (same provider, same type).

## Terraform 1.8 or later

Moving state between resource types or providers uses the MoveResourceState protocol call, added in Terraform 1.8. Terraform treats a `moved` block as a cross-provider move whenever the provider of the `to` resource differs from the provider recorded in state for the `from` resource, even when the type name (`docker_container`) is the same. Terraform reports the source provider as **`registry.terraform.io/kreuzwerker/docker`**, the address kreuzwerker/docker was installed from.

## How Terraform picks the providers

A `moved` block has no provider argument. The source provider is the one recorded in state for the `from` address (`registry.terraform.io/kreuzwerker/docker`); the target is the provider of the `to` resource in configuration. Both type names start with `docker_`, so both providers want the default local name `docker`. Give this provider the local name `docker` and kreuzwerker another local name while the migration runs:

```terraform
terraform {
  required_version = ">= 1.8"
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
    # Only while resources still use kreuzwerker: pins the version that wrote
    # the state (terraform init installs the providers named in state anyway).
    kreuzwerker = {
      source  = "kreuzwerker/docker"
      version = "4.6.0"
    }
  }
}

provider "docker" {}

# Not migrated yet: still managed by kreuzwerker.
resource "docker_network" "app" {
  provider = kreuzwerker
  name     = "app"
}
```

Resources without a `provider` argument use the local name `docker`, which is now this provider.

## Steps

1. Run `terraform apply -refresh-only` with kreuzwerker/docker v4.6.0 so every state is at its latest schema version.
2. Change `required_providers` as above and run `terraform init -upgrade`.
3. For each resource (or a few at a time): give it a new label, rewrite its body with this provider's attribute names (tables below), drop any `provider = kreuzwerker` argument, and add a `moved` block from the old address.
4. Run `terraform plan`. Terraform moves the state, refreshes it, and plans only real differences. Expect in-place updates where kreuzwerker stored an SDKv2 empty value ("" or an empty list) that this provider keeps as null, or where a default differs; a plan to replace a resource means its configuration differs in a "Forces replacement." attribute: fix the configuration rather than applying a replacement.
5. `terraform apply`. When no resource uses kreuzwerker any more, remove it from `required_providers` and remove the `moved` blocks once every workspace has applied them.

```terraform
# Before (kreuzwerker/docker v4.6.0)
resource "docker_container" "web" {
  name       = "web"
  image      = docker_image.nginx.image_id
  memory     = 512 # MB
  log_driver = "json-file"
  env        = ["APP_ENV=prod"]
  ports {
    internal = 80
    external = 8080
  }
  networks_advanced {
    name    = "app_net"
    aliases = ["web"]
  }
}

# After (JerryBalmer1/docker)
moved {
  from = docker_container.web
  to   = docker_container.web_moved
}

resource "docker_container" "web_moved" {
  name   = "web"
  image  = docker_image.nginx.id
  memory = 536870912 # bytes
  logging {
    driver = "json-file"
  }
  env = { APP_ENV = "prod" }
  ports {
    container_port = 80
    host_port      = 8080
  }
  networks = {
    app_net = { aliases = ["web"] }
  }
}
```

## Type rename

kreuzwerker's `data "docker_logs"` is `data "docker_container_logs"` here, beside `data "docker_service_logs"` and `data "docker_task_logs"` (one logs data source per Engine operation). Data sources hold no state: rename the block and its references.

## logging

Containers and services take one `logging { driver, options }` block. It is the same pair in all three upstream shapes:

| this provider | Engine container (HostConfig) | Swarm service (TaskTemplate) | compose |
|---|---|---|---|
| `logging.driver` | `LogConfig.Type` | `LogDriver.Name` | `logging.driver` |
| `logging.options` | `LogConfig.Config` | `LogDriver.Options` | `logging.options` |

kreuzwerker's `docker_container` `log_driver` / `log_opts` and `docker_service` `task_spec.log_driver { name, options }` both move into it.

## Service networks

`docker_service` takes a `networks` map keyed by network name (or ID), each value `{ aliases, driver_opts }`: one entry of the Swarm `TaskTemplate.Networks[]` list (`Target` = the key, `Aliases`, `DriverOpts`). kreuzwerker's `task_spec.networks_advanced` blocks move into it (key = `name`, or `id` when `name` is empty; `driver_opts` `"k=v"` strings become a map). `docker_container` takes the same kind of map for its networks (`networks_advanced` plus the computed `network_data`).

## Moved blocks per resource type

Every managed type below accepts a cross-provider `moved` block from kreuzwerker/docker. Rename the resource label (the `from` and `to` addresses must differ), keep the old address only in the `moved` block, and rewrite the resource body with the attribute names in the tables further down.

| kreuzwerker type | moves to | kreuzwerker state schema version | values that change on the move |
|---|---|---|---|
| `docker_buildx_builder` | `docker_buildx_builder` | 0 | - |
| `docker_compose` | `docker_compose` | 0 | `id` and `name` are the compose project name. |
| `docker_config` | `docker_config` | 0 | `data_raw` (plain text) is base64-encoded into `data`. |
| `docker_container` | `docker_container` | 2 | `network_data` is merged into the `networks` entries that `networks_advanced` declared; `volumes` blocks become `mounts` entries (type `bind` with a `host_path`, `volume` otherwise); `memory` / `memory_swap` / `shm_size` MB become bytes; `cpus` becomes `nano_cpus`; `env` becomes a map; `upload.executable = true` becomes `files.mode = "0744"`. |
| `docker_image` | `docker_image` | 0 | `id` becomes the image ID (kreuzwerker's `image_id`; kreuzwerker's `id` is image ID + name); `build.label` keys are merged into `build.labels`; `pull_triggers` is not moved (set it as `triggers` in configuration). |
| `docker_network` | `docker_network` | 1 | - |
| `docker_plugin` | `docker_plugin` | 0 | - |
| `docker_registry_image` | `docker_registry_image` | 0 | `id` becomes the pushed reference (`name`; kreuzwerker's `id` is the digest); `image_digest_ref` is set to `<repository>@<digest>`. |
| `docker_secret` | `docker_secret` | 1 | - |
| `docker_service` | `docker_service` | 2 | `task_spec.container_spec.*` and `task_spec.*` move to top-level attributes; `mode` becomes `mode = "replicated" \| "global"` plus `replicas`; `networks_advanced` becomes the `networks` map (key = `name`, or `id` when `name` is empty). |
| `docker_tag` | `docker_tag` | 0 | `id` becomes `<source_image_id>.<target_image>` (kreuzwerker: `<source_image>.<target_image>`). |
| `docker_volume` | `docker_volume` | 1 | - |

```terraform
moved {
  from = docker_buildx_builder.web
  to   = docker_buildx_builder.web_moved
}

moved {
  from = docker_compose.web
  to   = docker_compose.web_moved
}

moved {
  from = docker_config.web
  to   = docker_config.web_moved
}

moved {
  from = docker_container.web
  to   = docker_container.web_moved
}

moved {
  from = docker_image.web
  to   = docker_image.web_moved
}

moved {
  from = docker_network.web
  to   = docker_network.web_moved
}

moved {
  from = docker_plugin.web
  to   = docker_plugin.web_moved
}

moved {
  from = docker_registry_image.web
  to   = docker_registry_image.web_moved
}

moved {
  from = docker_secret.web
  to   = docker_secret.web_moved
}

moved {
  from = docker_service.web
  to   = docker_service.web_moved
}

moved {
  from = docker_tag.web
  to   = docker_tag.web_moved
}

moved {
  from = docker_volume.web
  to   = docker_volume.web_moved
}
```

## Attribute mapping

Generated from `reference/_gen/renames.json` by `go run ./tools/parity -guide`. Per type: renamed or reshaped attributes, attributes kept as they are, attributes dropped with the reason, and kreuzwerker attributes this provider does not support yet. Data sources and actions have no state to move: rewrite their configuration with the same tables.

### `docker_buildx_builder`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `append` | `append` | added (gap, Pass 5): bool, default false, RequiresReplace |
| `buildkit_config` | `buildkitd_config` | rename |
| `buildkit_flags` | `buildkitd_flags` | rename |
| `docker_container` | `docker_container` | list (max 1) block -> single block |
| `docker_container.cgroup_parent` | `docker_container.cgroup_parent` | added (gap, Pass 5) |
| `docker_container.cpu_period` | `docker_container.cpu_period` | added (gap, Pass 5) |
| `docker_container.cpu_quota` | `docker_container.cpu_quota` | added (gap, Pass 5) |
| `docker_container.cpu_shares` | `docker_container.cpu_shares` | added (gap, Pass 5) |
| `docker_container.cpuset_cpus` | `docker_container.cpuset_cpus` | added (gap, Pass 5) |
| `docker_container.cpuset_mems` | `docker_container.cpuset_mems` | added (gap, Pass 5) |
| `docker_container.default_load` | `docker_container.default_load` | added (gap, Pass 5): stored as a label; the seam loads by default on that builder |
| `docker_container.memory` | `docker_container.memory` | string -> number (bytes) |
| `docker_container.memory_swap` | `docker_container.memory_swap` | added (gap, Pass 5) |
| `endpoint` | `endpoint` | optional input -> computed |
| `node` | `node` | added (gap, Pass 5): Optional+Computed, default `<name>0`; container `buildx_buildkit_<node>` |
| `platform` | `platform` | added (gap, Pass 5): list, RequiresReplace; stored as a node-container label, listed first in `platforms` |

Unchanged: `bootstrap`, `docker_container.env`, `docker_container.image`, `docker_container.network`, `docker_container.restart_policy`, `driver`, `id`, `name`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `driver_options` | dropped: docker-container driver options are the docker_container block; other drivers not provided. |
| `kubernetes` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.annotations` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.default_load` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.image` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.labels` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.limits` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.limits.cpu` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.limits.ephemeral_storage` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.limits.memory` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.loadbalance` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.namespace` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.nodeselector` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.qemu` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.qemu.image` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.qemu.install` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.replicas` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.requests` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.requests.cpu` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.requests.ephemeral_storage` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.requests.memory` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.rootless` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.schedulername` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.serviceaccount` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.timeout` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `kubernetes.tolerations` | dropped: kubernetes driver not provided (the provider talks to Docker only). |
| `remote` | dropped: remote driver not provided (our endpoint is the computed address of the docker-container builder). |
| `remote.cacert` | dropped: remote driver not provided (our endpoint is the computed address of the docker-container builder). |
| `remote.cert` | dropped: remote driver not provided (our endpoint is the computed address of the docker-container builder). |
| `remote.default_load` | dropped: remote driver not provided (our endpoint is the computed address of the docker-container builder). |
| `remote.key` | dropped: remote driver not provided (our endpoint is the computed address of the docker-container builder). |
| `remote.servername` | dropped: remote driver not provided (our endpoint is the computed address of the docker-container builder). |
| `use` | dropped: no current-builder state; builds name their builder. |

### `docker_compose`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `config_paths` | `files` | rename |
| `env_files` | `env_file` | rename |
| `project_name` | `name` | rename |
| `remove_orphans` | `remove_orphans` | added (gap, Pass 5): . |

Unchanged: `id`, `profiles`, `project_directory`, `wait`, `wait_timeout`.

### `docker_config`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `data_raw` | `data` | plain text -> base64 (base64encode) |
| `labels` | `labels` | set of blocks {label, value} -> map(string) |
| `labels.label` | `labels` | block attribute -> map key |
| `labels.value` | `labels` | block attribute -> map value |

Unchanged: `data`, `id`, `name`.

### `docker_container`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `attach` | `state` | attach = true -> state = "exited" (run once, wait for the exit, keep the container) |
| `capabilities.add` | `cap_add` | rename; block attribute -> top-level set(string) |
| `capabilities.drop` | `cap_drop` | rename; block attribute -> top-level set(string) |
| `cgroup_parent` | `cgroup_parent` | added (gap, Pass 5) |
| `cgroupns_mode` | `cgroupns_mode` | added (gap, Pass 5) |
| `cpu_period` | `cpu_period` | added (gap, Pass 5) |
| `cpu_quota` | `cpu_quota` | added (gap, Pass 5) |
| `cpu_set` | `cpuset_cpus` | added (gap, Pass 5) |
| `cpus` | `nano_cpus` | string decimal CPUs -> number nano CPUs (x 1e9) |
| `device_cgroup_rules` | `device_cgroup_rules` | added (gap, Pass 5) |
| `device_read_bps` | `blkio_device_read_bps` | added (gap, Pass 5) |
| `device_read_bps.path` | `blkio_device_read_bps.path` | added (gap, Pass 5) |
| `device_read_bps.rate` | `blkio_device_read_bps.rate` | added (gap, Pass 5) |
| `device_read_iops` | `blkio_device_read_iops` | added (gap, Pass 5) |
| `device_read_iops.path` | `blkio_device_read_iops.path` | added (gap, Pass 5) |
| `device_read_iops.rate` | `blkio_device_read_iops.rate` | added (gap, Pass 5) |
| `device_requests` | `device_requests` | added (gap, Pass 5) |
| `device_requests.capabilities` | `device_requests.capabilities` | added (gap, Pass 5) |
| `device_requests.count` | `device_requests.count` | added (gap, Pass 5) |
| `device_requests.device_ids` | `device_requests.device_ids` | added (gap, Pass 5) |
| `device_requests.driver` | `device_requests.driver` | added (gap, Pass 5) |
| `device_requests.options` | `device_requests.options` | added (gap, Pass 5) |
| `device_write_bps` | `blkio_device_write_bps` | added (gap, Pass 5) |
| `device_write_bps.path` | `blkio_device_write_bps.path` | added (gap, Pass 5) |
| `device_write_bps.rate` | `blkio_device_write_bps.rate` | added (gap, Pass 5) |
| `device_write_iops` | `blkio_device_write_iops` | added (gap, Pass 5) |
| `device_write_iops.path` | `blkio_device_write_iops.path` | added (gap, Pass 5) |
| `device_write_iops.rate` | `blkio_device_write_iops.rate` | added (gap, Pass 5) |
| `devices` | `devices` | identity; set of blocks -> list of blocks |
| `devices.container_path` | `devices.path_in_container` | rename |
| `devices.host_path` | `devices.path_on_host` | rename |
| `devices.permissions` | `devices.cgroup_permissions` | rename |
| `dns` | `dns` | identity; set(string) -> list(string) |
| `dns_opts` | `dns_options` | added (gap, Pass 5) |
| `dns_search` | `dns_search` | identity; set(string) -> list(string) |
| `domainname` | `domainname` | added (gap, Pass 5) |
| `env` | `env` | set(string) "KEY=VALUE" -> map(string) |
| `exit_code` | `exit_code` | added (gap, Pass 5): computed |
| `group_add` | `group_add` | added (gap, Pass 5) |
| `healthcheck` | `healthcheck` | identity; list (max 1) block -> single block |
| `healthcheck.start_interval` | `healthcheck.start_interval` | added (gap, Pass 5) |
| `host` | `extra_hosts` | set of blocks {host, ip} -> list(string) "host:ip" |
| `host.host` | `extra_hosts` | block attribute -> part before ':' |
| `host.ip` | `extra_hosts` | block attribute -> part after ':' |
| `init` | `init` | added (gap, Pass 5) |
| `ipc_mode` | `ipc_mode` | added (gap, Pass 5) |
| `labels` | `labels` | set of blocks {label, value} -> map(string) |
| `labels.label` | `labels` | block attribute -> map key |
| `labels.value` | `labels` | block attribute -> map value |
| `log_driver` | `logging.driver` | rename; flat attribute -> single block logging |
| `log_opts` | `logging.options` | rename; flat attribute -> single block logging |
| `max_retry_count` | `maximum_retry_count` | rename |
| `memory` | `memory` | number MB -> number bytes (x 1048576) |
| `memory_reservation` | `memory_reservation` | added (gap, Pass 5) |
| `memory_swap` | `memory_swap` | number MB -> number bytes (x 1048576; -1 stays -1) |
| `mounts` | `mounts` | identity; set of blocks -> list of blocks |
| `mounts.bind_options` | `mounts.bind_options` | added (gap, Pass 5) |
| `mounts.bind_options.propagation` | `mounts.bind_options.propagation` | added (gap, Pass 5) |
| `mounts.tmpfs_options` | `mounts.tmpfs_options` | list (max 1) block -> single block |
| `mounts.tmpfs_options.mode` | `mounts.tmpfs_options.mode` | added (gap, Pass 5) |
| `mounts.volume_options` | `mounts.volume_options` | added (gap, Pass 5) |
| `mounts.volume_options.driver_name` | `mounts.volume_options.driver_config.name` | added (gap, Pass 5) |
| `mounts.volume_options.driver_options` | `mounts.volume_options.driver_config.options` | added (gap, Pass 5) |
| `mounts.volume_options.labels` | `mounts.volume_options.labels` | added (gap, Pass 5) |
| `mounts.volume_options.labels.label` | `mounts.volume_options.labels` | added (gap, Pass 5): map key |
| `mounts.volume_options.labels.value` | `mounts.volume_options.labels` | added (gap, Pass 5): map value |
| `mounts.volume_options.no_copy` | `mounts.volume_options.no_copy` | added (gap, Pass 5) |
| `mounts.volume_options.subpath` | `mounts.volume_options.subpath` | added (gap, Pass 5) |
| `network_data` | `networks` | computed list(object) keyed by network_name -> computed fields of the networks map |
| `network_data.gateway` | `networks.gateway` | rename (computed) |
| `network_data.global_ipv6_address` | `networks.global_ipv6_address` | rename (computed) |
| `network_data.global_ipv6_prefix_length` | `networks.global_ipv6_prefix_len` | rename (computed) |
| `network_data.ip_address` | `networks.ip_address` | rename (computed) |
| `network_data.ip_prefix_length` | `networks.ip_prefix_len` | rename (computed) |
| `network_data.ipv6_gateway` | `networks.ipv6_gateway` | rename (computed) |
| `network_data.mac_address` | `networks.mac_address` | rename (computed) |
| `network_data.network_name` | `networks` | object attribute -> map key |
| `network_mode` | `network_mode` | added (gap, Pass 5) |
| `networks_advanced` | `networks` | list of blocks keyed by name -> map keyed by network name; connect/disconnect in place (NetworkConnect/NetworkDisconnect) |
| `networks_advanced.aliases` | `networks.aliases` | rename (parent block -> map value) |
| `networks_advanced.driver_opts` | `networks.driver_opts` | set(string) "k=v" -> map(string) |
| `networks_advanced.gw_priority` | `networks.gw_priority` | rename (parent block -> map value) |
| `networks_advanced.ipv4_address` | `networks.ipv4_address` | rename (parent block -> map value) |
| `networks_advanced.ipv6_address` | `networks.ipv6_address` | rename (parent block -> map value) |
| `networks_advanced.link_local_ips` | `networks.link_local_ips` | rename (parent block -> map value) |
| `networks_advanced.mac_address` | `networks.mac_address` | rename (parent block -> map value) |
| `networks_advanced.name` | `networks` | block attribute -> map key |
| `pid_mode` | `pid_mode` | added (gap, Pass 5) |
| `platform` | `platform` | added (gap, Pass 5) |
| `ports.external` | `ports.host_port` | rename |
| `ports.internal` | `ports.container_port` | rename |
| `ports.ip` | `ports.host_ip` | rename |
| `publish_all_ports` | `publish_all_ports` | added (gap, Pass 5) |
| `rm` | `auto_remove` | added (gap, Pass 5) |
| `runtime` | `runtime` | added (gap, Pass 5) |
| `security_opts` | `security_opt` | rename |
| `shm_size` | `shm_size` | added (gap, Pass 5): bytes |
| `start` | `state` | start = false -> state = "created"; otherwise "running" (or "exited" with attach = true) |
| `storage_opts` | `storage_opt` | added (gap, Pass 5) |
| `tmpfs` | `tmpfs` | added (gap, Pass 5) |
| `ulimit` | `ulimits` | rename (set of blocks -> list of blocks) |
| `ulimit.hard` | `ulimits.hard` | rename (parent block) |
| `ulimit.name` | `ulimits.name` | rename (parent block) |
| `ulimit.soft` | `ulimits.soft` | rename (parent block) |
| `upload` | `files` | rename (list of blocks); set of blocks -> list of blocks |
| `upload.content` | `files.content` | rename (parent block) |
| `upload.content_base64` | `files.content_base64` | rename (parent block) |
| `upload.executable` | `files.mode` | bool -> mode: true = "0744" (unless permissions set); removed as a separate attribute |
| `upload.file` | `files.path` | rename |
| `upload.group` | `files.gid` | rename; string -> number |
| `upload.owner` | `files.uid` | rename; string -> number |
| `upload.permissions` | `files.mode` | rename |
| `upload.source` | `files.source` | rename (parent block) |
| `upload.source_hash` | `files.source_hash` | rename (parent block) |
| `userns_mode` | `userns_mode` | added (gap, Pass 5) |
| `volumes` | `mounts` | set of blocks -> mounts entries (type bind when host_path, volume when volume_name) |
| `volumes.container_path` | `mounts.target` | rename |
| `volumes.from_container` | `volumes_from` | added (gap, Pass 5) |
| `volumes.host_path` | `mounts.source` | rename; sets mounts.type = "bind" |
| `volumes.read_only` | `mounts.read_only` | identity (moved to mounts) |
| `volumes.selinux_relabel` | `mounts.selinux_relabel` | identity (moved to mounts) |
| `volumes.volume_name` | `mounts.source` | rename; sets mounts.type = "volume" |
| `wait_timeout` | `wait_timeout` | number seconds -> string Go duration ("<n>s") |

Unchanged: `command`, `cpu_shares`, `entrypoint`, `gpus`, `healthcheck.interval`, `healthcheck.retries`, `healthcheck.start_period`, `healthcheck.test`, `healthcheck.timeout`, `hostname`, `id`, `image`, `mounts.read_only`, `mounts.source`, `mounts.target`, `mounts.tmpfs_options.size_bytes`, `mounts.type`, `name`, `ports`, `ports.protocol`, `privileged`, `read_only`, `restart`, `stdin_open`, `stop_signal`, `stop_timeout`, `sysctls`, `tty`, `user`, `wait`, `working_dir`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `bridge` | dropped: deprecated upstream field. |
| `capabilities` | General rule: HostConfig has CapAdd/CapDrop, no capabilities object (compose cap_add/cap_drop; docker_service already uses them). |
| `container_logs` | dropped from moved state: with state = "exited" the run's output is in logs / log_entries (logs contract shape), captured at create; a moved container starts with them null. |
| `container_read_refresh_timeout_milliseconds` | dropped: wait / wait_timeout cover waiting for the container. |
| `destroy_grace_seconds` | dropped: destroy force-removes the container; stop_timeout covers graceful stops. |
| `logs` | dropped: the logs flag is not needed; with state = "exited" (attach = true) the run's output is captured at create in logs / log_entries, and data.docker_container_logs reads any container's logs. |
| `must_run` | dropped (Pass 5.3 D1): subsumed by state = "running", which starts a container found stopped in place. must_run = false (start once, then leave a stopped container alone) has no counterpart: use state = "created" plus the docker_container_start action. |
| `remove_volumes` | dropped: destroy keeps volumes; manage them with docker_volume. |
| `timeouts` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.create` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.delete` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.update` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |

### `docker_image`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `build` | `build` | set (max 1) of blocks -> single block |
| `build.additional_contexts` | `build.additional_contexts` | added (gap, Pass 5): in the schema; returns "not supported yet" until seamBuildRequest gets named contexts, see TODO |
| `build.build_args` | `build.args` | rename |
| `build.cache_to` | `build.cache_to` | added (gap, Pass 5) |
| `build.cgroup_parent` | `build.cgroup_parent` | added (gap, Pass 5) |
| `build.cpu_period` | `build.cpu_period` | added (gap, Pass 5) |
| `build.cpu_quota` | `build.cpu_quota` | added (gap, Pass 5) |
| `build.cpu_set_cpus` | `build.cpuset_cpus` | added (gap, Pass 5) |
| `build.cpu_set_mems` | `build.cpuset_mems` | added (gap, Pass 5) |
| `build.cpu_shares` | `build.cpu_shares` | added (gap, Pass 5) |
| `build.extra_hosts` | `build.extra_hosts` | added (gap, Pass 5) |
| `build.force_remove` | `build.force_rm` | added (gap, Pass 5) |
| `build.isolation` | `build.isolation` | added (gap, Pass 5) |
| `build.label` | `build.labels` | rename (deprecated kreuzwerker duplicate of labels) |
| `build.memory` | `build.memory` | added (gap, Pass 5) |
| `build.memory_swap` | `build.memswap` | added (gap, Pass 5) |
| `build.network_mode` | `build.network` | added (gap, Pass 5) |
| `build.provenance` | `build.provenance` | added (gap, Pass 5) |
| `build.pull_parent` | `build.pull` | rename |
| `build.remote_context` | `build.remote` | added (gap, Pass 5) |
| `build.sbom` | `build.sbom` | added (gap, Pass 5) |
| `build.secrets` | `build.secrets` | list of blocks (identity) |
| `build.shm_size` | `build.shm_size` | added (gap, Pass 5) |
| `build.squash` | `build.squash` | added (gap, Pass 5) |
| `build.tag` | `build.tags` | added (gap, Pass 5) |
| `build.ulimit` | `build.ulimits` | added (gap, Pass 5) |
| `build.ulimit.hard` | `build.ulimits.hard` | added (gap, Pass 5) |
| `build.ulimit.name` | `build.ulimits` | added (gap, Pass 5): map key |
| `build.ulimit.soft` | `build.ulimits.soft` | added (gap, Pass 5) |
| `id` | `id` | value change: kreuzwerker id = image ID + name; ours = image ID |
| `image_id` | `id` | rename; image_id removed |
| `pull_triggers` | `triggers` | set(string) -> map(string) (any keys); config-only, no state to migrate |
| `repo_digest` | `image_digest_ref` | rename; upstream RepoDigests list goes in the structured output |

Unchanged: `build.builder`, `build.cache_from`, `build.context`, `build.dockerfile`, `build.labels`, `build.no_cache`, `build.platform`, `build.secrets.env`, `build.secrets.id`, `build.secrets.src`, `build.target`, `force_remove`, `keep_locally`, `name`, `platform`, `triggers`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `build.auth_config` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.auth_config.auth` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.auth_config.email` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.auth_config.host_name` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.auth_config.identity_token` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.auth_config.password` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.auth_config.registry_token` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.auth_config.server_address` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.auth_config.user_name` | dropped: registry credentials come from the provider registry_auth blocks. |
| `build.build_id` | dropped: build cancellation by ID is not exposed. |
| `build.build_log_file` | dropped: the build log is the build_log raw output (Q33), not a file. |
| `build.remove` | dropped: intermediate containers are always removed (ImageBuild rm=true). |
| `build.security_opt` | dropped: ImageBuild has no security options in API 1.56 (BuildKit entitlements are separate). |
| `build.session_id` | dropped: BuildKit session is managed by the provider. |
| `build.suppress_output` | dropped: the build log is always captured (build_log). |
| `build.use_legacy_builder` | dropped: the backend is chosen by build.builder. |
| `build.version` | dropped: the backend is chosen by build.builder (ImageBuild query version). |
| `timeouts` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.create` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.delete` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.update` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |

### `docker_network`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `ipam_config` | `ipam_config` | identity; set of blocks -> list of blocks |
| `ipam_options` | `ipam_options` | added (gap, Pass 5) |
| `ipv6` | `enable_ipv6` | rename |
| `labels` | `labels` | set of blocks {label, value} -> map(string) |
| `labels.label` | `labels` | block attribute -> map key |
| `labels.value` | `labels` | block attribute -> map value |

Unchanged: `attachable`, `driver`, `id`, `ingress`, `internal`, `ipam_config.aux_address`, `ipam_config.gateway`, `ipam_config.ip_range`, `ipam_config.subnet`, `ipam_driver`, `name`, `options`, `scope`.

### `docker_plugin`

Unchanged: `alias`, `enable_timeout`, `enabled`, `env`, `force_destroy`, `force_disable`, `grant_all_permissions`, `grant_permissions`, `grant_permissions.name`, `grant_permissions.value`, `id`, `name`, `plugin_reference`.

### `docker_registry_image`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `id` | `id` | value change: kreuzwerker id = digest; ours = pushed reference (name) |
| `sha256_digest` | `digest` | rename (image_digest_ref added beside it) |

Unchanged: `insecure_skip_verify`, `keep_remotely`, `name`, `triggers`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `auth_config` | dropped: registry credentials come from the provider registry_auth blocks. |
| `auth_config.address` | dropped: provider registry_auth. |
| `auth_config.password` | dropped: provider registry_auth. |
| `auth_config.username` | dropped: provider registry_auth. |
| `build` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.additional_contexts` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config.auth` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config.email` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config.host_name` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config.identity_token` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config.password` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config.registry_token` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config.server_address` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.auth_config.user_name` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.build_args` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.build_id` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.build_log_file` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.builder` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.cache_from` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.cache_to` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.cgroup_parent` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.context` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.cpu_period` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.cpu_quota` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.cpu_set_cpus` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.cpu_set_mems` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.cpu_shares` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.dockerfile` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.extra_hosts` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.force_remove` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.isolation` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.label` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.labels` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.memory` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.memory_swap` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.network_mode` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.no_cache` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.platform` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.provenance` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.pull_parent` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.remote_context` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.remove` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.sbom` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.secrets` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.secrets.env` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.secrets.id` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.secrets.src` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.security_opt` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.session_id` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.shm_size` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.squash` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.suppress_output` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.tag` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.target` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.ulimit` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.ulimit.hard` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.ulimit.name` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.ulimit.soft` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.use_legacy_builder` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `build.version` | dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image. |
| `timeouts` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.create` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.delete` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |
| `timeouts.update` | dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema). |

### `docker_secret`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `labels` | `labels` | set of blocks {label, value} -> map(string) |
| `labels.label` | `labels` | block attribute -> map key |
| `labels.value` | `labels` | block attribute -> map value |

Unchanged: `data`, `id`, `name`.

### `docker_service`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `converge_config` | `converge_config` | identity; list (max 1) block -> single block |
| `endpoint_spec` | `endpoint_spec` | identity; list (max 1) block -> single block |
| `endpoint_spec.ports.name` | `endpoint_spec.ports.name` | added (gap, Pass 5) |
| `labels` | `labels` | set of blocks {label, value} -> map(string) |
| `labels.label` | `labels` | block attribute -> map key |
| `labels.value` | `labels` | block attribute -> map value |
| `mode` | `mode` | block {global, replicated{replicas}} -> string "replicated" \| "global" plus top-level replicas |
| `mode.global` | `mode` | bool true -> mode = "global" |
| `mode.replicated` | `mode` | block present -> mode = "replicated" |
| `mode.replicated.replicas` | `replicas` | moved to top level |
| `rollback_config` | `rollback_config` | identity; list (max 1) block -> single block |
| `rollback_config.max_failure_ratio` | `rollback_config.max_failure_ratio` | identity; string -> number |
| `task_spec.container_spec.args` | `args` | identity (flattened path) |
| `task_spec.container_spec.cap_add` | `cap_add` | identity (flattened path); list(string) -> set(string) |
| `task_spec.container_spec.cap_drop` | `cap_drop` | identity (flattened path); list(string) -> set(string) |
| `task_spec.container_spec.command` | `command` | identity (flattened path) |
| `task_spec.container_spec.configs` | `configs` | identity (flattened path); set of blocks -> list of blocks |
| `task_spec.container_spec.configs.config_id` | `configs.config_id` | identity (flattened path) |
| `task_spec.container_spec.configs.config_name` | `configs.config_name` | identity (flattened path) |
| `task_spec.container_spec.configs.file_gid` | `configs.gid` | rename (flattened path) |
| `task_spec.container_spec.configs.file_mode` | `configs.mode` | rename (flattened path) |
| `task_spec.container_spec.configs.file_name` | `configs.file_name` | identity (flattened path) |
| `task_spec.container_spec.configs.file_uid` | `configs.uid` | rename (flattened path) |
| `task_spec.container_spec.dir` | `dir` | identity (flattened path) |
| `task_spec.container_spec.dns_config` | `dns_config` | list (max 1) block -> single block |
| `task_spec.container_spec.dns_config.nameservers` | `dns_config.nameservers` | identity (flattened path) |
| `task_spec.container_spec.dns_config.options` | `dns_config.options` | identity (flattened path) |
| `task_spec.container_spec.dns_config.search` | `dns_config.search` | identity (flattened path) |
| `task_spec.container_spec.env` | `env` | identity (flattened path) |
| `task_spec.container_spec.groups` | `groups` | added (gap, Pass 5) |
| `task_spec.container_spec.healthcheck` | `healthcheck` | identity (flattened path); list (max 1) block -> single block |
| `task_spec.container_spec.healthcheck.interval` | `healthcheck.interval` | identity (flattened path) |
| `task_spec.container_spec.healthcheck.retries` | `healthcheck.retries` | identity (flattened path) |
| `task_spec.container_spec.healthcheck.start_period` | `healthcheck.start_period` | identity (flattened path) |
| `task_spec.container_spec.healthcheck.test` | `healthcheck.test` | identity (flattened path) |
| `task_spec.container_spec.healthcheck.timeout` | `healthcheck.timeout` | identity (flattened path) |
| `task_spec.container_spec.hostname` | `hostname` | identity (flattened path) |
| `task_spec.container_spec.hosts` | `hosts` | set of blocks {host, ip} -> list(string) "<ip> <host>" |
| `task_spec.container_spec.hosts.host` | `hosts` | block attribute -> hostname part |
| `task_spec.container_spec.hosts.ip` | `hosts` | block attribute -> IP part |
| `task_spec.container_spec.image` | `image` | identity (flattened path) |
| `task_spec.container_spec.init` | `init` | added (gap, Pass 5) |
| `task_spec.container_spec.isolation` | `isolation` | added (gap, Pass 5) |
| `task_spec.container_spec.labels` | `container_labels` | set of blocks {label, value} -> map(string) (flattened path) |
| `task_spec.container_spec.labels.label` | `container_labels` | block attribute -> map key |
| `task_spec.container_spec.labels.value` | `container_labels` | block attribute -> map value |
| `task_spec.container_spec.mounts` | `mounts` | identity (flattened path); set of blocks -> list of blocks |
| `task_spec.container_spec.mounts.bind_options` | `mounts.bind_options` | added (gap, Pass 5) |
| `task_spec.container_spec.mounts.bind_options.propagation` | `mounts.bind_options.propagation` | added (gap, Pass 5) |
| `task_spec.container_spec.mounts.read_only` | `mounts.read_only` | identity (flattened path) |
| `task_spec.container_spec.mounts.source` | `mounts.source` | identity (flattened path) |
| `task_spec.container_spec.mounts.target` | `mounts.target` | identity (flattened path) |
| `task_spec.container_spec.mounts.tmpfs_options` | `mounts.tmpfs_options` | list (max 1) block -> single block |
| `task_spec.container_spec.mounts.tmpfs_options.mode` | `mounts.tmpfs_options.mode` | added (gap, Pass 5) |
| `task_spec.container_spec.mounts.tmpfs_options.size_bytes` | `mounts.tmpfs_options.size_bytes` | identity (our previous flat tmpfs_size) |
| `task_spec.container_spec.mounts.type` | `mounts.type` | identity (flattened path) |
| `task_spec.container_spec.mounts.volume_options` | `mounts.volume_options` | list (max 1) block -> single block |
| `task_spec.container_spec.mounts.volume_options.driver_name` | `mounts.volume_options.driver_config.name` | rename |
| `task_spec.container_spec.mounts.volume_options.driver_options` | `mounts.volume_options.driver_config.options` | rename |
| `task_spec.container_spec.mounts.volume_options.labels` | `mounts.volume_options.labels` | set of blocks {label, value} -> map(string) |
| `task_spec.container_spec.mounts.volume_options.labels.label` | `mounts.volume_options.labels` | block attribute -> map key |
| `task_spec.container_spec.mounts.volume_options.labels.value` | `mounts.volume_options.labels` | block attribute -> map value |
| `task_spec.container_spec.mounts.volume_options.no_copy` | `mounts.volume_options.no_copy` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges` | `privileges` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges.credential_spec` | `privileges.credential_spec` | added (gap, Pass 5): ; `config` was added too |
| `task_spec.container_spec.privileges.credential_spec.file` | `privileges.credential_spec.file` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges.credential_spec.registry` | `privileges.credential_spec.registry` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges.se_linux_context` | `privileges.selinux_context` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges.se_linux_context.disable` | `privileges.selinux_context.disable` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges.se_linux_context.level` | `privileges.selinux_context.level` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges.se_linux_context.role` | `privileges.selinux_context.role` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges.se_linux_context.type` | `privileges.selinux_context.type` | added (gap, Pass 5) |
| `task_spec.container_spec.privileges.se_linux_context.user` | `privileges.selinux_context.user` | added (gap, Pass 5) |
| `task_spec.container_spec.read_only` | `read_only` | added (gap, Pass 5) |
| `task_spec.container_spec.secrets` | `secrets` | identity (flattened path); set of blocks -> list of blocks |
| `task_spec.container_spec.secrets.file_gid` | `secrets.gid` | rename (flattened path) |
| `task_spec.container_spec.secrets.file_mode` | `secrets.mode` | rename (flattened path) |
| `task_spec.container_spec.secrets.file_name` | `secrets.file_name` | identity (flattened path) |
| `task_spec.container_spec.secrets.file_uid` | `secrets.uid` | rename (flattened path) |
| `task_spec.container_spec.secrets.secret_id` | `secrets.secret_id` | identity (flattened path) |
| `task_spec.container_spec.secrets.secret_name` | `secrets.secret_name` | identity (flattened path) |
| `task_spec.container_spec.stop_grace_period` | `stop_grace_period` | identity (flattened path) |
| `task_spec.container_spec.stop_signal` | `stop_signal` | added (gap, Pass 5) |
| `task_spec.container_spec.sysctl` | `sysctls` | rename (flattened path) |
| `task_spec.container_spec.user` | `user` | identity (flattened path) |
| `task_spec.force_update` | `force_update` | moved to top level |
| `task_spec.log_driver` | `logging` | list (max 1) block -> single block; renamed |
| `task_spec.log_driver.name` | `logging.driver` | rename |
| `task_spec.log_driver.options` | `logging.options` | rename |
| `task_spec.networks_advanced` | `networks` | set of blocks -> map keyed by network name |
| `task_spec.networks_advanced.aliases` | `networks.aliases` | rename (map value) |
| `task_spec.networks_advanced.driver_opts` | `networks.driver_opts` | rename (map value) |
| `task_spec.networks_advanced.name` | `networks` | block attribute -> map key |
| `task_spec.placement` | `placement` | identity (flattened path); list (max 1) block -> single block |
| `task_spec.placement.constraints` | `placement.constraints` | identity (flattened path); set(string) -> list(string) |
| `task_spec.placement.max_replicas` | `placement.max_replicas` | identity (flattened path) |
| `task_spec.placement.platforms` | `placement.platforms` | added (gap, Pass 5) |
| `task_spec.placement.platforms.architecture` | `placement.platforms.architecture` | added (gap, Pass 5) |
| `task_spec.placement.platforms.os` | `placement.platforms.os` | added (gap, Pass 5) |
| `task_spec.placement.prefs` | `placement.preferences` | rename (set(string) -> list(string)) |
| `task_spec.resources` | `resources` | identity (flattened path); list (max 1) block -> single block |
| `task_spec.resources.limits` | `resources.limits` | identity (flattened path); list (max 1) block -> single block |
| `task_spec.resources.limits.memory_bytes` | `resources.limits.memory_bytes` | identity (flattened path) |
| `task_spec.resources.limits.nano_cpus` | `resources.limits.nano_cpus` | identity (flattened path) |
| `task_spec.resources.reservation` | `resources.reservations` | rename (list max 1 -> single block) |
| `task_spec.resources.reservation.generic_resources` | `resources.reservations.generic_resources` | added (gap, Pass 5) |
| `task_spec.resources.reservation.generic_resources.discrete_resources_spec` | `resources.reservations.generic_resources.discrete_resource_spec` | added (gap, Pass 5): {kind, value} |
| `task_spec.resources.reservation.generic_resources.named_resources_spec` | `resources.reservations.generic_resources.named_resource_spec` | added (gap, Pass 5): {kind, value} |
| `task_spec.resources.reservation.memory_bytes` | `resources.reservations.memory_bytes` | rename (parent) |
| `task_spec.resources.reservation.nano_cpus` | `resources.reservations.nano_cpus` | rename (parent) |
| `task_spec.restart_policy` | `restart_policy` | identity (flattened path); list (max 1) block -> single block |
| `task_spec.restart_policy.condition` | `restart_policy.condition` | identity (flattened path) |
| `task_spec.restart_policy.delay` | `restart_policy.delay` | identity (flattened path) |
| `task_spec.restart_policy.max_attempts` | `restart_policy.max_attempts` | identity (flattened path) |
| `task_spec.restart_policy.window` | `restart_policy.window` | identity (flattened path) |
| `task_spec.runtime` | `runtime` | added (gap, Pass 5) |
| `update_config` | `update_config` | identity; list (max 1) block -> single block |
| `update_config.max_failure_ratio` | `update_config.max_failure_ratio` | identity; string -> number |

Unchanged: `converge_config.delay`, `converge_config.timeout`, `endpoint_spec.mode`, `endpoint_spec.ports`, `endpoint_spec.ports.protocol`, `endpoint_spec.ports.publish_mode`, `endpoint_spec.ports.published_port`, `endpoint_spec.ports.target_port`, `id`, `name`, `rollback_config.delay`, `rollback_config.failure_action`, `rollback_config.monitor`, `rollback_config.order`, `rollback_config.parallelism`, `update_config.delay`, `update_config.failure_action`, `update_config.monitor`, `update_config.order`, `update_config.parallelism`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `auth` | dropped: registry credentials come from the provider registry_auth blocks. |
| `auth.password` | dropped: provider registry_auth. |
| `auth.server_address` | dropped: provider registry_auth. |
| `auth.username` | dropped: provider registry_auth. |
| `task_spec` | flattened: task_spec fields are top-level attributes of docker_service (no task_spec block). |
| `task_spec.container_spec` | flattened: container_spec fields are top-level attributes of docker_service. |
| `task_spec.networks_advanced.id` | dropped: the map key (network name or ID) is the target. |

### `docker_tag`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `id` | `id` | value change: kreuzwerker id = <source_image>.<target_image>; ours = <source_image_id>.<target_image> |
| `tag_triggers` | `triggers` | added (gap, Pass 5) |

Unchanged: `source_image`, `source_image_id`, `target_image`.

### `docker_volume`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `cluster` | `cluster_volume_spec` | added (gap, Pass 5) |
| `cluster.availability` | `cluster_volume_spec.availability` | added (gap, Pass 5): in place via VolumeUpdate |
| `cluster.group` | `cluster_volume_spec.group` | added (gap, Pass 5) |
| `cluster.id` | `cluster_volume_id` | added (gap, Pass 5): computed, top level: ClusterVolume.ID is not part of the spec |
| `cluster.limit_bytes` | `cluster_volume_spec.capacity_range.limit_bytes` | added (gap, Pass 5) |
| `cluster.required_bytes` | `cluster_volume_spec.capacity_range.required_bytes` | added (gap, Pass 5) |
| `cluster.scope` | `cluster_volume_spec.access_mode.scope` | added (gap, Pass 5) |
| `cluster.sharing` | `cluster_volume_spec.access_mode.sharing` | added (gap, Pass 5) |
| `cluster.topology_preferred` | `cluster_volume_spec.accessibility_requirements.preferred` | added (gap, Pass 5) |
| `cluster.topology_required` | `cluster_volume_spec.accessibility_requirements.requisite` | added (gap, Pass 5) |
| `cluster.type` | `cluster_volume_spec.access_mode.mount_volume` | added (gap, Pass 5): / cluster_volume_spec.access_mode.block_volume |
| `labels` | `labels` | set of blocks {label, value} -> map(string) |
| `labels.label` | `labels` | block attribute -> map key |
| `labels.value` | `labels` | block attribute -> map value |

Unchanged: `driver`, `driver_opts`, `id`, `mountpoint`, `name`.

### `data.docker_container_logs (kreuzwerker data.docker_logs)`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `logs_list_string` | `log_entries.message` | list(string) -> list(object); each line's text is .message (raw `logs` string and timestamp/stream/source added) |
| `name` | `container` | rename (ID or name) |
| `show_stderr` | `stderr` | rename |
| `show_stdout` | `stdout` | rename |

Unchanged: `details`, `id`, `since`, `tail`, `until`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `discard_headers` | dropped: stream headers are always demultiplexed into log_entries.stream. |
| `follow` | dropped: a data source cannot follow a stream. |
| `logs_list_string_enabled` | dropped: log_entries is always populated (Q6 logs contract). |
| `timestamps` | Q6 logs contract: log_entries.timestamp always comes from timestamps=true. |

### `data.docker_containers`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `containers.created` | `containers.created` | number (unix seconds) -> string (RFC 3339) |

Unchanged: `containers`, `containers.command`, `containers.id`, `containers.image`, `containers.image_id`, `containers.labels`, `containers.names`, `containers.state`, `containers.status`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `id` | dropped: the data source has no id attribute. |

### `data.docker_image`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `repo_digest` | `image_digest_ref` | rename; upstream RepoDigests list goes in the structured output |

Unchanged: `id`, `name`.

### `data.docker_network`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `containers` | `containers` | added (gap, Pass 5): map keyed by container ID |
| `containers.container_id` | `containers` | added (gap, Pass 5): map key |
| `containers.endpoint_id` | `containers.endpoint_id` | added (gap, Pass 5) |
| `containers.ipv4_address` | `containers.ipv4_address` | added (gap, Pass 5) |
| `containers.ipv6_address` | `containers.ipv6_address` | added (gap, Pass 5) |
| `containers.mac_address` | `containers.mac_address` | added (gap, Pass 5) |
| `containers.name` | `containers.name` | added (gap, Pass 5) |
| `ipam_config` | `ipam_config` | identity; set(object) -> list(object) |

Unchanged: `driver`, `id`, `internal`, `ipam_config.aux_address`, `ipam_config.gateway`, `ipam_config.ip_range`, `ipam_config.subnet`, `name`, `options`, `scope`.

### `data.docker_plugin`

Unchanged: `alias`, `enabled`, `env`, `id`, `name`, `plugin_reference`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `grant_all_permissions` | dropped: an install-time flag, not readable from PluginInspect. |

### `data.docker_registry_image`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `insecure_skip_verify` | `insecure_skip_verify` | added (gap, Pass 5) |
| `sha256_digest` | `digest` | rename (image_digest_ref added beside it) |

Unchanged: `id`, `name`.

### `data.docker_registry_image_manifests`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `manifests` | `manifests` | identity; set(object) -> list(object) |
| `manifests.sha256_digest` | `manifests.digest` | rename |

Unchanged: `insecure_skip_verify`, `manifests.architecture`, `manifests.media_type`, `manifests.os`, `name`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `auth_config` | dropped: registry credentials come from the provider registry_auth blocks. |
| `auth_config.address` | dropped: provider registry_auth. |
| `auth_config.password` | dropped: provider registry_auth. |
| `auth_config.username` | dropped: provider registry_auth. |
| `id` | dropped: the data source has no id attribute; name identifies it. |

### `data.docker_registry_image_tags`

Unchanged: `insecure_skip_verify`, `name`, `tags`.

Dropped:

| kreuzwerker | reason |
|---|---|
| `id` | dropped: the data source has no id attribute; repository identifies it. |
| `strict_semver` | dropped: filter tags in Terraform (e.g. regex) instead. |

### `action.docker_container_export`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `container` | `container_id` | rename (accepts ID or name as before) |

Unchanged: `output`.

### `action.docker_exec`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `container` | `container_id` | rename (accepts ID or name as before) |
| `detach` | `detach` | added (gap, Pass 5) |
| `env` | `env` | list(string) "KEY=VALUE" -> map(string) |
| `env_file` | `env_file` | added (gap, Pass 5) |
| `tty` | `tty` | added (gap, Pass 5) |

Unchanged: `command`, `privileged`, `user`, `workdir`.

### `action.docker_image_import`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `reference` | `repository` | split "repo:tag" into repository and tag |

Unchanged: `changes`, `message`, `platform`, `source`.

### `action.docker_image_load`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `platform` | `platform` | added (gap, Pass 5) |
| `source` | `input` | rename (our previous path) |

Dropped:

| kreuzwerker | reason |
|---|---|
| `quiet` | dropped: progress goes to action progress messages. |

### `action.docker_image_save`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `platform` | `platform` | added (gap, Pass 5) |

Unchanged: `images`, `output`.

### `action.docker_system_prune`

Renamed or reshaped:

| kreuzwerker | this provider | change |
|---|---|---|
| `filter` | `filters` | list(string) "k=v" -> map(string) |

Unchanged: `all`, `volumes`.
