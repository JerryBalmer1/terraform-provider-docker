# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

This section covers build passes 5, 5.1 and 5.2 (the log of each pass is in [BUILD_REPORT.md](BUILD_REPORT.md)).

### Breaking

Keep a Changelog has no Breaking heading. It is listed first here because upgrading requires configuration and state changes.

Pass 5 renamed attributes to their upstream names (Engine API, compose-spec, docker CLI, buildx). Most of the old shapes came from kreuzwerker/docker; several are this provider's own. If you are coming from kreuzwerker/docker, follow the [migration guide](docs/guides/migrating-from-kreuzwerker.md). It covers the `moved` blocks for all 12 kreuzwerker resource types (Terraform >= 1.8) and the full kreuzwerker attribute mapping, including the kreuzwerker attributes that are dropped, each with a reason.

Behaviour that changes without a rename:

- `docker_tag.id` is now `<source image ID>.<target image>`. Existing state is migrated on read.
- Plural data sources such as `docker_configs`, `docker_secrets`, `docker_nodes` and `docker_services` hold the full Engine objects, field by field. For example, `nodes[*].hostname` is now `nodes[*].description.hostname`; every moved field is listed below.

The attributes below changed between this provider's schema before Pass 5 and now. The list is generated from [reference/_gen/renames.json](reference/_gen/renames.json): every row whose previous attribute differs from the current one, was removed, or (for this provider's own attributes) changed shape. Each row reads `old` -> `new`, with the shape change after the colon where there is one. `Q<n>` and "General rule" in a reason refer to [reference/DECISIONS.md](reference/DECISIONS.md).

Type rename: `data.docker_logs` -> `data.docker_container_logs`. Q6: one logs data source per Engine operation (ContainerLogs / ServiceLogs / TaskLogs); the container one is named for its object, beside data.docker_service_logs and data.docker_task_logs.

#### Resources

**`docker_buildx_builder`**

- `buildkit_config` -> `buildkitd_config`
- `buildkit_config_content` -> `buildkitd_config_content`
- `buildkit_flags` -> `buildkitd_flags`

**`docker_compose`**

- `project_name` -> `name`
- `working_dir` -> `project_directory`

**`docker_container`**

- `capabilities` removed; single block {add, drop} -> flattened into top-level cap_add / cap_drop. General rule: HostConfig has CapAdd/CapDrop, no capabilities object (compose cap_add/cap_drop; docker_service already uses them).
- `capabilities.add` -> `cap_add`: rename; block attribute -> top-level set(string)
- `capabilities.drop` -> `cap_drop`: rename; block attribute -> top-level set(string)
- `devices.container_path` -> `devices.path_in_container`
- `devices.host_path` -> `devices.path_on_host`
- `devices.permissions` -> `devices.cgroup_permissions`
- `log_driver` -> `logging.driver`: rename; flat attribute -> single block logging
- `log_opts` -> `logging.options`: rename; flat attribute -> single block logging
- `max_retry_count` -> `maximum_retry_count`
- `mounts.tmpfs_size` -> `mounts.tmpfs_options.size_bytes`
- `networks_advanced` -> `networks`: list of blocks keyed by name -> map keyed by network name; connect/disconnect in place (NetworkConnect/NetworkDisconnect)
- `networks_advanced.aliases` -> `networks.aliases`: rename (parent block -> map value)
- `networks_advanced.ipv4_address` -> `networks.ipv4_address`: rename (parent block -> map value)
- `networks_advanced.ipv6_address` -> `networks.ipv6_address`: rename (parent block -> map value)
- `networks_advanced.name` -> `networks`: block attribute -> map key
- `ports.external` -> `ports.host_port`
- `ports.internal` -> `ports.container_port`
- `ports.ip` -> `ports.host_ip`
- `upload` -> `files`: rename (list of blocks); set of blocks -> list of blocks
- `upload.content` -> `files.content`: rename (parent block)
- `upload.content_base64` -> `files.content_base64`: rename (parent block)
- `upload.executable` -> `files.mode`: bool -> mode: true = "0744" (unless permissions set); removed as a separate attribute
- `upload.file` -> `files.path`
- `upload.group` -> `files.gid`: rename; string -> number
- `upload.owner` -> `files.uid`: rename; string -> number
- `upload.permissions` -> `files.mode`
- `upload.source` -> `files.source`: rename (parent block)
- `upload.source_hash` -> `files.source_hash`: rename (parent block)

**`docker_image`**

- `image_id` -> `id`: rename; image_id removed
- `repo_digest` -> `image_digest_ref`: rename; upstream RepoDigests list goes in the structured output

**`docker_network`**

- `ipv6` -> `enable_ipv6`

**`docker_registry_image`**

