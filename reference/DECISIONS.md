# Naming decisions

Every open naming question from [INVENTORY.md](INVENTORY.md#naming-questions) (1-51), at HEAD 4f5f1b7. The two
Lane Z carry-overs (Pass 4.5, BUILD_REPORT.md) are already in that list: the names for non-JSON raw streams (Q33)
and the action result shape (Q35, together with Q43). Question numbers match INVENTORY.md.

**Status (Pass 5):** every question below is decided. Each `Decision:` line is the user's ruling, and the inventory follows it. The recommendations stay as the record of what was proposed. From Pass 5 on, kreuzwerker naming is not a naming source (see "General rule" at the end).

Recommendations follow this order of preference: match kreuzwerker/docker v4.6.0 where it has the type or attribute
(migration path), then the upstream name, then consistency with [CONTRACT.md](CONTRACT.md).

Evidence sources:
- **Upstream**: `upstream/engine-api/api/swagger.yaml` (API 1.56) operationIds, `upstream/compose-spec`,
  `upstream/docker-cli/docs/reference/commandline`, `upstream/compose-cli/docs/reference`,
  `upstream/buildx/docs/reference`, `upstream/dockerfile/frontend/dockerfile/docs/reference.md`.
- **kreuzwerker**: github.com/kreuzwerker/terraform-provider-docker `docs/` at tag v4.6.0 (commit 1e48613c), and
  `internal/provider/resource_docker_container.go` at the same commit for one ForceNew flag. "None" means v4.6.0 has
  no such type or attribute.

One fact affects several entries. Terraform 1.14 action schemas have no computed attributes, so an action cannot
return values into configuration or state. This provider reports action results through progress messages
(`SendProgress` in `internal/provider/action_common.go`), and kreuzwerker's six actions return nothing. Names in the
action-output entries (Q33 exec, Q35, Q37, Q40, Q43) therefore set the inventory contract and the progress payload,
not attributes a user can reference.

Item ids are `layer:slug` from [inventory.json](inventory.json).

## Summary

| # | question | recommended |
|---|---|---|
| 1 | Network connect/disconnect: standalone resource or in-place `networks_advanced`? | B: in place on docker_container |
| 2 | Build cache prune action name | A: `docker_builder_prune` (decide with Q23) |
| 3 | Standalone `docker_image_build` action? | B: no, builds stay in docker_image `build` |
| 4 | Manage an already-joined node: adopt mode or `docker_node` resource? | B: `docker_node` resource |
| 5 | Node role actions: promote/demote or `docker_node_role`? | A: `docker_node_promote` / `docker_node_demote` |
| 6 | Service/task logs: separate types or extend docker_logs? | A: `docker_service_logs` + `docker_task_logs` |
| 7 | SystemAuth identity token: ephemeral, data source or skip? | A: ephemeral `docker_registry_login` |
| 8 | PluginCreate: action or resource? | A: action `docker_plugin_create` |
| 9 | Container file read / copy-in names | A: data `docker_container_file` + action `docker_container_upload` |
| 10 | Swarm rotation actions: separate or one `docker_swarm_rotate`? | B: separate, uniform `docker_swarm_rotate_*` prefix |
| 11 | Compose up/down actions beside docker_compose? | C: skip up/down |
| 12 | `compose config` render name | A: data `docker_compose_config` |
| 13 | `compose ps` / `compose ls` names | B: `docker_compose_containers` + `docker_compose_projects` |
| 14 | `compose volumes`: own data source or label filter docs? | A: data `docker_compose_volumes` |
| 15 | Compose per-container verbs: compose actions or container actions with a selector? | A: separate `docker_compose_*` actions |
| 16 | `compose scale`: new action or extend docker_service_scale? | A: new `docker_compose_scale` |
| 17 | Compose top-level elements: inside docker_compose or split? | A: keep inside docker_compose |
| 18 | Compose `jobs` support | B: warn, no jobs support yet; `docker_compose_run` stays |
| 19 | Manifest list resource name | A: `docker_registry_manifest_list` |
| 20 | BuildKit build: extend docker_image or add `docker_buildx_build`? | A: extend docker_image + action `docker_buildx_bake` |
| 21 | Stack: keep `docker_stack`; stack data source name | A: keep `docker_stack`; data `docker_stacks` + `docker_stack` |
| 22 | `docker_context` data source or skip? | A: skip |
| 23 | One build-cache prune action for Engine and buildx builders: name | C: `docker_builder_prune` with optional `builder` |
| 24 | Buildx builder and history data source names | A: builder pair + two history data sources |
| 25 | `docker buildx stop` action or skip? | B: skip |
| 26 | Buildx disk usage: separate or fold into system df? | A: separate `docker_buildx_disk_usage` |
| 27 | Dockerfile shape: per-instruction types, one data source, or function? | B: one `data.docker_dockerfile` with nested blocks |
| 28 | Dockerfile directives: types or top-level attributes? | B: top-level `syntax`, `escape`, `check` |
| 29 | MAINTAINER (deprecated) | C: parse it, no input block |
| 30 | ONBUILD: nesting block or attribute? | B: `onbuild = true` attribute |
| 31 | Stages: `stage` block with `name`? | A: `stage` block with `name` |
| 32 | `check` rule names: pinned list or free strings? | C: warn on names outside the pinned list |
| 33 | Raw names for non-JSON streams (exec, build log, file content) | B: `stdout` + `stderr`, `build_log`, `content_base64` |
| 34 | Hash names when an item has two raw outputs | A: `<raw>_sha256` |
| 35 | Action result: one `result` object or flat fields? | A: one `result` object |
| 36 | List data sources: `ids` / `names` lists too? | A: keep `ids` / `names` beside the structured list |
| 37 | Action-written files: `archive_sha256` as identity or hash exception? | A: identity, no exception |
| 38 | docker_swarm_init `cluster_id` vs contract `id` | A: keep `cluster_id`, add `id` |
| 39 | Plural data sources: type-named list or uniform `items`? | A: named after the type |
| 40 | Action target identity: `node_id` / `service_id` or `id`? | A: `node_id` / `service_id` |
| 41 | docker_compose project name: `name`, rename, or keep `project_name`? | C: keep `project_name` |
| 42 | docker_compose facets: one `project` or per-slice attributes? | A: one `project` |
| 43 | Compose exec/run result shape | A: `result` object (same as Q35) |
| 44 | `repo_digest` vs contract `image_digest_ref` | B: add `image_digest_ref`, keep `repo_digest` (deprecated, never removed) |
| 45 | `sha256_digest` vs contract `digest` / `image_digest_ref` | B: add both, keep `sha256_digest` (deprecated, never removed) |
| 46 | `image_id` duplicates `id` | A: keep both |
| 47 | data.docker_dockerfile: also expose the source text? | A: add `source_dockerfile` + `source_dockerfile_sha256` |
| 48 | Structured output name per Dockerfile instruction | A: lowercase keyword |
| 49 | Image-mode provenance: `reconstructed` + `fidelity` or `fidelity` only? | B: `fidelity` only |
| 50 | Stack data source: keep `tasks`? | B: drop `tasks`; use docker_tasks |
| 51 | docker_stacks service count: `services` or `services_count`? | A: `services` |

## Engine (image + single host)

### Q1. Network attachment: standalone resource or in-place `networks_advanced`?

- **Items:** engine:network-attachment (`docker_network_attachment`), engine:container (`docker_container`)
- **Options:**
  - A. Add a standalone `docker_network_attachment` resource (one container x network pair).
  - B. Make docker_container `networks_advanced` connect and disconnect in place instead of forcing replacement; no new type.
  - C. Both, with a warning when one pair is managed in both places.
- **Upstream:** swagger `NetworkConnect` / `NetworkDisconnect` (POST /networks/{id}/connect, /disconnect); CLI `docker network connect` / `docker network disconnect`.
- **kreuzwerker v4.6.0:** No attachment type. docker_container `networks_advanced` (block set) is `ForceNew: true` in resource_docker_container.go. This provider uses the same attribute name.
- **Recommendation:** B. It keeps the kreuzwerker attribute, stops needless container replacement, and avoids two resources fighting over one pair.
- **Decision:** In-place network connect/disconnect on docker_container (NetworkConnect / NetworkDisconnect, no replacement). `networks_advanced` is renamed `networks`: a map keyed by network name of endpoint settings mirroring `NetworkingConfig.EndpointsConfig` (`aliases`, `ipv4_address`, `ipv6_address`, `mac_address`, `driver_opts`, `gw_priority`, ...). No standalone attachment type: engine:network-attachment becomes a skip. (Pass 5, decided by the user.)

### Q2. Build cache prune action name

- **Items:** engine:build-prune (`docker_builder_prune`); see also buildx:prune (Q23)
- **Options:**
  - A. `docker_builder_prune`
  - B. `docker_build_cache_prune`
- **Upstream:** swagger `BuildPrune` (POST /build/prune); CLI `docker builder prune`.
- **kreuzwerker v4.6.0:** None. The `docker_system_prune` action covers build cache in bulk.
- **Recommendation:** A. It matches the CLI command and the `docker_<object>_prune` family. Decide together with Q23.
- **Decision:** One action, `docker_builder_prune` (decided with Q23), with an optional `builder`: unset prunes the Engine build cache (BuildPrune); set prunes that buildx builder. (Pass 5, decided by the user.)

### Q3. Standalone image build action?

- **Items:** engine:image-build (`docker_image_build`), engine:image (`docker_image`)
- **Options:**
  - A. Add action `docker_image_build`.
  - B. Keep builds only in docker_image's `build` block.
  - C. Add it under a less confusable name (e.g. `docker_build`).
- **Upstream:** swagger `ImageBuild` (POST /build); CLI `docker image build` / `docker build`.
- **kreuzwerker v4.6.0:** No build action. Builds live in the docker_image (and docker_registry_image) `build` block.
- **Recommendation:** B. It matches kreuzwerker. Builds with no local image output belong in `docker_buildx_bake` (Q20).
- **Decision:** No standalone build action. Builds live in docker_image's `build` block; engine:image-build becomes a skip pointing there. (Pass 5, decided by the user.)

### Q7. SystemAuth identity token

- **Items:** engine:registry-auth (`docker_registry_auth`); related cli-only:login (skipped)
- **Options:**
  - A. Ephemeral resource `docker_registry_login`.
  - B. Data source `docker_registry_auth` (the inventory's current name).
  - C. Skip.
- **Upstream:** swagger `SystemAuth` (POST /auth, response `IdentityToken`); CLI `docker login`.
- **kreuzwerker v4.6.0:** None. It has the provider `registry_auth` block and per-resource `auth_config` blocks.
- **Recommendation:** A. The token is a credential and an ephemeral resource keeps it out of state. "login" matches the CLI and stays clear of `registry_auth` and `docker_registry_token`.
- **Decision:** Ephemeral resource `docker_registry_login` (SystemAuth `IdentityToken`; never stored in state). (Pass 5, decided by the user.)

### Q8. PluginCreate: action or resource?

- **Items:** engine:plugin-create (`docker_plugin_create`), engine:plugin (`docker_plugin`)
- **Options:**
  - A. Action `docker_plugin_create` (one-shot).
  - B. A resource under another name (e.g. `docker_plugin_local`), using PluginDelete on destroy.
  - C. A `rootfs`/`config` source mode on docker_plugin.
- **Upstream:** swagger `PluginCreate` (POST /plugins/create) and `PluginDelete`; CLI `docker plugin create`.
- **kreuzwerker v4.6.0:** docker_plugin installs from a registry (`name`, `plugin_reference`). It has no create.
- **Recommendation:** A. It mirrors the CLI one-to-one and leaves kreuzwerker's docker_plugin meaning unchanged.
- **Decision:** Action `docker_plugin_create`. (Pass 5, decided by the user.)

### Q9. Container file read and copy-in names

- **Items:** engine:container-file (`docker_container_file`), engine:container-upload (`docker_container_upload`)
- **Options:**
  - A. Data source `docker_container_file` (read) + action `docker_container_upload` (write).
  - B. Data source `docker_container_file` + action `docker_container_cp`.
  - C. One action `docker_container_cp` for both directions.
- **Upstream:** swagger `ContainerArchive` + `ContainerArchiveInfo` (GET/HEAD /containers/{id}/archive), `PutContainerArchive` (PUT); CLI `docker container cp`.
- **kreuzwerker v4.6.0:** docker_container `upload` block (`content`, `content_base64`, `source`). It has no read.
- **Recommendation:** A. "upload" is kreuzwerker's word for copy-in, and a read belongs in a data source.
- **Decision:** data `docker_container_file` (ContainerArchive + ContainerArchiveInfo) + action `docker_container_cp` (PutContainerArchive; host path or inline content -> container path). The copy-in block on docker_container becomes `files { path, content | content_base64 | source, mode }`. (Pass 5, decided by the user.)

### Q34. Hash names when an item has two raw outputs

- **Items:** engine:container-file (`stat_json`, `content_base64`); also any item that gains a second raw output (Q33 exec, Q47)
- **Options:**
  - A. `<raw>_sha256` (`stat_json_sha256`, `content_base64_sha256`), per CONTRACT.md.
  - B. Keep `content_sha256` for the file content and `stat_json_sha256` for the stat.
- **Upstream:** None. Archive stat is the `X-Docker-Container-Path-Stat` header of `ContainerArchiveInfo`, and content is the tar body of `ContainerArchive`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. It is the contract's existing rule, so one rule covers every two-raw item.
- **Decision:** Two raw outputs: `<raw>_sha256` each. One raw output: `content_sha256` (the current rule). (Pass 5, decided by the user.)

### Q37. Files written by actions: `archive_sha256` as identity, or a hash exception?

- **Items:** engine:container-export, engine:image-save, engine:container-upload (`archive_sha256`); compose:cli-export
- **Options:**
  - A. Keep `archive_sha256` as identity.
  - B. Add a hash-category exception for files not held as a raw output.
- **Upstream:** None. `ContainerExport` and `ImageGet` stream a tar with no digest header.
- **kreuzwerker v4.6.0:** None. Its container_export / image_save actions take `output` and return nothing.
- **Recommendation:** A. The hash category means "sha256 of a raw output", and the file is not one. An exception would weaken V2/merge.py checks for one case.
- **Decision:** `archive_sha256` stays identity. No hash exception. (Pass 5, decided by the user.)

### Q44. `repo_digest` vs contract `image_digest_ref`

- **Items:** engine:image (`docker_image`), engine:image-data (`data.docker_image`); also compose:service-build, buildx:build (share docker_image)
- **Options:**
  - A. Rename `repo_digest` to `image_digest_ref` (breaking).
  - B. Add `image_digest_ref`; keep `repo_digest`, deprecated but never removed.
  - C. Keep `repo_digest` as the contract name.
- **Upstream:** swagger `ImageInspect` → `RepoDigests` (list of `repo@sha256:…`).
- **kreuzwerker v4.6.0:** `repo_digest` on resource and data docker_image, documented as `repo[:tag]@sha256:<hash>`.
- **Recommendation:** B. kreuzwerker configs keep working, and the contract's handoff name stays uniform across the five image types.
- **Decision:** Rename `repo_digest` -> `image_digest_ref` on every image type. Expose the upstream `repo_digests` list in the structured output. No alias. (Pass 5, decided by the user.)

### Q45. `sha256_digest` vs contract `digest` / `image_digest_ref`

- **Items:** engine:registry-image-data (`data.docker_registry_image`); engine:registry-image (`docker_registry_image`, which also has `sha256_digest` today)
- **Options:**
  - A. Rename to `digest` + `image_digest_ref` (breaking).
  - B. Add `digest` + `image_digest_ref`; keep `sha256_digest`, deprecated but never removed.
  - C. Keep the current names.
- **Upstream:** swagger `DistributionInspect` → `Descriptor.digest`.
- **kreuzwerker v4.6.0:** `sha256_digest` on data and resource docker_registry_image, and in `docker_registry_image_manifests.manifests[*]`. Its documented example is `pull_triggers = [data.docker_registry_image.x.sha256_digest]`.
- **Recommendation:** B. That kreuzwerker example pattern is in many configs, and the contract names come in alongside it.
- **Decision:** Rename `sha256_digest` -> `digest` (+ `image_digest_ref`) on the registry image types and in `manifests[*]`. No alias. (Pass 5, decided by the user.)

### Q46. `image_id` duplicates `id`

- **Items:** engine:image, engine:image-data; engine:container / engine:container-data also expose `image_id` (a different meaning: the container's image)
- **Options:**
  - A. Keep both.
  - B. Deprecate `image_id`.
- **Upstream:** swagger `ImageInspect` → `Id`.
- **kreuzwerker v4.6.0:** On resource docker_image, `id` is documented as *not* the image ID ("use the `image_id` attribute"). `image_id` is the reference everyone uses (e.g. `docker_container.image`).
- **Recommendation:** A. Deprecating `image_id` would warn on most kreuzwerker configs.
- **Decision:** docker_image and data docker_image: `id` = the image ID (`ImageInspect.Id`); `image_id` is removed from those two. Containers keep `image_id` (the container's image). (Pass 5, decided by the user.)

## Swarm

### Q4. Managing an already-joined node

- **Items:** swarm:swarm-node (`docker_swarm_node`); swarm:node-data (`data.docker_node`)
- **Options:**
  - A. An adopt mode on docker_swarm_node (no join; NodeUpdate only).
  - B. A separate `docker_node` resource (labels / availability / role).
- **Upstream:** swagger `NodeUpdate` (POST /nodes/{id}/update); CLI `docker node update`.
- **kreuzwerker v4.6.0:** None (no swarm node types).
- **Recommendation:** B. It is the CLI noun, pairs with the existing data.docker_node, and docker_swarm_node keeps one meaning (join).
- **Decision:** New resource `docker_node`: labels, availability and role on an already-joined node (NodeUpdate). `docker_swarm_node` keeps the meaning "join". (Pass 5, decided by the user.)

### Q5. Node role actions

- **Items:** swarm:node-promote (`docker_node_promote`), swarm:node-demote (`docker_node_demote`)
- **Options:**
  - A. `docker_node_promote` / `docker_node_demote`.
  - B. One `docker_node_role` with `role = manager|worker`.
- **Upstream:** swagger `NodeUpdate` (`Spec.Role`); CLI `docker node promote` / `docker node demote`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. These are the CLI verbs and sit beside the existing `docker_node_drain` / `docker_node_activate`.
- **Decision:** `docker_node_promote` / `docker_node_demote` actions. (Pass 5, decided by the user.)

### Q6. Service and task logs

- **Items:** swarm:service-logs (`docker_service_logs`), swarm:task-logs (`docker_task_logs`), engine:container-logs (`docker_logs`)
- **Options:**
  - A. Separate `docker_service_logs` and `docker_task_logs` data sources.
  - B. Extend data.docker_logs with `service_id` / `task_id`.
  - C. One `docker_service_logs` that also takes a task ID (as the CLI does).
- **Upstream:** swagger `ServiceLogs`, `TaskLogs`, `ContainerLogs` (three operations); CLI `docker service logs` (service or task), `docker container logs`.
- **kreuzwerker v4.6.0:** data.docker_logs is container-only, with a required `name` and output `logs_list_string`.
- **Recommendation:** A. docker_logs keeps kreuzwerker's required `name`, each type maps to one operationId, and CONTRACT.md already uses these two names.
- **Decision:** data `docker_service_logs` + data `docker_task_logs`. data `docker_logs` is renamed `docker_container_logs`, with input `container` (ID or name). All three follow the logs contract (`logs`, `log_entries`, `tail` / `since`). (Pass 5, decided by the user.)

### Q10. Swarm rotation actions

- **Items:** swarm:swarm-rotate-unlock-key (`docker_swarm_rotate_unlock_key`), swarm:swarm-ca-rotate (`docker_swarm_ca_rotate`), swarm:swarm-rotate-tokens (`docker_swarm_rotate_tokens`, exists)
- **Options:**
  - A. Separate actions as proposed (`docker_swarm_rotate_unlock_key`, `docker_swarm_ca_rotate`).
  - B. Separate actions with one prefix: `docker_swarm_rotate_unlock_key`, `docker_swarm_rotate_ca`.
  - C. One `docker_swarm_rotate` with `what = worker_token|manager_token|unlock_key|ca`, absorbing rotate_tokens.
- **Upstream:** swagger `SwarmUpdate` (query `rotateWorkerToken`, `rotateManagerToken`, `rotateManagerUnlockKey`; CA via `Spec.CAConfig.ForceRotate`); CLI `docker swarm join-token --rotate`, `docker swarm unlock-key --rotate`, `docker swarm ca --rotate`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** B. Each action maps to one CLI command, the existing `docker_swarm_rotate_tokens` is unchanged, and the names sort together.
- **Decision:** Separate actions `docker_swarm_rotate_unlock_key` and `docker_swarm_rotate_ca`; `docker_swarm_rotate_tokens` unchanged. (Pass 5, decided by the user.)

### Q38. docker_swarm_init `cluster_id` vs contract `id`

- **Items:** swarm:swarm-init (`docker_swarm_init`)
- **Options:**
  - A. Keep `cluster_id` and expose `id` (= ClusterInfo.ID) as the contract identity.
  - B. Drop `cluster_id` (breaking).
- **Upstream:** swagger `SwarmInspect` → `ClusterInfo.ID`.
- **kreuzwerker v4.6.0:** None (no swarm init type).
- **Recommendation:** A. Existing users keep `cluster_id`, and the contract needs only `id`.
- **Decision:** docker_swarm_init: `id` = `ClusterInfo.ID` (contract identity); keep `cluster_id`. (Pass 5, decided by the user.)

### Q40. Action target identity

- **Items:** swarm:node-activate, swarm:node-demote, swarm:node-drain, swarm:node-promote, swarm:swarm-unlock (`node_id`); swarm:service-force-update, swarm:service-rollback, swarm:service-scale (`service_id`)
- **Options:**
  - A. `node_id` / `service_id`.
  - B. Plain `id`, like resources.
- **Upstream:** swagger Task fields `NodeID` / `ServiceID`; `NodeUpdate` / `ServiceUpdate` path param `{id}`.
- **kreuzwerker v4.6.0:** Its actions take the target as an input named for the object (`container`) and return nothing.
- **Recommendation:** A. An action is not an object with its own id, and data.docker_task already uses `node_id` / `service_id`.
- **Decision:** Action targets: `node_id` / `service_id` / `container_id` (object-named inputs). (Pass 5, decided by the user.)

## Compose

### Q11. Compose up/down actions beside docker_compose

- **Items:** compose:cli-up (`docker_compose_up`), compose:cli-down (`docker_compose_down`)
- **Options:**
  - A. Actions `docker_compose_up` / `docker_compose_down`.
  - B. Actions `docker_compose_apply` / `docker_compose_destroy`.
  - C. Skip both; apply and destroy of docker_compose are up and down.
- **Upstream:** CLI `docker compose up`, `docker compose down`.
- **kreuzwerker v4.6.0:** docker_compose resource (create = up, destroy = down, `remove_orphans`, `wait`). It has no compose actions.
- **Recommendation:** C. It matches kreuzwerker, and an up action beside the resource would fight the resource's state.
- **Decision:** No up/down actions. docker_compose apply / destroy are up / down; compose:cli-up and compose:cli-down become skips. (Pass 5, decided by the user.)

### Q12. `compose config` render

- **Items:** compose:cli-config (`docker_compose_config`)
- **Options:**
  - A. Data source `docker_compose_config`.
  - B. Data source `docker_compose_project` (or `docker_compose_model`).
  - C. Provider function `compose_render`.
- **Upstream:** CLI `docker compose config`; compose-spec top-level `configs` (#/properties/configs) is the confusable name.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. It is the CLI command, and CONTRACT.md's Handoff table already names `data.docker_compose_config`.
- **Decision:** data `docker_compose_config`. (Pass 5, decided by the user.)

### Q13. `compose ps` / `compose ls`

- **Items:** compose:cli-ps (`docker_compose_containers`), compose:cli-ls (`docker_compose_projects`)
- **Options:**
  - A. `docker_compose_ps` + `docker_compose_ls`.
  - B. `docker_compose_containers` + `docker_compose_projects`.
- **Upstream:** CLI `docker compose ps`, `docker compose ls`.
- **kreuzwerker v4.6.0:** Plural nouns for lists: data.docker_containers (not docker_ps).
- **Recommendation:** B. It follows kreuzwerker's noun convention.
- **Decision:** data `docker_compose_containers` + data `docker_compose_projects` (plural-noun rule for list data sources, the same as docker_containers / docker_services). (Pass 5, decided by the user.)

### Q14. `compose volumes`

- **Items:** compose:cli-volumes (`docker_compose_volumes`)
- **Options:**
  - A. Data source `docker_compose_volumes`.
  - B. No new type; document data.docker_volumes / list.docker_volume filtered by label `com.docker.compose.project`.
- **Upstream:** CLI `docker compose volumes`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. It is the upstream command, and the name follows Q13's plural nouns.
- **Decision:** data `docker_compose_volumes`. (Pass 5, decided by the user.)

### Q15. Compose per-container verbs

- **Items:** compose:cli-start, cli-stop, cli-restart, cli-kill, cli-pause, cli-unpause (`docker_compose_*`); engine:container-start, -stop, -restart, -kill, -pause, -unpause (`docker_container_*`)
- **Options:**
  - A. Separate `docker_compose_*` actions.
  - B. Only `docker_container_*` actions, with an optional project/service selector.
  - C. Both, sharing an implementation.
- **Upstream:** CLI `docker compose start|stop|restart|kill|pause|unpause`; swagger `ContainerStart`, `ContainerStop`, `ContainerRestart`, `ContainerKill`, `ContainerPause`, `ContainerUnpause`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. Each type maps to one CLI command (the docker-cli-map stays 1:1), and a selector would give container actions two targeting modes.
- **Decision:** Separate actions `docker_compose_start` / `_stop` / `_restart` / `_kill` / `_pause` / `_unpause`, plus `docker_container_start` / `_stop` / `_restart` / `_kill` / `_pause` / `_unpause`, sharing one implementation. (Pass 5, decided by the user.)

### Q16. `compose scale`

- **Items:** compose:cli-scale (`docker_compose_scale`); swarm:service-scale (`docker_service_scale`)
- **Options:**
  - A. New action `docker_compose_scale`.
  - B. Extend docker_service_scale to local compose projects.
- **Upstream:** CLI `docker compose scale` (creates/removes containers); `docker service scale` → swagger `ServiceUpdate` (replicas).
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. The CLI has two commands with different mechanisms, and docker_service_scale stays swarm-only.
- **Decision:** Action `docker_compose_scale`. `docker_service_scale` stays swarm-only. (Pass 5, decided by the user.)

### Q17. Compose top-level elements

- **Items:** compose:services, compose:networks, compose:volumes, compose:configs, compose:secrets, compose:models, compose:include, compose:name (all `docker_compose`)
- **Options:**
  - A. Keep all inside docker_compose.
  - B. Split into docker_compose_service / _network / _volume / ...
- **Upstream:** compose-spec top-level keys `services`, `networks`, `volumes`, `configs`, `secrets`, `models`, `include`, `name` of one project document.
- **kreuzwerker v4.6.0:** One docker_compose resource over `config_paths`.
- **Recommendation:** A. It matches kreuzwerker, and split names would collide with docker_service / docker_network / docker_volume.
- **Decision:** Compose top-level elements stay inside docker_compose. (Pass 5, decided by the user.)

### Q18. Compose `jobs`

- **Items:** compose:jobs (`docker_compose`); compose:cli-run (`docker_compose_run`)
- **Options:**
  - A. Support jobs in docker_compose (run once on apply) plus `docker_compose_run` for manual triggers.
  - B. No jobs support yet: warn when a project has `jobs`; `docker_compose_run` still ships for one-off runs.
  - C. Ignore `jobs` silently.
- **Upstream:** compose-spec `#/properties/jobs`, `#/$defs/job`, `#/$defs/schedule` (jobs.md); CLI `docker compose run`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** B. Run-on-apply semantics are a design decision, not a naming one; a warning is honest until that is made.
- **Decision:** Jobs: a plan-time warning when a project defines `jobs`. `docker_compose_run` ships for one-off runs. Run-on-apply is a later design pass. (Pass 5, decided by the user.)

### Q41. docker_compose project name

- **Items:** compose:name (`docker_compose`)
- **Options:**
  - A. Add a `name` alias.
  - B. Rename `project_name` to `name` (breaking).
  - C. Keep `project_name` (and `id`) and record it as the compose identity name in CONTRACT.md.
- **Upstream:** compose-spec top-level `name`; CLI `--project-name` / `-p`.
- **kreuzwerker v4.6.0:** docker_compose `project_name` (optional) and `id` = project name.
- **Recommendation:** C. It matches kreuzwerker exactly and the CLI flag. An alias would add a third name for one value.
- **Decision:** docker_compose: rename `project_name` -> `name` (compose-spec top-level `name`). `id` = `name`. (Pass 5, decided by the user.)

### Q42. docker_compose facets

- **Items:** compose:services, networks, volumes, configs, secrets, models, include, jobs, name (`project`)
- **Options:**
  - A. One `project` attribute (the whole normalized project).
  - B. Per-slice attributes (`project_services`, `project_networks`, ...).
- **Upstream:** compose-spec models one project document; `docker compose config` prints it whole.
- **kreuzwerker v4.6.0:** None (only `id`).
- **Recommendation:** A. It mirrors the spec's one document and CONTRACT.md's Handoff (`project`), and it does not clash with today's computed `services` / `networks` / `volumes` ID maps.
- **Decision:** One `project` structured output for docker_compose. (Pass 5, decided by the user.)

### Q43. Compose exec/run result shape

- **Items:** compose:cli-exec (`docker_compose_exec`), compose:cli-run (`docker_compose_run`)
- **Options:**
  - A. `result` object `{exit_code, stdout, stderr}`.
  - B. Flat `exit_code` / `stdout` / `stderr`, like ephemeral docker_exec_output.
- **Upstream:** swagger `ExecInspect` → `ExitCode`; `ContainerWait` → `StatusCode`. stdout and stderr are the two multiplexed streams.
- **kreuzwerker v4.6.0:** Its docker_exec action returns nothing.
- **Recommendation:** A, decided with Q35. Every action reports one `result`. The flat names stay on the ephemeral docker_exec_output, which can return values.
- **Decision:** Compose exec / run: the result object `{exit_code, stdout, stderr}` (same as Q35). (Pass 5, decided by the user.)

## Dockerfile

### Q27. Dockerfile shape

- **Items:** dockerfile:data-dockerfile (`docker_dockerfile`) and the 21 dockerfile_block items (dockerfile:ADD ... dockerfile:WORKDIR, dockerfile:directive-*)
- **Options:**
  - A. One type per instruction (`docker_dockerfile_run`, `docker_dockerfile_from`, ...).
  - B. One `data.docker_dockerfile` with ordered nested blocks (`stage { from, run, copy ... }`) rendering Dockerfile text.
  - C. A provider function.
- **Upstream:** Dockerfile reference (instructions in order, grouped in stages by FROM).
- **kreuzwerker v4.6.0:** None (only `build.dockerfile`, a file name).
- **Recommendation:** B. Order and stages fit nested blocks, and 21 trivial types would bloat the registry.
- **Decision:** One data `docker_dockerfile` with ordered nested blocks that renders Dockerfile text. (Pass 5, decided by the user.)

### Q28. Dockerfile directives

- **Items:** dockerfile:directive-syntax, dockerfile:directive-escape, dockerfile:directive-check
- **Options:**
  - A. Types `docker_dockerfile_directive_syntax` / `_escape` / `_check`.
  - B. Top-level attributes on data.docker_dockerfile: `syntax`, `escape`, `check { skip = [...], error = bool }`.
- **Upstream:** Dockerfile reference parser directives `# syntax=`, `# escape=`, `# check=skip=...;error=...`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** B. The directive names are used as-is, and it follows Q27 B.
- **Decision:** Top-level attributes `syntax`, `escape`, `check { skip = [...], error = bool }`. (Pass 5, decided by the user.)

### Q29. MAINTAINER (deprecated)

- **Items:** dockerfile:MAINTAINER (`docker_dockerfile_maintainer`)
- **Options:**
  - A. Include an input block.
  - B. Omit it entirely.
  - C. Parse it when reading existing Dockerfile text (and image history), but offer no input block; steer to `LABEL org.opencontainers.image.authors`.
- **Upstream:** Dockerfile reference "MAINTAINER (deprecated)", which recommends LABEL instead.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** C. Old Dockerfiles still read correctly, and new configs cannot write a deprecated instruction.
- **Decision:** MAINTAINER is parsed when reading text or image history; there is no input block. Docs point to `LABEL org.opencontainers.image.authors`. (Pass 5, decided by the user.)

### Q30. ONBUILD

- **Items:** dockerfile:ONBUILD (`docker_dockerfile_onbuild`)
- **Options:**
  - A. An `onbuild` block nesting one instruction.
  - B. An `onbuild = true` attribute on each instruction block.
- **Upstream:** Dockerfile reference `ONBUILD <INSTRUCTION>`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** B. Each instruction keeps one block type, and there is no nesting depth to validate.
- **Decision:** `onbuild = true` attribute on each instruction block. (Pass 5, decided by the user.)

### Q31. Stages

- **Items:** dockerfile:FROM (`docker_dockerfile_from`), dockerfile:data-dockerfile
- **Options:**
  - A. A `stage` block with `name` (FROM ... AS name).
  - B. An `as` flag on FROM.
- **Upstream:** Dockerfile reference `FROM [--platform=<platform>] <image> [AS <name>]`, with "build stage" as the term.
- **kreuzwerker v4.6.0:** None (only `build.target`).
- **Recommendation:** A. A stage owns its instructions, which a flag cannot express.
- **Decision:** `stage { name, from, platform, ... instruction blocks }` (`FROM ... AS name`). (Pass 5, decided by the user.)

### Q32. `check` rule names

- **Items:** dockerfile:directive-check
- **Options:**
  - A. Validate against the pinned list (21 rules at frontend 1.28); error on others.
  - B. Accept free strings.
  - C. Accept any string; warn when it is not in the pinned list.
- **Upstream:** `upstream/dockerfile/frontend/dockerfile/docs/rules/` (frontend 1.28).
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** C. A newer frontend chosen with `# syntax=` can add rules, which a hard error would block.
- **Decision:** `check` rule names: warn on names outside the pinned list, don't error. (Pass 5, decided by the user.)

### Q47. Source text as a second raw output

- **Items:** dockerfile:data-dockerfile (`docker_dockerfile`)
- **Options:**
  - A. Add `source_dockerfile` + `source_dockerfile_sha256` beside the rendered `dockerfile`.
  - B. Rendered form only.
- **Upstream:** None (the provider renders the text).
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. In git/URL mode the user does not hold the original text. Once there are two raw outputs, Q34's `<raw>_sha256` rule applies.
- **Decision:** data docker_dockerfile also exposes `source_dockerfile` + `source_dockerfile_sha256`. (Pass 5, decided by the user.)

### Q48. Structured output name per instruction

- **Items:** dockerfile:RUN (`run`), dockerfile:COPY (`copy`), dockerfile:FROM (`from`) and the other dockerfile_block items
- **Options:**
  - A. The lowercase keyword (`run`, `copy`, `from`).
  - B. One uniform name (`block`).
- **Upstream:** Dockerfile reference instruction keywords.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. It is the upstream keyword, and under Q27 B these become the nested block names anyway.
- **Decision:** Per-instruction structured output names: the lowercase keyword (`run`, `copy`, `env`, ...). (Pass 5, decided by the user.)

### Q49. Image-mode provenance outputs

- **Items:** dockerfile:data-dockerfile (`reconstructed`, `fidelity`)
- **Options:**
  - A. Both `reconstructed` (bool) and `fidelity`.
  - B. Only `fidelity` (`exact` / `approximate`).
- **Upstream:** swagger `ImageHistory` → `CreatedBy` (the source of reconstruction).
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** B. `fidelity` already implies `reconstructed`; one value cannot contradict itself.
- **Decision:** Image-mode provenance: `fidelity` only (`"exact"` | `"approximate"`). `reconstructed` is dropped. (Pass 5, decided by the user.)

## Buildx

### Q20. BuildKit build

- **Items:** buildx:build (`docker_image`), buildx:bake (`docker_buildx_bake`)
- **Options:**
  - A. Extend docker_image's `build` block; add action `docker_buildx_bake` for multi-target / non-image outputs.
  - B. Add a `docker_buildx_build` resource.
- **Upstream:** CLI `docker buildx build`, `docker buildx bake`; swagger `ImageBuild` + `Session`.
- **kreuzwerker v4.6.0:** docker_image `build` block already has buildx fields (`builder`, `cache_from`, `cache_to`, `provenance`, `additional_contexts`, `build_log_file`).
- **Recommendation:** A. It matches kreuzwerker, where buildx builds go through docker_image.
- **Decision:** Extend docker_image's `build` block for BuildKit, plus action `docker_buildx_bake`. (Pass 5, decided by the user.)

### Q23. One build-cache prune action for Engine and buildx builders

- **Items:** buildx:prune (`docker_buildx_prune`), engine:build-prune (`docker_builder_prune`)
- **Options:**
  - A. `docker_buildx_prune`.
  - B. `docker_build_cache_prune`.
  - C. `docker_builder_prune` with an optional `builder` (POST /build/prune when unset, BuildKit gRPC when set).
- **Upstream:** swagger `BuildPrune`; CLI `docker builder prune` and `docker buildx prune --builder`.
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** C. One type, the Engine CLI name from Q2, and `builder` mirrors the buildx flag.
- **Decision:** Same as Q2: one `docker_builder_prune` with an optional `builder`; buildx:prune folds into it (becomes a skip pointing at engine:build-prune). (Pass 5, decided by the user.)

### Q24. Buildx builder and history data sources

- **Items:** buildx:builder-inspect (`docker_buildx_builder`), buildx:builders (`docker_buildx_builders`), buildx:history-ls (`docker_buildx_history`), buildx:history-inspect (`docker_buildx_history_record`)
- **Options:**
  - A. `docker_buildx_builder` + `docker_buildx_builders`; history as `docker_buildx_history` (list) + `docker_buildx_history_record` (one).
  - B. The same builder pair; one `docker_buildx_history` with an optional `ref`.
- **Upstream:** CLI `docker buildx inspect`, `docker buildx ls`, `docker buildx history ls`, `docker buildx history inspect`.
- **kreuzwerker v4.6.0:** Resource `docker_buildx_builder` (no data sources). A resource/data-source pair with one name is its usual pattern (docker_image, docker_network, docker_plugin).
- **Recommendation:** A. There is one type per CLI command, and an optional argument would not change the output shape.
- **Decision:** data `docker_buildx_builder` + data `docker_buildx_builders`; data `docker_buildx_history` + data `docker_buildx_history_records`. Applied with the plural-noun rule (Q13): `docker buildx history ls` -> `docker_buildx_history_records` (list), `docker buildx history inspect` -> `docker_buildx_history` (one record). (Pass 5, decided by the user.)

### Q25. `docker buildx stop`

- **Items:** buildx:stop (`docker_buildx_builder_stop`)
- **Options:**
  - A. Action `docker_buildx_builder_stop`.
  - B. Skip.
- **Upstream:** CLI `docker buildx stop`.
- **kreuzwerker v4.6.0:** None. docker_buildx_builder has `bootstrap` only.
- **Recommendation:** B. It is low value, and a later docker_container_stop on the builder container covers it.
- **Decision:** Skip `docker buildx stop`. (Pass 5, decided by the user.)

### Q26. Buildx disk usage

- **Items:** buildx:du (`docker_buildx_disk_usage`), engine:system-df (`docker_system_df`)
- **Options:**
  - A. Separate `docker_buildx_disk_usage` (builder-scoped).
  - B. One data source with an optional `builder`.
- **Upstream:** CLI `docker buildx du`, `docker system df`; swagger `SystemDataUsage` (`BuildCache` section).
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. system df reports images, containers and volumes, which a builder lacks; unlike prune (Q23), the shapes differ.
- **Decision:** data `docker_buildx_disk_usage` (separate from system df). (Pass 5, decided by the user.)

## CLI-only

### Q19. Manifest list resource

- **Items:** cli-only:manifest-list (`docker_registry_manifest_list`); buildx:imagetools-create (skipped, points here)
- **Options:**
  - A. `docker_registry_manifest_list`.
  - B. `docker_manifest_list`.
  - C. `docker_buildx_imagetools`.
- **Upstream:** CLI `docker manifest create` / `annotate` / `push`, `docker buildx imagetools create`; registry v2 API (OCI index).
- **kreuzwerker v4.6.0:** The registry family is `docker_registry_image` and `docker_registry_image_manifests`.
- **Recommendation:** A. It sits in kreuzwerker's `docker_registry_*` family, and destroy can copy `keep_remotely` from docker_registry_image.
- **Decision:** Resource `docker_registry_manifest_list` (registry API; OCI image index). (Pass 5, decided by the user.)

### Q21. Stack names

- **Items:** cli-only:stack (`docker_stack`), cli-only:stacks (`docker_stacks`), cli-only:stack-services (`docker_stack`)
- **Options:**
  - A. Keep `docker_stack`; data sources `docker_stacks` + `docker_stack`.
  - B. Keep `docker_stack`; data sources `docker_stacks` + `docker_stack_services`.
  - C. Rename to `docker_swarm_stack`.
- **Upstream:** CLI `docker stack deploy`, `docker stack ls`, `docker stack services`.
- **kreuzwerker v4.6.0:** None. Same-name resource/data pairs are its pattern.
- **Recommendation:** A. docker_stack exists, and the pair follows kreuzwerker's naming pattern.
- **Decision:** Keep `docker_stack`. Add data `docker_stacks` + data `docker_stack`. (Pass 5, decided by the user.)

### Q22. Docker context

- **Items:** cli-only:context (skipped)
- **Options:**
  - A. Skip.
  - B. A read-only `docker_context` data source resolving endpoint/TLS from ~/.docker.
- **Upstream:** CLI `docker context create|use|inspect|...`.
- **kreuzwerker v4.6.0:** No type. The provider `context` argument ("Overrides the `host` if set", env `DOCKER_CONTEXT`) has the same name here.
- **Recommendation:** A. The provider argument (kreuzwerker-compatible) already selects a context, and a data source of the same name would confuse.
- **Decision:** Skip `docker_context`. (Pass 5, decided by the user.)

### Q50. Stack data source: `tasks`?

- **Items:** cli-only:stack-services (`docker_stack`)
- **Options:**
  - A. `services` + `tasks`.
  - B. `services` only; tasks via data.docker_tasks filtered by the stack namespace label.
- **Upstream:** CLI `docker stack services` (services) and `docker stack ps` (tasks; cli-only:stack-ps is skipped and points to docker_tasks).
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** B. It follows the CLI's split, and task lists churn on every reschedule.
- **Decision:** data docker_stack: drop `tasks` (use the tasks data source). (Pass 5, decided by the user.)

### Q51. docker_stacks service count

- **Items:** cli-only:stacks (`docker_stacks`)
- **Options:**
  - A. `services` (upstream name).
  - B. `services_count`.
- **Upstream:** `docker stack ls --format` field `.Services` ("Number of services").
- **kreuzwerker v4.6.0:** None.
- **Recommendation:** A. It is the upstream name, per CONTRACT naming; it lives on a different type from docker_stack's `services` map. B is the safer read if the type mismatch worries you.
- **Decision:** data docker_stacks: `services_count` (avoids one field name being a map on docker_stack and a number here). (Pass 5, decided by the user.)

## Cross-layer (outputs contract)

### Q33. Raw names for non-JSON streams (Lane Z carry-over)

- **Items:** engine:exec (`output`), engine:image-build (`build_log`), engine:container-file (`content_base64`). Logs (`logs` + `log_entries`) were decided in Pass 4.5 and are out of scope.
- **Options:**
  - A. Keep `output`, `build_log`, `content_base64`.
  - B. Exec as `stdout` + `stderr`; keep `build_log` and `content_base64`.
  - C. A uniform `<thing>_text` (`output_text`, `build_log_text`) and `content` for file bytes.
- **Upstream:** `ExecStart` multiplexes stdout and stderr; `ImageBuild` streams JSON progress messages; `ContainerArchive` returns a tar body.
- **kreuzwerker v4.6.0:** docker_container upload uses `content` / `content_base64` (the suffix names the encoding); its docker_exec action returns nothing. This provider's ephemeral docker_exec_output already uses `stdout` / `stderr` / `exit_code`.
- **Recommendation:** B. It matches upstream's two streams and the existing docker_exec_output, and `content_base64` follows kreuzwerker's encoding suffix. Exec then has two raw outputs, so Q34 names the hashes `stdout_sha256` / `stderr_sha256`.
- **Decision:** Non-JSON raw streams: `stdout` + `stderr` (exec / run), `build_log` (builds), `content_base64` (file content). (Pass 5, decided by the user.)

### Q35. Action result shape (Lane Z carry-over)

- **Items:** every action with a `result` output: engine:build-prune, container-prune, image-prune, network-prune, volume-prune, container-wait, exec; buildx:prune; compose:cli-exec, cli-run (Q43). The V6 result outputs named for their content (`archive_size`, `container_states`, `state`, ...) are the same question.
- **Options:**
  - A. One `result` object per action: the API response body, structured.
  - B. Flat top-level fields (`space_reclaimed`, `exit_code`, ...).
  - C. `result` for multi-field bodies, flat for a single scalar.
- **Upstream:** swagger response bodies, e.g. `ContainerPrune` → `{ContainersDeleted, SpaceReclaimed}`, `ContainerWait` → `{StatusCode, Error}`, `ExecInspect` → `ExitCode`.
- **kreuzwerker v4.6.0:** Its six actions return nothing. Terraform 1.14 actions have no computed attributes, so results only reach the user as progress messages.
- **Recommendation:** A. It gives one predictable name per action and maps 1:1 to the swagger body. Since results are progress payloads, not referenceable attributes, the flat form gains nothing.
- **Decision:** One result object per action. Every action also accepts an optional `result_path`: after a successful run it writes that same result object as JSON (UTF-8, sorted keys, trailing newline). Progress messages carry the same object. Terraform can read it in a later run with `local_file` + `jsondecode`. (Pass 5, decided by the user.)

### Q36. List data sources: `ids` / `names` lists

- **Items:** engine:containers, images, networks, plugins, volumes, image-search; swarm:configs, nodes, secrets, services, tasks; compose:cli-ls, cli-volumes; buildx:builders, du; cli-only:stacks
- **Options:**
  - A. Keep `ids` / `names` list(string) beside the structured list.
  - B. Identity only inside the structured list.
- **Upstream:** List endpoints return arrays of objects (`ContainerList`, `ServiceList`, ...); CLI `-q` prints IDs only.
- **kreuzwerker v4.6.0:** data.docker_containers has only `containers[*].id` / `names`.
- **Recommendation:** A. It is additive to kreuzwerker's shape (nothing breaks), it is what V1 needs as identity, and it gives the `-q` equivalent without a `for` expression.
- **Decision:** Keep `ids` / `names` lists beside the structured list. (Pass 5, decided by the user.)

### Q39. Plural data sources: structured list name

- **Items:** swarm:services, nodes, configs, secrets, tasks; engine:containers, images, networks, plugins, volumes; cli-only:stacks
- **Options:**
  - A. Named after the type (`services`, `nodes`, ...) with raw `<plural>_json`.
  - B. A uniform `items` / `items_json` everywhere.
- **Upstream:** CLI and API name lists by object (`docker service ls`, `ServiceList`).
- **kreuzwerker v4.6.0:** data.docker_containers → `containers`.
- **Recommendation:** A. It matches kreuzwerker's docker_containers.
- **Decision:** Plural data sources: the list attribute is named after the type (`containers`, `services`, `nodes`, ...). (Pass 5, decided by the user.)

## Not found

- **Upstream evidence, none:** Q34, Q37 (hash and identity conventions with no upstream counterpart; the cited operations stream bodies without digests) and Q47 (the provider renders that text itself).
- **kreuzwerker evidence:**
  - None for 26 of 51: Q4, Q5, Q10, Q12, Q14, Q15, Q16, Q18, Q23, Q25, Q26, Q27-Q32, Q34, Q37, Q38, Q42, Q47-Q51 (swarm, compose CLI, Dockerfile, buildx extras, stack).
  - Direct (same type or attribute): Q1, Q3, Q6, Q7, Q8, Q9, Q11, Q13, Q17, Q20, Q22, Q24, Q41, Q44, Q45, Q46.
  - Indirect (a naming pattern or neighbouring type only): Q2, Q19, Q21, Q33, Q35, Q36, Q39, Q40, Q43.

## General rule (Pass 5)

- **Decision:** Audit every existing attribute name across all types. Any name that came from Pass 3 kreuzwerker parity and doesn't match the upstream field (snake_cased per CONTRACT.md) is renamed to the upstream name, e.g. port bindings use `container_port` / `host_port` / `host_ip` / `protocol` from `PortBindings`, not `internal` / `external`. Where upstream has no name, the CONTRACT.md rules apply. Every rename, including Q1 / Q6 / Q9 / Q41 / Q44 / Q45 / Q46, is recorded in [_gen/renames.json](_gen/renames.json) as `{type, kreuzwerker_attr, previous_attr, our_attr, transform, reason}`; attributes dropped with no equivalent have `our_attr: null` and a reason. kreuzwerker names appear only as the "from" side of the migration mappings (`moved {}` from registry.terraform.io/kreuzwerker/docker). (Pass 5, decided by the user.)