- `sha256_digest` -> `digest`: rename (image_digest_ref added beside it)

**`docker_service`**

- `dns` -> `dns_config`
- `dns.nameservers` -> `dns_config.nameservers`
- `dns.options` -> `dns_config.options`
- `dns.search` -> `dns_config.search`
- `mounts.tmpfs_size` -> `mounts.tmpfs_options.size_bytes`
- `mounts.volume_driver` -> `mounts.volume_options.driver_config.name`
- `mounts.volume_driver_opts` -> `mounts.volume_options.driver_config.options`
- `mounts.volume_labels` -> `mounts.volume_options.labels`
- `networks` (same name): set(string) of network names -> map keyed by network name (NetworkAttachmentConfig); old values become the keys
- `networks_advanced` -> `networks`
- `networks_advanced.aliases` -> `networks.aliases`
- `networks_advanced.name` -> `networks`

**`docker_stack`**

- `service.dns` -> `service.dns_config`: rename (single block)
- `service.dns.nameservers` -> `service.dns_config.nameservers`: rename (parent block)
- `service.dns.options` -> `service.dns_config.options`: rename (parent block)
- `service.dns.search` -> `service.dns_config.search`: rename (parent block)
- `service.mounts.tmpfs_size` -> `service.mounts.tmpfs_options.size_bytes`: rename; flat attribute -> nested single block tmpfs_options
- `service.mounts.volume_driver` -> `service.mounts.volume_options.driver_config.name`: rename; flat attribute -> nested blocks volume_options.driver_config
- `service.mounts.volume_driver_opts` -> `service.mounts.volume_options.driver_config.options`: rename; flat attribute -> nested blocks volume_options.driver_config
- `service.mounts.volume_labels` -> `service.mounts.volume_options.labels`: rename; flat attribute -> nested single block volume_options
- `service.networks` (same name): set(string) of network names -> map keyed by network name (NetworkAttachmentConfig); old values become the keys
- `service.networks_advanced` -> `service.networks`: list of blocks keyed by name -> map keyed by network name (merged into networks)
- `service.networks_advanced.aliases` -> `service.networks.aliases`: rename (parent block -> map value)
- `service.networks_advanced.name` -> `service.networks`: block attribute -> map key

#### Data sources

**`data.docker_configs`**

- `configs.data` -> `configs.spec.data`: rename; flat list field -> upstream path in the configs objects
- `configs.labels` -> `configs.spec.labels`: rename; flat list field -> upstream path in the configs objects
- `configs.name` -> `configs.spec.name`: rename; flat list field -> upstream path in the configs objects

**`data.docker_container_logs`**

- `logs_list_string` -> `log_entries.message`: list(string) -> list(object); each line's text is .message (raw `logs` string and timestamp/stream/source added)
- `show_stderr` -> `stderr`
- `show_stdout` -> `stdout`
- `timestamps` removed; timestamps are always requested. Q6 logs contract: log_entries.timestamp always comes from timestamps=true.

**`data.docker_image`**

- `image_id` -> `id`: removed; same value as id (ImageInspect.Id)
- `repo_digest` -> `image_digest_ref`: rename; upstream RepoDigests list goes in the structured output

**`data.docker_network`**

- `ipv6` -> `enable_ipv6`

**`data.docker_nodes`**

- `nodes.addr` -> `nodes.status.addr`: rename; flat list field -> upstream path in the nodes objects
- `nodes.availability` -> `nodes.spec.availability`: rename; flat list field -> upstream path in the nodes objects
- `nodes.engine_version` -> `nodes.description.engine.engine_version`: rename; flat list field -> upstream path in the nodes objects
- `nodes.hostname` -> `nodes.description.hostname`: rename; flat list field -> upstream path in the nodes objects
- `nodes.labels` -> `nodes.spec.labels`: rename; flat list field -> upstream path in the nodes objects
- `nodes.leader` -> `nodes.manager_status.leader`: rename; flat list field -> upstream path in the nodes objects
- `nodes.role` -> `nodes.spec.role`: rename; flat list field -> upstream path in the nodes objects
- `nodes.state` -> `nodes.status.state`: rename; flat list field -> upstream path in the nodes objects

**`data.docker_registry_image`**

- `sha256_digest` -> `digest`: rename (image_digest_ref added beside it)

**`data.docker_secrets`**

- `secrets.labels` -> `secrets.spec.labels`: rename; flat list field -> upstream path in the secrets objects
- `secrets.name` -> `secrets.spec.name`: rename; flat list field -> upstream path in the secrets objects

**`data.docker_services`**

- `services.image` -> `services.spec.task_template.container_spec.image`: rename; flat list field -> upstream path in the services objects
- `services.labels` -> `services.spec.labels`: rename; flat list field -> upstream path in the services objects
- `services.mode` -> `services.spec.mode`: string "replicated" | "global" -> the upstream ServiceMode object (spec.mode.replicated / global / replicated_job / global_job; the one set is the mode)
- `services.name` -> `services.spec.name`: rename; flat list field -> upstream path in the services objects
- `services.replicas` -> `services.spec.mode.replicated.replicas`: rename; flat list field -> upstream path in the services objects

#### Actions

**`action.docker_container_export`**

- `container` -> `container_id`: rename (accepts ID or name as before)
- `path` -> `output`

**`action.docker_exec`**

- `container` -> `container_id`: rename (accepts ID or name as before)
- `service` -> `service_id`: rename (accepts ID or name as before)

**`action.docker_image_load`**

- `path` -> `input`

**`action.docker_image_save`**

- `path` -> `output`

**`action.docker_node_activate`**

- `node` -> `node_id`: rename (accepts ID or hostname as before)

**`action.docker_node_drain`**

- `node` -> `node_id`: rename (accepts ID or hostname as before)

**`action.docker_service_force_update`**

- `service` -> `service_id`: rename (accepts ID or name as before)

**`action.docker_service_rollback`**

- `service` -> `service_id`: rename (accepts ID or name as before)

**`action.docker_service_scale`**

- `service` -> `service_id`: rename (accepts ID or name as before)

**`action.docker_system_prune`**

- `all_images` -> `all`

#### Ephemeral resources

**`ephemeral.docker_exec_output`**

- `container` -> `container_id`: rename (accepts ID or name as before)

**`ephemeral.docker_registry_token`**

- `token` -> `token.token`: string -> object {token, expires_in, issued_at}; the bearer token is now token.token (expires_in / issued_at also stay top-level)

### Added

- 80 new types (Pass 5), for 138 registered in total: 17 resources, 54 data sources, 51 actions, 4 ephemeral resources, 8 list resources and 4 provider functions. [README_resources.md](README_resources.md) lists them by layer.
  - Resources: `docker_node`, `docker_registry_manifest_list`.
  - Ephemeral resource: `docker_registry_login`.
  - Data sources:
    - containers: `docker_container`, `docker_container_changes`, `docker_container_file`, `docker_container_stats`, `docker_container_top`;
    - images: `docker_images`, `docker_image_history`, `docker_image_search`, `docker_image_attestations`;
    - objects and system: `docker_networks`, `docker_volumes`, `docker_plugins`, `docker_info`, `docker_version`, `docker_system_df`;
    - swarm: `docker_swarm`, `docker_secret`, `docker_config`, `docker_service`, `docker_service_logs`, `docker_task`, `docker_tasks`, `docker_task_logs`;
    - compose and stacks: `docker_compose_config`, `docker_compose_containers`, `docker_compose_images`, `docker_compose_logs`, `docker_compose_port`, `docker_compose_projects`, `docker_compose_top`, `docker_compose_volumes`, `docker_stack`, `docker_stacks`;
    - Dockerfile: `docker_dockerfile`;
    - buildx: `docker_buildx_builder`, `docker_buildx_builders`, `docker_buildx_history`, `docker_buildx_history_records`, `docker_buildx_disk_usage`.
  - Actions:
    - containers: `docker_container_start` / `_stop` / `_restart` / `_kill` / `_pause` / `_unpause` / `_wait` / `_cp`;
    - images: `docker_image_commit`;
    - prune: `docker_container_prune`, `docker_network_prune`, `docker_volume_prune`, `docker_builder_prune`;
    - plugins: `docker_plugin_create`, `docker_plugin_push`;
    - swarm: `docker_node_promote`, `docker_node_demote`, `docker_swarm_unlock`, `docker_swarm_rotate_unlock_key`, `docker_swarm_rotate_ca`;
    - compose: `docker_compose_build` / `_commit` / `_cp` / `_exec` / `_export` / `_kill` / `_pause` / `_publish` / `_pull` / `_push` / `_restart` / `_run` / `_scale` / `_start` / `_stop` / `_unpause` / `_wait`;
    - buildx: `docker_buildx_bake`.
- Migration from kreuzwerker/docker: the 12 kreuzwerker resource types can move their state into this provider with `moved` blocks (`ResourceWithMoveState`, Terraform >= 1.8). See the [migration guide](docs/guides/migrating-from-kreuzwerker.md).
- The kreuzwerker/docker v4.6.0 gap attributes (143 of them) are implemented, across `docker_container`, `docker_image`, `docker_service`, `docker_buildx_builder` and others. The parity check now maps all 685 kreuzwerker attributes or drops each one with a reason.
- Outputs contract: managed types and data sources expose a raw `*_json` output of the Engine response next to the structured attributes, plus identity and version outputs. Re-encoded outputs are listed in [reference/CONTRACT.md](reference/CONTRACT.md).
- `docker_volume`: Swarm CSI cluster volumes (`cluster_volume_spec`, `cluster_volume_id`; availability changes in place).
- `docker_container`: one `networks` map, connected and disconnected in place, and a `files` block for copying files in.
- `docker_image`: builds can run on named buildx builders, and `build` gains `ssh`, `no_cache_filter`, outputs, attestations and build metadata outputs. `build.additional_contexts` now works (it used to return "not supported yet").
- `docker_swarm_init`: spec updates (orchestration, raft, dispatcher, CA config, labels).
- `docker_secret` / `docker_config`: labels update in place.
- The swarm actions report one result object and take `result_path`, as do image import, load and save.
- Project: the dind acceptance harness gained seeded registry images, a standby worker (worker4) joined in the `-SwarmInit` pass, and a test plugin fixture (no-op volume plugin and CSI driver). `TestAccComposeParity` runs all 13 sub-cases against the docker compose CLI. Every Dockerfile doc example round-trips through `data.docker_dockerfile`, or is allowlisted with a reason (204 round-trip, 9 allowlisted fragments).
- Project: inventory status is computed honestly. A registered type whose documented sub-feature is absent or a no-op carries a `// PARTIAL(pass5)` marker and shows as `partial` in [reference/INVENTORY.md](reference/INVENTORY.md). Ten items are partial: buildx bake / build / builder / history, compose publish / services / models / secrets / configs, and the registry image data source.

### Changed

- `com.docker.compose.config-hash` on `docker_compose` (local mode) is the docker compose CLI's value. The provider's own digest moved to `io.terraform.provider.compose-spec-hash`. Existing containers are not recreated.
- `docker_compose` local mode hashes `service:` and `volumes_from` references after resolving them to container IDs, as docker compose does, and recreates a dependant container when its target container is recreated (also when that happens outside Terraform; refresh shows it as drift). Containers of services with such references that were created before this change are recreated once on the first apply (Pass 5.2).
- `docker_compose` local mode writes the `com.docker.compose.depends_on` label with its entries in sorted order (Pass 5.2).
- `docker_compose` local mode keeps a `volumes_from` `:ro` suffix read-only (docker compose drops it and mounts read-write). This differs from docker compose and is documented in [reference/CONTRACT.md](reference/CONTRACT.md) (Pass 5.2).
- `docker_compose.remove_orphans` defaults to `true`, while the CLI's `--remove-orphans` is opt-in. See "Known differences from upstream defaults" in [reference/CONTRACT.md](reference/CONTRACT.md).
- Removing `docker_network.ipam_config` is now an in-place change.
- `ephemeral.docker_registry_token`: `token` is an object (`token.token`, `expires_in`, `issued_at`).
- `action.docker_swarm_unlock` `unlock_key` and `action.docker_swarm_rotate_ca` `signing_ca_key` are plain required arguments, not write-only (framework v1.19 rejects write-only arguments on actions). They are kept out of progress and result output.
- Raw `*_json` outputs are deterministic. Byte-exact outputs no longer vary by a trailing newline. Outputs the Engine client cannot return raw are re-encoded canonically: sorted keys, original number text, no HTML escaping.

### Removed

- `data.docker_logs` (now `data.docker_container_logs`), `docker_container.capabilities`, `data.docker_container_logs.timestamps`, `data.docker_image.image_id` and the other attributes listed under Breaking.

### Fixed

- `docker_compose` local mode: an `environment` entry with a null value is passed to the Engine as a bare `KEY`, following compose-spec unset semantics (Pass 5.2). Adding or removing only a null entry does not recreate an existing container. Swarm mode still drops null entries.
- `docker_compose` local mode create matches `docker compose up`: `com.docker.compose.image` is the image ID, `depends_on` uses `svc:condition:restart`, the container name is the first alias, and named volumes go through binds.
- `action.docker_compose_scale` starts kept containers that had stopped, as the CLI does.
- `docker_network.ipam_config`: a daemon-assigned pool in migrated state no longer forces replacement.
- `docker_container`: when `must_run` plans a start, the computed outputs are unknown. This fixes an inconsistent result after apply.
- `data.docker_dockerfile`: required arguments inside absent keyword blocks no longer fail validation.
- `action.docker_plugin_create` on Windows: tar entry permissions follow the docker CLI rule (`&= 0755`, `|= 0111`).
- `data.docker_container_changes` and verbose `data.docker_system_df` lists come back in a stable order.

### Security

- docker_swarm and docker_swarm_init inspect_json previously included swarm join tokens in state without being marked sensitive; it now contains cluster info only.
