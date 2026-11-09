# Inventory

The Terraform name of every item in [upstream/](upstream/) ([SOURCES.md](SOURCES.md)). Machine-readable: [inventory.json](inventory.json).

Names follow [DECISIONS.md](DECISIONS.md) (Pass 5). Status is computed by `_gen/merge.py`: `missing` = not registered in `internal/provider/registry_gen.go`, or still a Pass 5 stub; `partial` = registered with an open Pass 5 marker (the note says what is left); `exists` = registered with no marker. `cli` = CLI-only (no Engine API endpoint, client-side). Update: `in place` / `replace` for resources.

## Engine

| kind | name | update | sources | min API | status | note |
|---|---|---|---|---|---|---|
| resource | `docker_container` | in place | `POST /containers/create` (ContainerCreate)<br>`GET /containers/{id}/json` (ContainerInspect)<br>`POST /containers/{id}/update` (ContainerUpdate)<br>`POST /containers/{id}/rename` (ContainerRename)<br>`DELETE /containers/{id}` (ContainerDelete)<br>`PUT /containers/{id}/archive` (PutContainerArchive)<br>+9 more |  | exists | (Q1, Q9) |
| resource | `docker_image` | replace | `POST /images/create` (ImageCreate)<br>`POST /build` (ImageBuild)<br>`GET /images/{name}/json` (ImageInspect)<br>`DELETE /images/{name}` (ImageDelete)<br>`docker image pull`<br>`docker image build`<br>+1 more |  | exists | (Q3, Q20, Q44, Q46) |
| resource | `docker_network` | replace | `POST /networks/create` (NetworkCreate)<br>`GET /networks/{id}` (NetworkInspect)<br>`DELETE /networks/{id}` (NetworkDelete)<br>`docker network create`<br>`docker network rm` |  | exists |  |
| resource | `docker_plugin` | in place | `GET /plugins/privileges` (GetPluginPrivileges)<br>`POST /plugins/pull` (PluginPull)<br>`GET /plugins/{name}/json` (PluginInspect)<br>`POST /plugins/{name}/enable` (PluginEnable)<br>`POST /plugins/{name}/disable` (PluginDisable)<br>`POST /plugins/{name}/set` (PluginSet)<br>+8 more | 1.25 | exists |  |
| resource | `docker_registry_image` | replace | `POST /images/{name}/push` (ImagePush)<br>`GET /distribution/{name}/json` (DistributionInspect)<br>`docker image push` |  | exists | (Q45) |
| resource | `docker_tag` | replace | `POST /images/{name}/tag` (ImageTag)<br>`docker image tag` |  | exists |  |
| resource | `docker_volume` | in place | `POST /volumes/create` (VolumeCreate)<br>`GET /volumes/{name}` (VolumeInspect)<br>`PUT /volumes/{name}` (VolumeUpdate)<br>`DELETE /volumes/{name}` (VolumeDelete)<br>`docker volume create`<br>`docker volume update`<br>+1 more | 1.21 | exists |  |
| data source | `docker_container` |  | `GET /containers/{id}/json` (ContainerInspect)<br>`docker container inspect`<br>`docker inspect` |  | exists |  |
| data source | `docker_container_changes` |  | `GET /containers/{id}/changes` (ContainerChanges)<br>`docker container diff` |  | exists |  |
| data source | `docker_container_file` |  | `HEAD /containers/{id}/archive` (ContainerArchiveInfo)<br>`GET /containers/{id}/archive` (ContainerArchive)<br>`docker container cp (out of a container)` | 1.20 | exists | (Q9, Q33, Q34) ⚠ Confusable with docker_container `upload` blocks (the write direction); also consider an action instead if large archives are expected. |
| data source | `docker_container_logs` |  | `GET /containers/{id}/logs` (ContainerLogs)<br>`docker container logs` |  | exists | (Q6) (was `docker_logs`) |
| data source | `docker_container_stats` |  | `GET /containers/{id}/stats` (ContainerStats)<br>`docker container stats --no-stream` |  | exists |  |
| data source | `docker_container_top` |  | `GET /containers/{id}/top` (ContainerTop)<br>`docker container top` |  | exists |  |
| data source | `docker_containers` |  | `GET /containers/json` (ContainerList)<br>`docker container ls`<br>`docker ps` |  | exists |  |
| data source | `docker_image` |  | `GET /images/{name}/json` (ImageInspect)<br>`docker image inspect` |  | exists | (Q44, Q46) |
| data source | `docker_image_attestations` |  | `GET /images/{name}/attestations` (ImageAttestations) | 1.55 | exists |  |
| data source | `docker_image_history` |  | `GET /images/{name}/history` (ImageHistory)<br>`docker image history` |  | exists |  |
| data source | `docker_image_search` |  | `GET /images/search` (ImageSearch)<br>`docker search` |  | exists |  |
| data source | `docker_images` |  | `GET /images/json` (ImageList)<br>`docker image ls`<br>`docker images` |  | exists |  |
| data source | `docker_info` |  | `GET /info` (SystemInfo)<br>`docker system info`<br>`docker info` |  | exists |  |
| data source | `docker_network` |  | `GET /networks/{id}` (NetworkInspect)<br>`docker network inspect` |  | exists |  |
| data source | `docker_networks` |  | `GET /networks` (NetworkList)<br>`docker network ls` |  | exists |  |
| data source | `docker_plugin` |  | `GET /plugins/{name}/json` (PluginInspect)<br>`docker plugin inspect` | 1.25 | exists |  |
| data source | `docker_plugins` |  | `GET /plugins` (PluginList)<br>`docker plugin ls` | 1.25 | exists |  |
| data source | `docker_registry_image` |  | `GET /distribution/{name}/json` (DistributionInspect) | 1.30 | exists | (Q45) |
| data source | `docker_registry_image_tags` |  | `docker buildx imagetools inspect` |  | exists · cli |  |
| data source | `docker_system_df` |  | `GET /system/df` (SystemDataUsage)<br>`docker system df` |  | exists |  |
| data source | `docker_version` |  | `GET /version` (SystemVersion)<br>`docker version` |  | exists |  |
| data source | `docker_volume` |  | `GET /volumes/{name}` (VolumeInspect)<br>`docker volume inspect` | 1.21 | exists |  |
| data source | `docker_volumes` |  | `GET /volumes` (VolumeList)<br>`docker volume ls` | 1.21 | exists |  |
| action | `docker_builder_prune` |  | `POST /build/prune` (BuildPrune)<br>`docker builder prune` |  | exists | (Q2, Q23) ⚠ docker_builder_prune (Engine build cache) is easy to confuse with docker_buildx_builder (a BuildKit builder container); docker_build_cache_prune avoids it. |
| action | `docker_container_cp` |  | `PUT /containers/{id}/archive` (PutContainerArchive)<br>`docker container cp` | 1.20 | exists | (Q9) (was `docker_container_upload`) |
| action | `docker_container_export` |  | `GET /containers/{id}/export` (ContainerExport)<br>`docker container export` |  | exists |  |
| action | `docker_container_kill` |  | `POST /containers/{id}/kill` (ContainerKill)<br>`docker container kill` |  | exists | (Q15, Q40) |
| action | `docker_container_pause` |  | `POST /containers/{id}/pause` (ContainerPause)<br>`docker container pause` |  | exists | (Q15, Q40) |
| action | `docker_container_prune` |  | `POST /containers/prune` (ContainerPrune)<br>`docker container prune` | 1.25 | exists |  |
| action | `docker_container_restart` |  | `POST /containers/{id}/restart` (ContainerRestart)<br>`docker container restart` |  | exists | (Q15, Q40) |
| action | `docker_container_start` |  | `POST /containers/{id}/start` (ContainerStart)<br>`docker container start` |  | exists | (Q15, Q40) |
| action | `docker_container_stop` |  | `POST /containers/{id}/stop` (ContainerStop)<br>`docker container stop` |  | exists | (Q15, Q40) |
| action | `docker_container_unpause` |  | `POST /containers/{id}/unpause` (ContainerUnpause)<br>`docker container unpause` |  | exists | (Q15, Q40) |
| action | `docker_container_wait` |  | `POST /containers/{id}/wait` (ContainerWait)<br>`docker container wait` |  | exists |  |
| action | `docker_exec` |  | `POST /containers/{id}/exec` (ContainerExec)<br>`POST /exec/{id}/start` (ExecStart)<br>`GET /exec/{id}/json` (ExecInspect)<br>`docker container exec` |  | exists | (Q33, Q34, Q35) |
| action | `docker_image_commit` |  | `POST /commit` (ImageCommit)<br>`docker container commit` |  | exists |  |
| action | `docker_image_import` |  | `POST /images/create` (ImageCreate)<br>`docker image import` |  | exists |  |
| action | `docker_image_load` |  | `POST /images/load` (ImageLoad)<br>`docker image load` |  | exists |  |
| action | `docker_image_prune` |  | `POST /images/prune` (ImagePrune)<br>`docker image prune` | 1.25 | exists |  |
| action | `docker_image_save` |  | `GET /images/{name}/get` (ImageGet)<br>`GET /images/get` (ImageGetAll)<br>`docker image save` |  | exists |  |
| action | `docker_network_prune` |  | `POST /networks/prune` (NetworkPrune)<br>`docker network prune` | 1.25 | exists |  |
| action | `docker_plugin_create` |  | `POST /plugins/create` (PluginCreate)<br>`docker plugin create` | 1.25 | exists | (Q8) ⚠ Confusable with docker_plugin (install from a registry). PluginCreate has a delete counterpart, so it could also be a resource. |
| action | `docker_plugin_push` |  | `POST /plugins/{name}/push` (PluginPush)<br>`docker plugin push` | 1.25 | exists |  |
| action | `docker_system_prune` |  | `POST /containers/prune` (ContainerPrune)<br>`POST /networks/prune` (NetworkPrune)<br>`POST /images/prune` (ImagePrune)<br>`POST /build/prune` (BuildPrune)<br>`POST /volumes/prune` (VolumePrune)<br>`docker system prune` |  | exists |  |
| action | `docker_volume_prune` |  | `POST /volumes/prune` (VolumePrune)<br>`docker volume prune` | 1.25 | exists |  |
| ephemeral | `docker_exec_output` |  | `POST /containers/{id}/exec` (ContainerExec)<br>`POST /exec/{id}/start` (ExecStart)<br>`GET /exec/{id}/json` (ExecInspect) |  | exists |  |
| ephemeral | `docker_registry_login` |  | `POST /auth` (SystemAuth)<br>`docker login` |  | exists | (Q7) (was `docker_registry_auth`) ⚠ Confusable with ephemeral docker_registry_token and with provider registry_auth blocks. |
| ephemeral | `docker_registry_token` |  | `docker login` |  | exists · cli |  |
| list resource | `docker_container` |  |  |  | exists |  |
| list resource | `docker_image` |  |  |  | exists |  |
| list resource | `docker_network` |  |  |  | exists |  |
| list resource | `docker_volume` |  |  |  | exists |  |
| function | `image_ref` |  |  |  | exists |  |
| function | `parse_image_ref` |  |  |  | exists |  |
| function | `port_spec` |  |  |  | exists |  |
| skip | - |  | `POST /containers/{id}/attach` (ContainerAttach)<br>`GET /containers/{id}/attach/ws` (ContainerAttachWebsocket)<br>`docker container attach` |  | - | skip: Interactive hijacked/websocket stream. |
| skip | - |  | `GET /events` (SystemEvents)<br>`docker system events` |  | - | skip: Unbounded streaming endpoint; no Terraform lifecycle fits (a bounded since/until snapshot data source is possible later). |
| skip | - |  | `POST /build` (ImageBuild)<br>`docker image build`<br>`docker build` |  | - | skip: Q3: no standalone build action; builds live in docker_image's `build` block (BuildKit, Q20). ⚠ Confusable with docker_image (resource whose `build` block already calls ImageBuild) and with docker_buildx_builder. |
| skip | - |  | `POST /networks/{id}/connect` (NetworkConnect)<br>`POST /networks/{id}/disconnect` (NetworkDisconnect)<br>`docker network connect`<br>`docker network disconnect` |  | - | skip: Q1: no standalone attachment type; docker_container `networks` connects and disconnects in place (NetworkConnect / NetworkDisconnect). ⚠ Overlaps docker_container `networks`; managing the same pair in both would fight. |
| skip | - |  | `GET /_ping` (SystemPing)<br>`HEAD /_ping` (SystemPingHead) | 1.11 | - | skip: Used internally by provider configure for API version negotiation; no user-facing object. |
| skip | - |  | `POST /containers/{id}/resize` (ContainerResize)<br>`POST /exec/{id}/resize` (ExecResize) |  | - | skip: Only meaningful for an interactive TTY session. |
| skip | - |  | `POST /session` (Session) | 1.31 | - | skip: Deprecated (API 1.53) transport; used internally by the docker_image BuildKit build path, not a user-facing object. |

## Swarm

| kind | name | update | sources | min API | status | note |
|---|---|---|---|---|---|---|
| resource | `docker_config` | in place | `POST /configs/create` (ConfigCreate)<br>`GET /configs/{id}` (ConfigInspect)<br>`POST /configs/{id}/update` (ConfigUpdate)<br>`DELETE /configs/{id}` (ConfigDelete)<br>`docker config create`<br>`docker config rm` |  | exists |  ⚠ docker_config (swarm config object) vs compose top-level `configs` and the provider's own configuration block. |
| resource | `docker_node` | in place | `GET /nodes/{id}` (NodeInspect)<br>`POST /nodes/{id}/update` (NodeUpdate)<br>`docker node update` |  | exists | (Q4) |
| resource | `docker_secret` | in place | `POST /secrets/create` (SecretCreate)<br>`GET /secrets/{id}` (SecretInspect)<br>`POST /secrets/{id}/update` (SecretUpdate)<br>`DELETE /secrets/{id}` (SecretDelete)<br>`docker secret create`<br>`docker secret rm` | 1.25 | exists |  |
| resource | `docker_service` | in place | `POST /services/create` (ServiceCreate)<br>`GET /services/{id}` (ServiceInspect)<br>`POST /services/{id}/update` (ServiceUpdate)<br>`DELETE /services/{id}` (ServiceDelete)<br>`docker service create`<br>`docker service update`<br>+1 more |  | exists |  |
| resource | `docker_swarm_init` | in place | `POST /swarm/init` (SwarmInit)<br>`GET /swarm` (SwarmInspect)<br>`POST /swarm/update` (SwarmUpdate)<br>`GET /swarm/unlockkey` (SwarmUnlockkey)<br>`POST /swarm/leave` (SwarmLeave)<br>`docker swarm init`<br>+2 more |  | exists | (Q38) |
| resource | `docker_swarm_node` | in place | `POST /swarm/join` (SwarmJoin)<br>`POST /swarm/leave` (SwarmLeave)<br>`POST /nodes/{id}/update` (NodeUpdate)<br>`GET /nodes/{id}` (NodeInspect)<br>`DELETE /nodes/{id}` (NodeDelete)<br>`docker swarm join`<br>+3 more |  | exists | (Q4) ⚠ docker_swarm_node is a *join* resource; a user expecting it to manage an already-joined node (labels/availability only, NodeUpdate) would need a separate type, e.g. docker_node. |
| data source | `docker_config` |  | `GET /configs/{id}` (ConfigInspect)<br>`docker config inspect` |  | exists |  ⚠ Same confusion as docker_config resource (swarm config vs compose configs). |
| data source | `docker_configs` |  | `GET /configs` (ConfigList)<br>`docker config ls` |  | exists |  |
| data source | `docker_node` |  | `GET /nodes/{id}` (NodeInspect)<br>`docker node inspect` |  | exists |  |
| data source | `docker_nodes` |  | `GET /nodes` (NodeList)<br>`docker node ls` |  | exists |  |
| data source | `docker_secret` |  | `GET /secrets/{id}` (SecretInspect)<br>`docker secret inspect` | 1.25 | exists |  |
| data source | `docker_secrets` |  | `GET /secrets` (SecretList)<br>`docker secret ls` | 1.25 | exists |  |
| data source | `docker_service` |  | `GET /services/{id}` (ServiceInspect)<br>`docker service inspect` |  | exists |  |
| data source | `docker_service_logs` |  | `GET /services/{id}/logs` (ServiceLogs)<br>`docker service logs` |  | exists | (Q6) ⚠ Could instead extend data.docker_logs with a service_id/task_id argument. |
| data source | `docker_services` |  | `GET /services` (ServiceList)<br>`docker service ls` |  | exists |  |
| data source | `docker_swarm` |  | `GET /swarm` (SwarmInspect)<br>`docker swarm join-token` |  | exists |  |
| data source | `docker_swarm_tokens` |  | `GET /swarm` (SwarmInspect)<br>`docker swarm join-token` |  | exists |  |
| data source | `docker_task` |  | `GET /tasks/{id}` (TaskInspect)<br>`docker inspect` |  | exists |  |
| data source | `docker_task_logs` |  | `GET /tasks/{id}/logs` (TaskLogs)<br>`docker service logs <task-id>` |  | exists | (Q6) ⚠ Could merge with docker_service_logs or data.docker_logs. |
| data source | `docker_tasks` |  | `GET /tasks` (TaskList)<br>`docker service ps`<br>`docker node ps`<br>`docker stack ps` |  | exists |  |
| action | `docker_node_activate` |  | `POST /nodes/{id}/update` (NodeUpdate)<br>`docker node update --availability active` |  | exists |  |
| action | `docker_node_demote` |  | `POST /nodes/{id}/update` (NodeUpdate)<br>`docker node demote` |  | exists | (Q5, Q40) |
| action | `docker_node_drain` |  | `POST /nodes/{id}/update` (NodeUpdate)<br>`docker node update --availability drain` |  | exists |  |
| action | `docker_node_promote` |  | `POST /nodes/{id}/update` (NodeUpdate)<br>`docker node promote` |  | exists | (Q5, Q40) |
| action | `docker_service_force_update` |  | `POST /services/{id}/update` (ServiceUpdate)<br>`docker service update --force` |  | exists |  |
| action | `docker_service_rollback` |  | `POST /services/{id}/update` (ServiceUpdate)<br>`docker service rollback` |  | exists |  |
| action | `docker_service_scale` |  | `POST /services/{id}/update` (ServiceUpdate)<br>`docker service scale` |  | exists |  |
| action | `docker_swarm_rotate_ca` |  | `POST /swarm/update` (SwarmUpdate)<br>`docker swarm ca --rotate` |  | exists | (Q10) (was `docker_swarm_ca_rotate`) |
| action | `docker_swarm_rotate_tokens` |  | `POST /swarm/update` (SwarmUpdate)<br>`docker swarm join-token --rotate` |  | exists | (Q10) |
| action | `docker_swarm_rotate_unlock_key` |  | `POST /swarm/update` (SwarmUpdate)<br>`docker swarm unlock-key --rotate` |  | exists | (Q10) |
| action | `docker_swarm_unlock` |  | `POST /swarm/unlock` (SwarmUnlock)<br>`docker swarm unlock` |  | exists |  |
| ephemeral | `docker_swarm_tokens` |  | `GET /swarm` (SwarmInspect) |  | exists |  |
| list resource | `docker_config` |  |  |  | exists |  |
| list resource | `docker_secret` |  |  |  | exists |  |
| list resource | `docker_service` |  |  |  | exists |  |
| list resource | `docker_swarm_node` |  |  |  | exists |  |

## Compose

| kind | name | update | sources | min API | status | note |
|---|---|---|---|---|---|---|
| resource | `docker_compose` | in place | `#/properties/configs`<br>`#/properties/configs`<br>`#/$defs/config`<br>`#/$defs/config`<br>08-configs.md#configs-top-level-element |  | exists | (Q17, Q42) ⚠ Top-level `configs` vs docker_config (Swarm config resource) vs the proposed docker_compose_config data source (CLI `compose config`, rendered model). |
| resource | `docker_compose` | in place | `#/properties/include`<br>`#/properties/include`<br>`#/$defs/include`<br>`#/$defs/include`<br>14-include.md#include |  | exists | (Q17, Q42) |
| resource | `docker_compose` | in place | `#/properties/jobs`<br>`#/properties/jobs`<br>`#/$defs/job`<br>`#/$defs/job`<br>`#/$defs/schedule`<br>`#/$defs/schedule`<br>+2 more |  | exists | (Q18) |
| resource | `docker_compose` | in place | `#/properties/models`<br>`#/properties/models`<br>`#/$defs/model`<br>`#/$defs/model`<br>models.md#models-top-level-element |  | exists | (Q17, Q42) |
| resource | `docker_compose` | in place | `#/properties/name`<br>`#/properties/name`<br>04-version-and-name.md#name-top-level-element |  | exists | (Q41, Q42) |
| resource | `docker_compose` | in place | `#/properties/networks`<br>`#/properties/networks`<br>`#/$defs/network`<br>`#/$defs/network`<br>06-networks.md#networks-top-level-element |  | exists | (Q17, Q42) ⚠ docker_compose_network would be confusable with docker_network. |
| resource | `docker_compose` | in place | `#/properties/secrets`<br>`#/properties/secrets`<br>`#/$defs/secret`<br>`#/$defs/secret`<br>09-secrets.md#secrets-top-level-element |  | exists | (Q17, Q42) ⚠ docker_compose_secret would be confusable with docker_secret. |
| resource | `docker_compose` | in place | `#/properties/services`<br>`#/properties/services`<br>`#/$defs/service`<br>`#/$defs/service`<br>05-services.md#services-top-level-element<br>container_spec.md#container-specification<br>+1 more |  | exists | (Q17, Q42) ⚠ A per-service type named docker_compose_service would be confusable with docker_service (swarm). Keep services inside docker_compose. |
| resource | `docker_compose` | in place | `#/properties/volumes`<br>`#/properties/volumes`<br>`#/$defs/volume`<br>`#/$defs/volume`<br>07-volumes.md#volumes-top-level-element |  | exists | (Q17, Q42) ⚠ docker_compose_volume(s) would be confusable with docker_volume and with the proposed docker_compose_volumes data source (CLI `compose volumes`). |
| resource | `docker_image` | in place | `#/$defs/service/properties/build`<br>`#/$defs/service/properties/build`<br>build.md#compose-build-specification |  | exists |  |
| resource | `docker_service` | in place | `#/$defs/service/properties/deploy`<br>`#/$defs/service/properties/deploy`<br>`#/$defs/deployment`<br>`#/$defs/deployment`<br>deploy.md#compose-deploy-specification |  | exists |  |
| data source | `docker_compose_config` |  | `docker compose config`<br>`docker compose config` |  | exists · cli | (Q12) ⚠ Confusable with top-level compose `configs` and docker_config (swarm config resource). Alternatives: docker_compose_project, docker_compose_model, or function compose_render. |
| data source | `docker_compose_containers` |  | `docker compose ps`<br>`docker compose ps` |  | exists · cli | (Q13) ⚠ Name options: docker_compose_ps (CLI-shaped) vs docker_compose_containers (plural-noun convention beside docker_containers). |
| data source | `docker_compose_images` |  | `docker compose images`<br>`docker compose images` |  | exists · cli |  ⚠ Beside data.docker_image (singular) and list.docker_image. |
| data source | `docker_compose_logs` |  | `docker compose logs`<br>`docker compose logs` |  | exists · cli |  |
| data source | `docker_compose_port` |  | `docker compose port`<br>`docker compose port` |  | exists · cli |  ⚠ Confusable with the port_spec function. |
| data source | `docker_compose_projects` |  | `docker compose ls`<br>`docker compose ls` |  | exists · cli | (Q13) ⚠ docker_compose_ls (CLI-shaped) vs docker_compose_projects (noun). |
| data source | `docker_compose_top` |  | `docker compose top`<br>`docker compose top` |  | exists · cli |  |
| data source | `docker_compose_volumes` |  | `docker compose volumes`<br>`docker compose volumes` |  | exists · cli | (Q14) ⚠ docker_compose_volumes vs docker_volume / top-level compose `volumes`. |
| action | `docker_compose_build` |  | `docker compose build`<br>`docker compose build` |  | exists · cli |  |
| action | `docker_compose_commit` |  | `docker compose commit`<br>`docker compose commit` |  | exists · cli |  |
| action | `docker_compose_cp` |  | `docker compose cp`<br>`docker compose cp` |  | exists · cli |  |
| action | `docker_compose_exec` |  | `docker compose exec`<br>`docker compose exec` |  | exists · cli | (Q43) |
| action | `docker_compose_export` |  | `docker compose export`<br>`docker compose export` |  | exists · cli |  |
| action | `docker_compose_kill` |  | `docker compose kill`<br>`docker compose kill` |  | exists · cli | (Q15) ⚠ Would mirror single-container docker_container_kill actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those. |
| action | `docker_compose_pause` |  | `docker compose pause`<br>`docker compose pause` |  | exists · cli | (Q15) ⚠ Would mirror single-container docker_container_pause actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those. |
| action | `docker_compose_publish` |  | `docker compose publish`<br>`docker compose publish` |  | exists · cli |  |
| action | `docker_compose_pull` |  | `docker compose pull`<br>`docker compose pull` |  | exists · cli |  |
| action | `docker_compose_push` |  | `docker compose push`<br>`docker compose push` |  | exists · cli |  |
| action | `docker_compose_restart` |  | `docker compose restart`<br>`docker compose restart` |  | exists · cli | (Q15) ⚠ Would mirror single-container docker_container_restart actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those. |
| action | `docker_compose_run` |  | `docker compose run`<br>`docker compose run` |  | exists · cli | (Q18, Q43) |
| action | `docker_compose_scale` |  | `docker compose scale`<br>`docker compose scale` |  | exists · cli | (Q16) ⚠ docker_service_scale (swarm action) already exists; docker_compose_scale is near-identical in purpose. |
| action | `docker_compose_start` |  | `docker compose start`<br>`docker compose start` |  | exists · cli | (Q15) ⚠ Would mirror single-container docker_container_start actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those. |
| action | `docker_compose_stop` |  | `docker compose stop`<br>`docker compose stop` |  | exists · cli | (Q15) ⚠ Would mirror single-container docker_container_stop actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those. |
| action | `docker_compose_unpause` |  | `docker compose unpause`<br>`docker compose unpause` |  | exists · cli | (Q15) ⚠ Would mirror single-container docker_container_unpause actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those. |
| action | `docker_compose_wait` |  | `docker compose wait`<br>`docker compose wait` |  | exists · cli |  |
| function | `compose_env` |  |  |  | exists |  |
| skip | - |  | `docker compose alpha`<br>`docker compose alpha` |  | - · cli | skip: command group only |
| skip | - |  | `docker compose alpha dry-run`<br>`docker compose alpha dry-run` |  | - · cli | skip: Terraform plan already is the dry run |
| skip | - |  | `docker compose alpha generate`<br>`docker compose alpha generate` |  | - · cli | skip: experimental authoring helper; output is a file, not infra (terraform query + list resources cover discovery) |
| skip | - |  | `docker compose alpha publish`<br>`docker compose alpha publish` |  | - · cli | skip: duplicate of docker_compose_publish |
| skip | - |  | `docker compose alpha scale`<br>`docker compose alpha scale` |  | - · cli | skip: duplicate of docker_compose_scale |
| skip | - |  | `docker compose alpha viz`<br>`docker compose alpha viz` |  | - · cli | skip: visualisation only; terraform graph covers it |
| skip | - |  | `docker compose alpha watch`<br>`docker compose alpha watch` |  | - · cli | skip: interactive file watcher |
| skip | - |  | `docker compose attach`<br>`docker compose attach` |  | - · cli | skip: interactive attach stream |
| skip | - |  | `docker compose bridge`<br>`docker compose bridge` |  | - · cli | skip: command group; targets non-Docker platforms |
| skip | - |  | `docker compose bridge convert`<br>`docker compose bridge convert` |  | - · cli | skip: out of scope (Kubernetes output) |
| skip | - |  | `docker compose bridge transformations`<br>`docker compose bridge transformations` |  | - · cli | skip: command group |
| skip | - |  | `docker compose bridge transformations create`<br>`docker compose bridge transformations create` |  | - · cli | skip: out of scope (compose-bridge tooling) |
| skip | - |  | `docker compose bridge transformations list`<br>`docker compose bridge transformations list` |  | - · cli | skip: out of scope (compose-bridge tooling) |
| skip | - |  | `docker compose convert` |  | - · cli | skip: alias of config |
| skip | - |  | `docker compose create`<br>`docker compose create` |  | - · cli | skip: half of `up`; the docker_compose resource owns container creation |
| skip | - |  | `docker compose down`<br>`docker compose down` |  | - · cli | skip: Q11: no down action; destroying docker_compose is `down`. |
| skip | - |  | `docker compose events`<br>`docker compose events` |  | - · cli | skip: streaming |
| skip | - |  | `docker compose rm`<br>`docker compose rm` |  | - · cli | skip: docker_compose reconciles and removes its own containers; one-off cleanup is docker_system_prune |
| skip | - |  | `docker compose`<br>`docker compose` |  | - · cli | skip: command group only; global flags (-f, -p, --profile, --env-file) are docker_compose attributes |
| skip | - |  | `docker compose stats`<br>`docker compose stats` |  | - · cli | skip: streaming (a --no-stream snapshot could be a data source later) |
| skip | - |  | `docker compose up`<br>`docker compose up` |  | - · cli | skip: Q11: no up action; creating or updating docker_compose is `up`. ⚠ docker_compose (resource) already means up; an action docker_compose_up beside it invites confusion. |
| skip | - |  | `docker compose version`<br>`docker compose version` |  | - · cli | skip: client version only; provider embeds compose-go v2.16.2 |
| skip | - |  | `docker compose watch`<br>`docker compose watch` |  | - · cli | skip: interactive, long-running file watcher |
| skip | - |  | 11-extension.md#extension |  | - | skip: file-format mechanic, handled by the compose loader |
| skip | - |  | 10-fragments.md#fragments |  | - | skip: file-format mechanic, handled by the compose loader |
| skip | - |  | 12-interpolation.md#interpolation |  | - | skip: file-format mechanic, handled by the compose loader |
| skip | - |  | 13-merge.md#merge-and-override |  | - | skip: file-format mechanic, handled by the compose loader |
| skip | - |  | 15-profiles.md#profiles |  | - | skip: file-format mechanic, handled by the compose loader |
| skip | - |  | `#/$defs/service/properties/develop`<br>`#/$defs/service/properties/develop`<br>`#/$defs/development`<br>`#/$defs/development`<br>develop.md#compose-develop-specification |  | - | skip: developer inner-loop file watching; not declarative (docker_compose warns and ignores it) |
| skip | - |  | `#/properties/version`<br>`#/properties/version`<br>04-version-and-name.md#version-top-level-element-obsolete |  | - | skip: obsolete; Compose ignores it (warns) and so does the loader |

## Dockerfile

| kind | name | flags | sources | status |
|---|---|---|---|---|
| data source | `docker_dockerfile` |  | reference.md#format<br>reference.md#parser-directives<br>reference.md#from<br>reference.md#understand-how-arg-and-from-interact<br>reference.md#expose<br>reference.md#arg<br>+4 more | exists (Q27, Q47, Q48, Q49) |
| block | `docker_dockerfile.add` | `sources/destination`, `--keep-git-dir`, `--checksum`, `--chmod`, `--chown`, `--link`, `--unpack`, `--exclude` | reference.md#add | exists (Q27) |
| block | `docker_dockerfile.arg` | `<name>[=<default>]`, `predefined ARGs`, `automatic platform ARGs`, `BuildKit built-in build args` | reference.md#arg | exists (Q27) |
| block | `docker_dockerfile.check` | `skip`, `error`, `ConsistentInstructionCasing`, `CopyIgnoredFile`, `DuplicateStageName`, `ExposeInvalidFormat`, `ExposeProtoCasing`, `FromAsCasing`, `FromPlatformFlagConstDisallowed`, `InvalidDefaultArgInFrom`, `InvalidDefinitionDescription`, `JSONArgsRecommended`, `LegacyKeyValueFormat`, `MaintainerDeprecated`, `MultipleInstructionsDisallowed`, `NoEmptyContinuation`, `RedundantTargetPlatform`, `ReservedStageName`, `SecretsUsedInArgOrEnv`, `StageNameCasing`, `UndefinedArgInFrom`, `UndefinedVar`, `WorkdirRelativePath` | reference.md#check<br>consistent-instruction-casing.md<br>copy-ignored-file.md<br>duplicate-stage-name.md<br>expose-invalid-format.md<br>expose-proto-casing.md<br>+17 more | exists (Q27, Q28) |
| block | `docker_dockerfile.cmd` | `exec form`, `exec form (params to ENTRYPOINT)`, `shell form` | reference.md#cmd | exists (Q27) |
| block | `docker_dockerfile.copy` | `sources/destination`, `heredoc`, `--from`, `--chmod`, `--chown`, `--link`, `--parents`, `--exclude` | reference.md#copy | exists (Q27) |
| block | `docker_dockerfile.entrypoint` | `exec form`, `shell form` | reference.md#entrypoint | exists (Q27) |
| block | `docker_dockerfile.env` | `<key>=<value>` | reference.md#env | exists (Q27) |
| block | `docker_dockerfile.escape` | `escape` | reference.md#escape | exists (Q27, Q28) |
| block | `docker_dockerfile.expose` | `<port>[/<protocol>]` | reference.md#expose | exists (Q27) |
| block | `docker_dockerfile.healthcheck` | `CMD`, `NONE`, `--interval`, `--timeout`, `--start-period`, `--start-interval`, `--retries` | reference.md#healthcheck | exists (Q27) |
| block | `docker_dockerfile.label` | `<key>=<value>` | reference.md#label | exists (Q27) |
| block | `docker_dockerfile.maintainer` | `<name>` | reference.md#maintainer-deprecated | exists (Q27, Q29) |
| block | `docker_dockerfile.onbuild` | `trigger`, `trigger with --from / --mount from` | reference.md#onbuild | exists (Q27, Q30) |
| block | `docker_dockerfile.run` | `shell form`, `exec form`, `heredoc`, `--device`, `--mount`, `--mount=type=bind`, `--mount=type=cache`, `--mount=type=tmpfs`, `--mount=type=secret`, `--mount=type=ssh`, `--network`, `--network=default`, `--network=none`, `--network=host`, `--security` | reference.md#run | exists (Q27) |
| block | `docker_dockerfile.shell` | `exec form` | reference.md#shell | exists (Q27) |
| block | `docker_dockerfile.stage` | `--platform`, `image`, `AS name`, `global ARG substitution` | reference.md#from | exists (Q27, Q31) |
| block | `docker_dockerfile.stopsignal` | `signal` | reference.md#stopsignal | exists (Q27) |
| block | `docker_dockerfile.syntax` | `syntax` | reference.md#syntax | exists (Q27, Q28) |
| block | `docker_dockerfile.user` | `user` | reference.md#user | exists (Q27) |
| block | `docker_dockerfile.volume` | `paths` | reference.md#volume | exists (Q27) |
| block | `docker_dockerfile.workdir` | `path` | reference.md#workdir | exists (Q27) |

## Buildx

| kind | name | update | sources | min API | status | note |
|---|---|---|---|---|---|---|
| resource | `docker_buildx_builder` | in place | `docker buildx create`<br>`docker buildx create --help`<br>`docker buildx rm`<br>`docker buildx rm --help` |  | exists · cli |  |
| resource | `docker_image` | replace | `docker buildx build`<br>`docker buildx build --help`<br>`docker build`<br>`docker build --help`<br>`docker image build`<br>`docker image build --help`<br>+3 more |  | exists · cli | (Q20) ⚠ Do not add docker_buildx_build: it would duplicate docker_image. If exporters that produce no local image are wanted, that is an action (docker_buildx_bake or docker_image_build action), not a second build resource. |
| data source | `docker_buildx_builder` |  | `docker buildx inspect`<br>`docker buildx inspect --help` |  | exists · cli | (Q24) ⚠ Same name as the resource (resource/data source pair, allowed). For builders not created by Terraform, discovery is limited to buildx_buildkit_* containers on the daemon (buildx's builder store is client-side). |
| data source | `docker_buildx_builders` |  | `docker buildx ls`<br>`docker buildx ls --help` |  | exists · cli | (Q24) |
| data source | `docker_buildx_disk_usage` |  | `docker buildx du`<br>`docker buildx du --help`<br>`GET /system/df` (SystemDataUsage) |  | exists · cli | (Q26) ⚠ Overlaps Z1's system df data source (BuildCache section); merge or keep builder-scoped. |
| data source | `docker_buildx_history` |  | `docker buildx history inspect`<br>`docker buildx history inspect --help` |  | exists · cli | (Q24) (was `docker_buildx_history_record`) ⚠ Could be folded into docker_buildx_history as a `ref` filter; naming question. |
| data source | `docker_buildx_history_records` |  | `docker buildx history ls`<br>`docker buildx history ls --help`<br>`docker buildx history`<br>`docker buildx history --help` |  | exists · cli | (Q24) (was `docker_buildx_history`) |
| data source | `docker_registry_image_manifests` |  | `docker buildx imagetools inspect`<br>`docker buildx imagetools inspect --help` |  | exists · cli |  ⚠ Same type as cli-only:manifest-inspect. |
| action | `docker_buildx_bake` |  | `docker buildx bake`<br>`docker buildx bake --help`<br>`docker bake`<br>`docker bake --help`<br>bake-reference.md#file-format |  | exists · cli | (Q20) ⚠ Overlaps docker_image (build block) for single targets. |
| skip | - |  | bake-reference.md#function<br>bake-stdlib.md |  | - · cli | skip: Bake HCL is itself HCL built on the same go-cty stdlib; Terraform functions (plus provider functions) replace user functions. |
| skip | - |  | bake-reference.md#group |  | - · cli | skip: A list of target names; in Terraform this is the docker_buildx_bake action's `targets` argument or a for_each over targets. |
| skip | - |  | bake-reference.md#target |  | - · cli | skip: Not a Terraform type: becomes a nested `target` block of the docker_buildx_bake action (and its attributes mirror docker_image.build). `inherits` and `matrix` map to Terraform locals/for_each. |
| skip | - |  | bake-reference.md#variable |  | - · cli | skip: Terraform variables/locals replace them; when the action consumes an existing bake file, overrides go through a `set`/`variables` map argument. |
| skip | - |  | `docker buildx dap`<br>`docker buildx dap --help`<br>`docker buildx dap build`<br>`docker buildx dap build --help` |  | - · cli | skip: Interactive IDE debugging session; not declarative. |
| skip | - |  | `docker buildx debug`<br>`docker buildx debug build` |  | - · cli | skip: Experimental, interactive; also newer than the installed buildx v0.34.1 (not in help capture). |
| skip | - |  | `docker buildx dial-stdio`<br>`docker buildx dial-stdio --help` |  | - · cli | skip: Transport plumbing for other tools; the provider dials BuildKit directly. |
| skip | - |  | `docker buildx history export`<br>`docker buildx history export --help` |  | - · cli | skip: Exports a Docker Desktop .dockerbuild bundle (Desktop UI artifact). |
| skip | - |  | `docker buildx history import`<br>`docker buildx history import --help` |  | - · cli | skip: Imports a Docker Desktop .dockerbuild bundle (Desktop UI artifact). |
| skip | - |  | `docker buildx history inspect attachment`<br>`docker buildx history inspect attachment --help` |  | - · cli | skip: Returns raw attachment blobs (SBOM/provenance) of a record; low value as state, and docker_image attestations are Z1's ImageAttestations territory. |
| skip | - |  | `docker buildx history logs`<br>`docker buildx history logs --help` |  | - · cli | skip: Build log stream; logs are not Terraform state. |
| skip | - |  | `docker buildx history open`<br>`docker buildx history open --help` |  | - · cli | skip: Opens the Docker Desktop UI. |
| skip | - |  | `docker buildx history rm`<br>`docker buildx history rm --help` |  | - · cli | skip: Deleting build records is housekeeping; covered well enough by buildx prune / builder recreation. Revisit as an action if asked. |
| skip | - |  | `docker buildx history trace`<br>`docker buildx history trace --help` |  | - · cli | skip: Starts a local Jaeger UI for the trace; interactive. |
| skip | - |  | `docker buildx imagetools create`<br>`docker buildx imagetools create --help`<br>`docker buildx imagetools`<br>`docker buildx imagetools --help` |  | - · cli | skip: Same object as cli-only:manifest-list (proposed docker_registry_manifest_list); one resource covers both `docker manifest create/push` and imagetools create. ⚠ docker_buildx_imagetools_create would duplicate docker_registry_manifest_list. |
| skip | - |  | `docker buildx policy`<br>`docker buildx policy --help`<br>`docker buildx policy eval`<br>`docker buildx policy eval --help` |  | - · cli | skip: Build-policy (Rego) development tooling, client-local; build policy itself is a --policy input on build/bake. |
| skip | - |  | `docker buildx policy json-schema` |  | - · cli | skip: Prints a JSON schema; newer than installed buildx v0.34.1. |
| skip | - |  | `docker buildx policy test`<br>`docker buildx policy test --help` |  | - · cli | skip: Build-policy (Rego) development tooling, client-local. |
| skip | - |  | `docker buildx prune`<br>`docker buildx prune --help`<br>`POST /build/prune` (BuildPrune) |  | - · cli | skip: Q2/Q23: folded into action docker_builder_prune (engine:build-prune), which prunes a buildx builder when `builder` is set. ⚠ Z1 will likely propose an Engine action for POST /build/prune (e.g. docker_build_cache_prune / docker_builder_prune). One type should take an optional `builder` and choose BuildKit gRPC vs POST /build/prune. |
| skip | - |  | `docker buildx replay`<br>`docker buildx replay build` |  | - · cli | skip: New and experimental reproducibility tooling, newer than installed buildx v0.34.1; revisit as an action docker_buildx_replay once stable. |
| skip | - |  | `docker buildx stop`<br>`docker buildx stop --help`<br>`POST /containers/{id}/stop` (ContainerStop) |  | - · cli | skip: Q25: skipped; a builder is stopped by removing or replacing docker_buildx_builder. ⚠ Low value: docker_buildx_builder with bootstrap=false or a must_run-style flag could cover it; consider skip. |
| skip | - |  | `docker buildx use`<br>`docker buildx use --help` |  | - · cli | skip: Client-side default selection; Terraform references the builder explicitly via docker_image.build.builder. |
| skip | - |  | `docker buildx version`<br>`docker buildx version --help` |  | - · cli | skip: Client version print. |

## CLI-only

| kind | name | update | sources | min API | status | note |
|---|---|---|---|---|---|---|
| resource | `docker_registry_manifest_list` | in place | `docker manifest create`<br>`docker manifest create --help`<br>`docker manifest annotate`<br>`docker manifest annotate --help`<br>`docker manifest push`<br>`docker manifest push --help`<br>+6 more |  | exists · cli | (Q19) ⚠ Same job as `docker buildx imagetools create` (buildx:imagetools-create points here). Do not also add docker_buildx_imagetools / docker_manifest. `docker manifest rm` only deletes the local draft, so destroy semantics (registry DELETE of the tag vs leave it) need a flag like docker_registry_image.keep_remotely. |
| resource | `docker_stack` | in place | `docker stack deploy`<br>`docker stack rm`<br>`docker stack`<br>`POST /services/create` (ServiceCreate)<br>`POST /services/{id}/update` (ServiceUpdate)<br>`DELETE /services/{id}` (ServiceDelete)<br>+1 more |  | exists · cli | (Q21) ⚠ Keep `docker_stack` (exists); do not rename to docker_swarm_stack even though it is a swarm-layer type. |
| data source | `docker_registry_image_manifests` |  | `docker manifest inspect`<br>`docker manifest inspect --help`<br>`docker buildx imagetools inspect`<br>`docker buildx imagetools inspect --help`<br>`GET /distribution/{name}/json` (DistributionInspect) |  | exists · cli |  ⚠ buildx:imagetools-inspect maps to the same data source. |
| data source | `docker_stack` |  | `docker stack services`<br>`GET /services` (ServiceList)<br>`GET /tasks` (TaskList) |  | exists · cli | (Q21, Q50) ⚠ Data source `docker_stack` would share its name with the existing docker_stack resource (allowed by Terraform, same pattern as docker_image); alternative name docker_stack_services. |
| data source | `docker_stacks` |  | `docker stack ls`<br>`GET /services` (ServiceList) |  | exists · cli | (Q21, Q51) |
| skip | - |  | `docker checkpoint create`<br>`docker checkpoint ls`<br>`docker checkpoint rm`<br>`docker checkpoint` |  | - · cli | skip: Experimental (needs experimental daemon + CRIU, Linux only); daemon endpoints /containers/{id}/checkpoints exist but are undocumented and absent from swagger.yaml 1.56. Revisit only if moby documents them (then: action docker_container_checkpoint). |
| skip | - |  | `docker context create`<br>`docker context create --help`<br>`docker context update`<br>`docker context update --help`<br>`docker context rm`<br>`docker context rm --help`<br>+14 more |  | - · cli | skip: Client-side config store, no Engine API; the provider's `context`, `host`, `tls` and `ssh` arguments already select the daemon, and writing files under ~/.docker from Terraform would be a local-file side effect. ⚠ A `docker_context` resource would be confusable with the provider-level `context` argument. |
| skip | - |  | `docker login`<br>`docker login --help`<br>`POST /auth` (SystemAuth) |  | - | skip: Only the validation half is Engine API (POST /auth, which Z1 owns); persisting credentials is client config. Terraform supplies credentials per run via provider registry_auth (password_wo) and ephemeral docker_registry_token. |
| skip | - |  | `docker logout`<br>`docker logout --help` |  | - · cli | skip: Pure client config edit, no endpoint; Terraform never persists credentials, so there is nothing to log out of. |
| skip | - |  | `docker stack config` |  | - · cli | skip: Pure client-side compose rendering with no daemon call; covered by tools/compose2stack and the compose lane. |
| skip | - |  | `docker stack ps`<br>`GET /tasks` (TaskList) |  | - · cli | skip: Same as the tasks list data source Z1 derives from GET /tasks with a label filter (com.docker.stack.namespace); no separate type. |
| skip | - |  | `docker system dial-stdio` |  | - · cli | skip: Transport plumbing for the CLI's ssh:// helper; the provider's ssh block is pure Go and does not need it. |
| skip | - |  | `docker trust`<br>`docker trust inspect`<br>`docker trust key`<br>`docker trust key generate`<br>`docker trust key load`<br>`docker trust revoke`<br>+4 more |  | - · cli | skip: Notary v1 client against a Notary server, no Engine API; split out of docker/cli into the docker-trust plugin and DCT is being retired by Docker Hub. Signing belongs to cosign/notation-style providers. |
| skip | - |  | `docker agent --help`<br>`docker --help` |  | - · cli | skip: Docker Desktop AI product plugin; talks to model providers and its own runtime, not the Engine API. |
| skip | - |  | `docker ai --help`<br>`docker --help` |  | - · cli | skip: Interactive Docker Desktop product feature; no Engine API. |
| skip | - |  | `docker debug --help`<br>`docker --help` |  | - · cli | skip: Docker Desktop (Pro) plugin; composite of Engine calls (create helper container, exec) plus proprietary toolbox; interactive. docker_exec covers the declarative part. |
| skip | - |  | `docker desktop --help`<br>`docker --help` |  | - · cli | skip: Controls the Docker Desktop app via its backend socket, not the Engine API. |
| skip | - |  | `docker dhi --help`<br>`docker --help` |  | - · cli | skip: Docker Hub product API (DHI), not Engine API; mirroring could later be a registry-side resource in a Hub provider. |
| skip | - |  | `docker extension --help`<br>`docker --help` |  | - · cli | skip: Docker Desktop extension manager; not Engine API. |
| skip | - |  | `docker init --help`<br>`docker --help` |  | - · cli | skip: Interactive local file scaffolding; no daemon involved. |
| skip | - |  | `docker mcp --help`<br>`docker --help` |  | - · cli | skip: Docker Desktop MCP product; own gateway/catalog APIs, not Engine API (gateway runs MCP servers as containers through Docker but via its own logic). |
| skip | - |  | `docker model --help`<br>`docker --help` |  | - · cli | skip: Model Runner has its own HTTP API (OpenAI-compatible, /models, /engines via the Desktop socket or localhost:12434, or a docker/model-runner container on plain Engine), not the Engine API. Plausible future provider surface (docker_model resource for pull/rm, install-runner as a container), but it is a separate API; out of scope for Engine-API parity. |
| skip | - |  | `docker offload --help`<br>`docker --help` |  | - · cli | skip: Docker cloud product session; not Engine API (it switches the daemon endpoint). |
| skip | - |  | `docker pass --help`<br>`docker --help` |  | - · cli | skip: Local/Desktop secrets store; Terraform has its own secret handling (write-only args, ephemeral). |
| skip | - |  | `docker sandbox --help`<br>`docker --help` |  | - · cli | skip: Docker Desktop sandbox product with its own VM backend API, not Engine API. |
| skip | - |  | `docker scout --help`<br>`docker --help` |  | - · cli | skip: Docker Scout SaaS/analysis tool; not Engine API. Vulnerability data could later be a data source in a Scout/Hub provider. |

## Outputs

The outputs contract ([CONTRACT.md](CONTRACT.md)): 159 items, 1156 outputs, from [_gen/fragments/outputs/](_gen/fragments/outputs/). `*` = sensitive, `(a.b*)` = only those nested fields are sensitive, `+` = known after apply. Full detail (types, sources, field maps) is in inventory.json.

### Engine

| kind | name | raw | structured | result | identity | version | hash | outputs status |
|---|---|---|---|---|---|---|---|---|
| resource | `docker_container` | `inspect_json`*+ | `inspect`(config.env*)+ |  | `id`+, `name`+, `image_id`+, `labels`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Exposed today: id, name, image_id, labels. Missing: inspect_json, inspect, engine_api_version, docker_server_version, content_sha256. |
| resource | `docker_image` | `dockerfile`*, `inspect_json`*+ | `stages`(instructions.env, instructions.arg, instructions.created_by*)+, `inspect`(config.env*)+ |  | `id`+, `name`, `image_ref`, `image_digest_ref`+, `digest`+, `platforms`+, `labels`+, `context_digest` | `dockerfile_syntax`, `engine_api_version`+, `docker_server_version`+ | `dockerfile_sha256`, `inspect_json_sha256`+ | partial: Today: id, image_id (dup of id), name, repo_digest (= image_digest_ref), context_digest. Missing: image_ref, digest, platforms, labels, dockerfile, stages, inspect_json, inspect, both hashes, dockerfile_syntax, engine_api_version, docker_server_version. No buildx_version: docker_image builds through the Engine API /build (classic or BuildKit session), not buildx. |
| resource | `docker_network` | `inspect_json`+ | `inspect`+ |  | `id`+, `name`+, `labels`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Exposed today: id, name, labels. Missing: inspect_json, inspect, engine_api_version, docker_server_version, content_sha256. |
| resource | `docker_plugin` | `inspect_json`*+ | `inspect`(settings.env, config.env*)+ |  | `id`+, `name`+, `plugin_reference`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Exposed today: id, name, plugin_reference. Missing: inspect_json, inspect, engine_api_version, docker_server_version, content_sha256. |
| resource | `docker_registry_image` | `distribution_json`+ | `distribution`+ |  | `name`, `image_ref`, `digest`+, `image_digest_ref`+, `platforms`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Exposed today: name. Missing: image_ref, digest, image_digest_ref, platforms, distribution_json, distribution, engine_api_version, docker_server_version, content_sha256. |
| resource | `docker_tag` | `inspect_json`+ | `inspect`+ |  | `id`+, `image_ref`, `repo_digests`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Exposed today: id. Missing: image_ref, repo_digests, inspect_json, inspect, engine_api_version, docker_server_version, content_sha256. |
| resource | `docker_volume` | `inspect_json`*+ | `inspect`(options*)+ |  | `name`+, `labels`+, `mountpoint`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Exposed today: name, labels, mountpoint. Missing: inspect_json, inspect, engine_api_version, docker_server_version, content_sha256. |
| data source | `docker_container` | `inspect_json`* | `inspect`(config.env*) |  | `id`, `name`, `image_id`, `labels` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_container data source today (inventory status missing). |
| data source | `docker_container_changes` | `changes_json` | `changes` |  | `container_id` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_container_changes data source today (inventory status missing). |
| data source | `docker_container_file` | `stat_json`, `content_base64`* | `stat` |  | `container_id`, `path` | `engine_api_version`, `docker_server_version` | `stat_json_sha256`, `content_base64_sha256` | missing: No docker_container_file data source today (inventory status missing). |
| data source | `docker_container_logs` | `logs`* | `log_entries`(message*) |  | `container_id` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: Exposed today: none. Missing: container_id, logs, log_entries, content_sha256, engine_api_version, docker_server_version, and the tail/since inputs. |
| data source | `docker_container_stats` | `stats_json` | `stats` |  | `id`, `name` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_container_stats data source today (inventory status missing). |
| data source | `docker_container_top` | `top_json` | `top` |  | `container_id` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_container_top data source today (inventory status missing). |
| data source | `docker_containers` | `containers_json` | `containers` |  | `ids` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Exposed today: containers. Missing: ids, containers_json, engine_api_version, docker_server_version, content_sha256. |
| data source | `docker_image` | `inspect_json`* | `manifests`, `config`(env*), `inspect`(config.env*) | `created` | `id`, `name`, `image_ref`, `image_digest_ref`, `digest`, `platforms`, `labels`, `repo_tags` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Today: id, image_id, name, repo_digest (= image_digest_ref), repo_tags, labels. Missing: image_ref, digest, platforms, created, manifests, config, inspect, inspect_json, content_sha256, engine_api_version, docker_server_version. Shares identity/structured/version shape with data.docker_registry_image; local-only fields: labels, created, repo_tags, config, manifests[].digest/media_type/size/available. |
| data source | `docker_image_attestations` | `attestations_json`* | `attestations`(descriptor.data, statement*) |  | `image_ref`, `digests` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_image_attestations data source today (inventory status missing). |
| data source | `docker_image_history` | `history_json`* | `history`(created_by*) |  | `id` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_image_history data source today (inventory status missing). |
| data source | `docker_image_search` | `results_json` | `results` |  | `names` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_image_search data source today (inventory status missing). |
| data source | `docker_images` | `images_json` | `images` |  | `ids` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_images data source today (inventory status partial). |
| data source | `docker_info` | `info_json` | `info` |  | `id`, `name` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_info data source today (inventory status missing). |
| data source | `docker_network` | `inspect_json` | `inspect` |  | `id`, `name`, `labels` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Exposed today: id, name, labels. Missing: inspect_json, inspect, engine_api_version, docker_server_version, content_sha256. |
| data source | `docker_networks` | `networks_json` | `networks` |  | `ids` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_networks data source today (inventory status partial). |
| data source | `docker_plugin` | `inspect_json`* | `inspect`(settings.env, config.env*) |  | `id`, `name`, `plugin_reference` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Exposed today: id, name, plugin_reference. Missing: inspect_json, inspect, engine_api_version, docker_server_version, content_sha256. |
| data source | `docker_plugins` | `plugins_json`* | `plugins`(settings.env, config.env*) |  | `ids` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_plugins data source today (inventory status missing). |
| data source | `docker_registry_image` | `inspect_json` | `manifests`, `config`(env*), `inspect` | `created` | `id`, `name`, `image_ref`, `image_digest_ref`, `digest`, `platforms`, `labels` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Today: name, id (`name@digest`, ~ image_digest_ref), sha256_digest (= digest). Missing: image_ref, image_digest_ref, platforms, manifests, inspect, inspect_json, content_sha256, versions. labels, created and config stay null on this side: DistributionInspect returns only the descriptor and the platform list, not the image config or per-platform digests. |
| data source | `docker_registry_image_tags` | `tags_json` | `tag_list` |  | `name`, `tags` | `distribution_api_version`+ | `content_sha256` |  |
| data source | `docker_system_df` | `df_json` | `df` |  | `id` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_system_df data source today (inventory status missing). |
| data source | `docker_version` | `version_json` | `version` |  | `id`, `platform_name` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_version data source today (inventory status missing). |
| data source | `docker_volume` | `inspect_json`* | `inspect`(options*) |  | `name`, `labels`, `mountpoint` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Exposed today: name, labels, mountpoint. Missing: inspect_json, inspect, engine_api_version, docker_server_version, content_sha256. |
| data source | `docker_volumes` | `volumes_json`* | `volumes`(volumes.options*) |  | `names` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_volumes data source today (inventory status partial). |
| action | `docker_builder_prune` |  |  | `result`+ | `caches_deleted`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: partial). |
| action | `docker_container_cp` |  |  | `result`+ | `container_id`+, `path`+, `archive_sha256`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: partial). |
| action | `docker_container_export` |  |  | `result`+ | `container_id`+, `path`+, `archive_sha256`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: exists). |
| action | `docker_container_kill` |  |  | `result`+ | `container_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_container_pause` |  |  | `result`+ | `container_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_container_prune` |  |  | `result`+ | `containers_deleted`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: partial). |
| action | `docker_container_restart` |  |  | `result`+ | `container_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_container_start` |  |  | `result`+ | `container_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_container_stop` |  |  | `result`+ | `container_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_container_unpause` |  |  | `result`+ | `container_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_container_wait` |  |  | `result`+ | `container_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_exec` | `stdout`*+, `stderr`*+ |  | `result`+ | `exec_id`+, `container_id`+ | `engine_api_version`+, `docker_server_version`+ | `stdout_sha256`+, `stderr_sha256`+ | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: exists). |
| action | `docker_image_commit` |  |  | `result`+ | `id`+, `image_ref`+, `container_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_image_import` |  |  | `result`+ | `id`+, `image_ref`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: exists). |
| action | `docker_image_load` |  |  | `result`+ | `loaded_images`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: exists). |
| action | `docker_image_prune` |  |  | `result`+ | `images_deleted`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: exists). |
| action | `docker_image_save` |  |  | `result`+ | `images`+, `path`+, `archive_sha256`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: exists). |
| action | `docker_network_prune` |  |  | `result`+ | `networks_deleted`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: partial). |
| action | `docker_plugin_create` |  |  | `result`+ | `name`+, `id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_plugin_push` |  |  | `result`+ | `name`+, `digest`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: missing). |
| action | `docker_system_prune` |  |  | `result`+ | `containers_deleted`+ | `engine_api_version`+, `docker_server_version`+ |  | partial: Reports space reclaimed in progress today. |
| action | `docker_volume_prune` |  |  | `result`+ | `volumes_deleted`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: Terraform actions have no result attributes; none of these values is exposed today (status of the action itself: partial). |
| ephemeral | `docker_exec_output` | `stdout`*+, `stderr`*+ |  | `result`+ | `exec_id`+, `container_id`+ | `engine_api_version`+, `docker_server_version`+ | `stdout_sha256`+, `stderr_sha256`+ |  |
| ephemeral | `docker_registry_login` | `auth_json`* | `auth`(identity_token*) |  | `server_address`, `username` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No docker_registry_auth data source today (inventory status partial). |
| ephemeral | `docker_registry_token` |  | `token`(token*)+ |  | `registry` | `distribution_api_version`+ |  |  |
| list resource | `docker_container` |  |  |  |  |  |  |  |
| list resource | `docker_image` |  |  |  |  |  |  |  |
| list resource | `docker_network` |  |  |  |  |  |  |  |
| list resource | `docker_volume` |  |  |  |  |  |  |  |
| function | `image_ref` |  |  |  |  |  |  |  |
| function | `parse_image_ref` |  |  |  |  |  |  |  |
| function | `port_spec` |  |  |  |  |  |  |  |

### Swarm

| kind | name | raw | structured | result | identity | version | hash | outputs status |
|---|---|---|---|---|---|---|---|---|
| resource | `docker_config` | `inspect_json`+ | `inspect`+ |  | `id`+, `name`+, `labels`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Today: id plus the name/labels/data arguments. Missing: inspect_json, inspect, content_sha256, version_index, engine_api_version, docker_server_version. known_after_apply true: computed by create. |
| resource | `docker_node` | `inspect_json`+ | `inspect`+ |  | `id`+, `name`+, `labels`+, `version_index`+, `hostname`+, `role`+, `addr`+ | `engine_api_version`, `docker_server_version` | `content_sha256`+ | missing: Pass 5 (Q4): same outputs as data.docker_node. |
| resource | `docker_secret` | `inspect_json`+ | `inspect`+ |  | `id`+, `name`+, `labels`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Today: id plus the name/labels/data arguments (data is the sensitive input, not an output). Missing: inspect_json, inspect, content_sha256, version_index, version outputs. The inspect output carries no secret value (the Engine never returns Spec.Data). |
| resource | `docker_service` | `inspect_json`*+ | `inspect`(spec.task_template.container_spec.env, spec.task_template.container_spec.mounts.volume_options.driver_config.options*)+, `published_ports`+ |  | `id`+, `name`+, `labels`+, `version_index`+, `image_digest_ref`+, `replicas`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Today: id plus the input spec attributes (some Computed, e.g. endpoint_spec ports, restart_policy). Missing: inspect_json, inspect, content_sha256, version_index, image_digest_ref, published_ports, version outputs. inspect_json is sensitive; inspect is sensitive only at ContainerSpec.Env (field-level); image_digest_ref, replicas and published_ports are non-sensitive copies so consumers need no nonsensitive(). |
| resource | `docker_swarm_init` | `inspect_json`+ | `inspect`+, `join_tokens`*+ |  | `id`+, `cluster_id`+, `labels`+, `version_index`+, `worker_join_token`*+, `manager_join_token`*+, `node_id`+, `unlock_key`*+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Today: id, cluster_id, node_id, worker_join_token, manager_join_token, unlock_key, data_path_port, default_addr_pool, subnet_size. Missing: inspect_json, inspect, join_tokens, content_sha256, labels, version_index, version outputs. Today's attribute for the cluster ID is cluster_id; this contract uses id = ClusterInfo.ID (see naming question). |
| resource | `docker_swarm_node` | `inspect_json`+ | `inspect`+ |  | `id`+, `name`+, `labels`+, `version_index`+, `hostname`+, `role`+, `addr`+ | `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Today: id, node_id, hostname, role, availability. Missing: inspect_json, inspect, content_sha256, name, labels, version_index, addr, version outputs. Values come from NodeInspect on a manager after SwarmJoin. |
| data source | `docker_config` | `inspect_json` | `inspect` |  | `id`, `name`, `labels`, `version_index` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: Type does not exist yet. known_after_apply false (reads an existing object; unknown only when its id/name argument comes from a not-yet-applied resource). |
| data source | `docker_configs` | `configs_json` | `configs` |  | `ids`, `names` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Today: a list of configs with id/name/labels. Missing: configs_json, full per-object fields, content_sha256, ids/names, version outputs. known_after_apply false. |
| data source | `docker_node` | `inspect_json` | `inspect` |  | `id`, `name`, `labels`, `version_index`, `hostname`, `role`, `addr` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Today: id, hostname, role, availability, state, addr, labels, engine_version, leader. Missing: inspect_json, inspect, content_sha256, name, version_index, version outputs. known_after_apply false. |
| data source | `docker_nodes` | `nodes_json` | `nodes` |  | `ids`, `names` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Today: nodes list with id, hostname, role, availability, state, addr, labels, engine_version, leader. Missing: nodes_json, full Node fields (Spec/Description/Status/ManagerStatus), content_sha256, ids/names, version outputs. names = Description.Hostname (Spec.Name is usually empty). known_after_apply false. |
| data source | `docker_secret` | `inspect_json` | `inspect` |  | `id`, `name`, `labels`, `version_index` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: Type does not exist yet. known_after_apply false (see swarm:config-data). |
| data source | `docker_secrets` | `secrets_json` | `secrets` |  | `ids`, `names` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Today: a list of secrets with id/name/labels. Missing: secrets_json, full per-object fields, content_sha256, ids/names, version outputs. known_after_apply false. |
| data source | `docker_service` | `inspect_json`* | `inspect`(spec.task_template.container_spec.env, spec.task_template.container_spec.mounts.volume_options.driver_config.options*), `published_ports` |  | `id`, `name`, `labels`, `version_index`, `image_digest_ref`, `replicas` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: Type does not exist yet. known_after_apply false. |
| data source | `docker_service_logs` | `logs`* | `log_entries`(message*) |  | `id` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: Type does not exist yet. Sensitive by default (log content). known_after_apply false. |
| data source | `docker_services` | `services_json`* | `services`(spec.task_template.container_spec.env, spec.task_template.container_spec.mounts.volume_options.driver_config.options*) |  | `ids`, `names` | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Today: a list of services with id/name/labels. Missing: services_json, the full spec chain, content_sha256, ids/names, version outputs. services_json sensitive; services only at ContainerSpec.Env (field-level); ids/names are not. known_after_apply false. |
| data source | `docker_swarm` | `inspect_json` | `inspect`, `join_tokens`* |  | `id`, `labels`, `version_index`, `worker_join_token`*, `manager_join_token`* | `engine_api_version`, `docker_server_version` | `content_sha256` | partial: Today only data.docker_swarm_tokens (id, worker, manager) and ephemeral.docker_swarm_tokens. Missing: inspect_json, inspect, join_tokens, content_sha256, labels, version_index, version outputs. Tokens read by a data source land in state (sensitive); the ephemeral resource stays the preferred way to get them. known_after_apply false. |
| data source | `docker_swarm_tokens` | none: Only the join tokens; the whole SwarmInspect response is data.docker_swarm's inspect_json. | `join_tokens`* |  | `id` | `engine_api_version`+, `docker_server_version`+ |  |  |
| data source | `docker_task` | `inspect_json`* | `inspect`(spec.container_spec.env, spec.container_spec.mounts.volume_options.driver_config.options*) |  | `id`, `name`, `labels`, `service_id`, `node_id`, `slot`, `container_id`, `version_index` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: Type does not exist yet. known_after_apply false. |
| data source | `docker_task_logs` | `logs`* | `log_entries`(message*) |  | `id` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: Type does not exist yet. Sensitive by default (log content). known_after_apply false. |
| data source | `docker_tasks` | `tasks_json`* | `tasks`(spec.container_spec.env, spec.container_spec.mounts.volume_options.driver_config.options*) |  | `ids`, `names` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: Type does not exist yet. tasks_json sensitive; tasks only at ContainerSpec.Env (field-level); ids/names are not. known_after_apply false. |
| action | `docker_node_activate` |  |  | `result`+ | `node_id`+, `hostname`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ |  | partial: Action exists; actions return no attributes today. Changed value: Spec.Availability = active. |
| action | `docker_node_demote` |  |  | `result`+ | `node_id`+, `hostname`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: No action yet (role is an argument of docker_swarm_node). Changed value: Spec.Role = worker. |
| action | `docker_node_drain` |  |  | `result`+ | `node_id`+, `hostname`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ |  | partial: Action exists; actions return no attributes today. Changed value: Spec.Availability = drain. |
| action | `docker_node_promote` |  |  | `result`+ | `node_id`+, `hostname`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: No action yet (role is an argument of docker_swarm_node). Changed value: Spec.Role = manager. |
| action | `docker_service_force_update` |  |  | `result`+ | `service_id`+, `name`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ |  | partial: Action exists; returns no attributes today. Changed value: TaskTemplate.ForceUpdate. |
| action | `docker_service_rollback` |  |  | `result`+ | `service_id`+, `name`+, `version_index`+, `image_digest_ref`+ | `engine_api_version`+, `docker_server_version`+ |  | partial: Action exists; returns no attributes today. Rollback is ServiceUpdate with rollback=previous; the whole spec reverts. |
| action | `docker_service_scale` |  |  | `result`+ | `service_id`+, `name`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ |  | partial: Action exists; returns no attributes today. Changed value: Spec.Mode.Replicated.Replicas. |
| action | `docker_swarm_rotate_ca` |  |  | `result`+ | `id`+, `version_index`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: No action yet. SwarmUpdate with Spec.CAConfig.ForceRotate incremented; a supplied signing key is a sensitive input and never returned. |
| action | `docker_swarm_rotate_tokens` |  |  | `result`(join_tokens*)+ | `id`+, `version_index`+, `worker_join_token`*+, `manager_join_token`*+ | `engine_api_version`+, `docker_server_version`+ |  | partial: Action exists; returns no attributes today (new tokens are read with ephemeral.docker_swarm_tokens). Token outputs must stay sensitive. |
| action | `docker_swarm_rotate_unlock_key` |  |  | `result`+ | `id`+, `version_index`+, `unlock_key`*+ | `engine_api_version`+, `docker_server_version`+ |  | missing: No action yet. SwarmUpdate with rotateManagerUnlockKey=true, then SwarmUnlockkey. |
| action | `docker_swarm_unlock` |  |  | `result`+ | `node_id`+ | `engine_api_version`+, `docker_server_version`+ |  | missing: No action yet. SwarmUnlock returns no body; outputs come from GET /info afterwards. The unlock key is a sensitive input, never an output. |
| ephemeral | `docker_swarm_tokens` |  | `join_tokens`* |  | `id` | `engine_api_version`+, `docker_server_version`+ |  |  |
| list resource | `docker_config` |  |  |  |  |  |  |  |
| list resource | `docker_secret` |  |  |  |  |  |  |  |
| list resource | `docker_service` |  |  |  |  |  |  |  |
| list resource | `docker_swarm_node` |  |  |  |  |  |  |  |

### Compose

| kind | name | raw | structured | result | identity | version | hash | outputs status |
|---|---|---|---|---|---|---|---|---|
| resource | `docker_compose` | none: Facet of docker_compose: the raw output is `compose_yaml` on compose:name (the item that represents the whole project); this facet's slice is serialized inside it, not separately. | `project`(configs.content, configs.environment*) |  | `configs`+ | `compose_spec_version`, `compose_go_version`, `engine_api_version`+ |  | missing |
| resource | `docker_compose` | none: Facet of docker_compose: the raw output is `compose_yaml` on compose:name (the item that represents the whole project); this facet's slice is serialized inside it, not separately. | `project`(include.env_file*) |  | `config_files` | `compose_spec_version`, `compose_go_version` |  | missing |
| resource | `docker_compose` | none: Facet of docker_compose: the raw output is `compose_yaml` on compose:name (the item that represents the whole project); this facet's slice is serialized inside it, not separately. | `project`(jobs.env_file, jobs.environment, jobs.secrets, jobs.build.dockerfile_inline, jobs.build.args, jobs.build.ssh, jobs.build.secrets*) |  | `jobs`+ | `compose_spec_version`, `compose_go_version`, `engine_api_version`+ |  | missing |
| resource | `docker_compose` | none: Facet of docker_compose: the raw output is `compose_yaml` on compose:name (the item that represents the whole project); this facet's slice is serialized inside it, not separately. | `project` |  | `models` | `compose_spec_version`, `compose_go_version` |  | missing |
| resource | `docker_compose` | `compose_yaml`* | `project`(include.env_file, services.env_file, services.environment, services.secrets, services.build.dockerfile_inline, services.build.args, services.build.ssh, services.build.secrets, volumes.driver_opts, secrets.environment, configs.content, configs.environment, jobs.env_file, jobs.environment, jobs.secrets, jobs.build.dockerfile_inline, jobs.build.args, jobs.build.ssh, jobs.build.secrets*) |  | `id`+, `name`, `labels`, `config_files` | `compose_spec_version`, `compose_go_version`, `engine_api_version`+, `docker_server_version`+ | `content_sha256` | partial: Today: id, project_name and spec_hash (a digest of the parsed project, not of compose_yaml). Missing: compose_yaml, content_sha256, project, labels, config_files and all version outputs. |
| resource | `docker_compose` | none: Facet of docker_compose: the raw output is `compose_yaml` on compose:name (the item that represents the whole project); this facet's slice is serialized inside it, not separately. | `project` |  | `networks`+, `network_names` | `compose_spec_version`, `compose_go_version`, `engine_api_version`+ |  | partial: Today: `networks` (key -> ID). Missing: project.networks, network_names, versions. |
| resource | `docker_compose` | none: Facet of docker_compose: the raw output is `compose_yaml` on compose:name (the item that represents the whole project); this facet's slice is serialized inside it, not separately. | `project`(secrets.environment*) |  | `secrets`+ | `compose_spec_version`, `compose_go_version`, `engine_api_version`+ |  | missing |
| resource | `docker_compose` | none: Facet of docker_compose: the raw output is `compose_yaml` on compose:name (the item that represents the whole project); this facet's slice is serialized inside it, not separately. | `project`(services.env_file, services.environment, services.secrets, services.build.dockerfile_inline, services.build.args, services.build.ssh, services.build.secrets*) |  | `services`+, `container_names`+, `image_refs` | `compose_spec_version`, `compose_go_version`, `engine_api_version`+ |  | partial: Today: `services` (name -> container IDs / service ID). Missing: project.services, container_names, image_refs, versions. |
| resource | `docker_compose` | none: Facet of docker_compose: the raw output is `compose_yaml` on compose:name (the item that represents the whole project); this facet's slice is serialized inside it, not separately. | `project`(volumes.driver_opts*) |  | `volumes`+ | `compose_spec_version`, `compose_go_version`, `engine_api_version`+ |  | partial: Today: `volumes` (key -> name). Missing: project.volumes, versions. |
| resource | `docker_image` | none: The build section is serialized inside compose_yaml (compose:name); docker_image's own inspect_json belongs to the engine image item. | `build`(dockerfile_inline, args, ssh, secrets*) |  | `id`+, `image_ref`, `image_digest_ref`+, `platforms` | `engine_api_version`+ |  | Pass 5 Lane Z: the compose version outputs were dropped here; they describe compose parsing, which docker_image does not do (docker_compose carries them). |
| resource | `docker_service` | none: The deploy section is serialized inside compose_yaml (compose:name); docker_service's own inspect_json belongs to the swarm service item. | `deploy` |  | `id`+, `name`, `labels` | `compose_spec_version`, `compose_go_version`, `engine_api_version`+ |  |  |
| data source | `docker_compose_config` | `compose_yaml`* | `project`(include.env_file, services.env_file, services.environment, services.secrets, services.build.dockerfile_inline, services.build.args, services.build.ssh, services.build.secrets, volumes.driver_opts, secrets.environment, configs.content, configs.environment, jobs.env_file, jobs.environment, jobs.secrets, jobs.build.dockerfile_inline, jobs.build.args, jobs.build.ssh, jobs.build.secrets*) |  | `name`, `service_names`, `image_refs` | `compose_spec_version`, `compose_go_version` | `content_sha256` | missing |
| data source | `docker_compose_containers` | `containers_json`*+ | `containers`+ |  | `name`, `container_ids`+ | `engine_api_version`, `docker_server_version` | `content_sha256` | missing |
| data source | `docker_compose_images` | `images_json`+ | `images`+ |  | `name`, `image_refs`+, `image_digest_refs`+, `image_ids`+ | `engine_api_version`, `docker_server_version` | `content_sha256` | missing |
| data source | `docker_compose_logs` | `logs`*+ | `log_entries`(message*)+ |  | `name`, `container_ids`+ | `engine_api_version`, `docker_server_version` | `content_sha256`+ | missing |
| data source | `docker_compose_port` | none: The answer is one port binding; `bindings` is the whole result and has no separate serialized form upstream. | `bindings`+ |  | `host_ip`+, `published_port`+, `container_id`+ | `engine_api_version`, `docker_server_version` |  | missing |
| data source | `docker_compose_projects` | `projects_json`+ | `projects`+ |  | `names`+ | `engine_api_version`, `docker_server_version` | `content_sha256` | missing |
| data source | `docker_compose_top` | `top_json`*+ | `processes`+ |  | `name`, `container_ids`+ | `engine_api_version`, `docker_server_version` | `content_sha256` | missing |
| data source | `docker_compose_volumes` | `volumes_json`+ | `volumes`+ |  | `name`, `names`+ | `engine_api_version`, `docker_server_version` | `content_sha256` | missing |
| action | `docker_compose_build` |  |  | `result`+ | `name`+, `service_names`+, `image_refs`+, `image_ids`+ | `compose_spec_version`, `compose_go_version`, `engine_api_version`+ |  | missing |
| action | `docker_compose_commit` |  |  | `result`+ | `name`+, `container_id`+, `id`+, `image_ref`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_cp` |  |  | `result`+ | `name`+, `container_id`+, `path`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_exec` |  |  | `result`(stdout, stderr*)+ | `name`+, `container_id`+, `exec_id`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_export` |  |  | `result`+ | `name`+, `container_id`+, `path`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_kill` |  |  | `result`+ | `name`+, `service_names`+, `container_ids`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_pause` |  |  | `result`+ | `name`+, `service_names`+, `container_ids`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_publish` |  |  | `result`+ | `name`+, `image_ref`+, `image_digest_ref`+, `digest`+ | `compose_spec_version`, `compose_go_version` |  | missing |
| action | `docker_compose_pull` |  |  | `result`+ | `name`+, `service_names`+, `image_digest_refs`+, `image_ids`+ | `compose_go_version`, `engine_api_version`+ |  | missing |
| action | `docker_compose_push` |  |  | `result`+ | `name`+, `service_names`+, `image_refs`+, `image_digest_refs`+ | `compose_go_version`, `engine_api_version`+ |  | missing |
| action | `docker_compose_restart` |  |  | `result`+ | `name`+, `service_names`+, `container_ids`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_run` |  |  | `result`(stdout, stderr*)+ | `name`+, `container_id`+, `container_name`+ | `compose_go_version`, `engine_api_version`+ |  | missing |
| action | `docker_compose_scale` |  |  | `result`+ | `name`+, `container_ids`+, `replicas`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_start` |  |  | `result`+ | `name`+, `service_names`+, `container_ids`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_stop` |  |  | `result`+ | `name`+, `service_names`+, `container_ids`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_unpause` |  |  | `result`+ | `name`+, `service_names`+, `container_ids`+ | `engine_api_version`+ |  | missing |
| action | `docker_compose_wait` |  |  | `result`+ | `name`+, `service_names`+ | `engine_api_version`+ |  | missing |
| function | `compose_env` |  |  |  |  |  |  |  |

### Dockerfile

| kind | name | raw | structured | result | identity | version | hash | outputs status |
|---|---|---|---|---|---|---|---|---|
| data source | `docker_dockerfile` | `dockerfile`*, `source_dockerfile`* | `stages`(instructions.env, instructions.arg, instructions.created_by*), `directives`, `global_args`(default*), `exposed_ports`, `base_images`, `history`(created_by*) | `fidelity` | `id`, `source_type`, `git_commit`, `image_digest_ref`, `stage_names`, `final_stage`, `labels` | `dockerfile_syntax`, `engine_api_version`, `docker_server_version` | `dockerfile_sha256`, `source_dockerfile_sha256` | missing: The data source does not exist (no docker_dockerfile in internal/provider). |
| block | `docker_dockerfile.add` | `dockerfile` | `add` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.arg` | `dockerfile`* | `arg`(args.default*) |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.check` | `dockerfile` | `check` |  | `directive` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet; directives would be top-level attributes of data.docker_dockerfile. |
| block | `docker_dockerfile.cmd` | `dockerfile` | `cmd` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.copy` | `dockerfile` | `copy` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.entrypoint` | `dockerfile` | `entrypoint` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.env` | `dockerfile`* | `env`(env*) |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.escape` | `dockerfile` | `escape` |  | `directive` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet; directives would be top-level attributes of data.docker_dockerfile. |
| block | `docker_dockerfile.expose` | `dockerfile` | `expose` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.healthcheck` | `dockerfile` | `healthcheck` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.label` | `dockerfile` | `label` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.maintainer` | `dockerfile` | `maintainer` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.onbuild` | `dockerfile` | `onbuild` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.run` | `dockerfile` | `run` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.shell` | `dockerfile` | `shell` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.stage` | `dockerfile` | `from` |  | `instruction`, `stage_index`, `instruction_index`, `stage_name` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.stopsignal` | `dockerfile` | `stopsignal` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.syntax` | `dockerfile` | `syntax` |  | `directive` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet; directives would be top-level attributes of data.docker_dockerfile. |
| block | `docker_dockerfile.user` | `dockerfile` | `user` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.volume` | `dockerfile` | `volume` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |
| block | `docker_dockerfile.workdir` | `dockerfile` | `workdir` |  | `instruction`, `stage_index`, `instruction_index` | `dockerfile_syntax` | `content_sha256` | missing: No Dockerfile composer exists yet (see data.docker_dockerfile); docker_image only takes a Dockerfile path. |

### Buildx

| kind | name | raw | structured | result | identity | version | hash | outputs status |
|---|---|---|---|---|---|---|---|---|
| resource | `docker_buildx_builder` | `inspect_json`*+ | `builder`(nodes.driver_opts*)+ |  | `id`+, `name`, `driver`, `endpoint`+, `platforms`+, `labels`+ | `buildkit_version`+, `buildx_version`, `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Today: id, name, driver, endpoint, tcp_address, container_name, volume_name, buildkit_version, platforms (first node). Missing: inspect_json, builder (nodes, driver_opts, status, flags, labels, gc_policy, last_activity), labels, the three version outputs. Gaps vs `buildx inspect`: the '*' marker for manually pinned platforms and the --debug extra view. |
| resource | `docker_image` | `metadata_json`*+, `dockerfile`+ | `build_metadata`(buildx_build_provenance*)+ |  | `id`+, `image_ref`, `image_digest_ref`+, `digest`+, `platforms`+, `labels`+, `build_ref`+ | `buildx_version`, `dockerfile_syntax`+, `engine_api_version`+, `docker_server_version`+ | `metadata_json_sha256`+, `dockerfile_sha256`+ | partial: Today docker_image exposes id, image_id, repo_digest (= image_digest_ref), context_digest. Missing: metadata_json/build_metadata, dockerfile text, digest, platforms, labels, build_ref, the version outputs. Image inspect outputs (inspect_json) belong to the engine image item, not here. |
| data source | `docker_buildx_builder` | `inspect_json`* | `builder`(nodes.driver_opts*) |  | `id`, `name`, `driver`, `endpoint`, `platforms`, `labels` | `buildkit_version`, `buildx_version`, `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No data source today. Builders not created by Terraform are discoverable only as buildx_buildkit_* containers (buildx's client-side store is not read), so last_activity may be empty for them. |
| data source | `docker_buildx_builders` | `builders_json`* | `builders`(nodes.driver_opts*) |  | `names`, `endpoints` | `buildx_version`, `engine_api_version`, `docker_server_version` | `content_sha256` | missing: No data source today. The current-builder '*' marker of `buildx ls` is client-side state (~/.docker/buildx) and has no output. |
| data source | `docker_buildx_disk_usage` | `disk_usage_json` | `records`, `summary` |  | `builder`, `ids` | `buildx_version`, `engine_api_version` | `content_sha256` | missing: No data source today (docker_system_df / Z1 may expose the daemon BuildCache section). Mutable and Reclaimable are buildx terms; the Engine BuildCache model has InUse instead. |
| data source | `docker_buildx_history` | `inspect_json`* | `record`(context, vcs_repository, config, materials.uri*) |  | `ref`, `name`, `platforms`, `vcs_revision` | `buildx_version`, `dockerfile_syntax` | `content_sha256` | missing: No data source today. Gap: attachment content (provenance/SBOM bodies, `docker buildx history inspect attachment`) is not an output; only attachment digests/types. Config holds more keys than ImageResolveMode in practice (fields_complete false). |
| data source | `docker_buildx_history_records` | `history_json` | `records` |  | `builder`, `refs` | `buildx_version` | `content_sha256` | missing |
| data source | `docker_registry_image_manifests` | `manifest_json`, `image_config_json`*, `provenance_json`*, `sbom_json` | `manifest`, `image_config`(config, history.created_by*) |  | `image_ref`, `image_digest_ref`, `digest`, `media_type`, `platforms` | `buildx_version` | `manifest_json_sha256`, `image_config_json_sha256`, `provenance_json_sha256`, `sbom_json_sha256` | partial: Today: digest, media_type, manifests (flattened: digest, media_type, size, os, architecture, variant, os_version). Missing: manifest_json, image_config_json/image_config, provenance_json, sbom_json, image_ref, image_digest_ref, platforms, buildx_version; manifests[] lacks annotations, urls, artifact_type, os.features. Same Terraform type as cli-only:manifest-inspect, same outputs. Existing `name` is the input reference, so the normalized ref is `image_ref`. |
| action | `docker_buildx_bake` | `bake_hcl`*+, `bake_json`*+, `metadata_json`*+ |  | `result`(bake.target.args, bake.target.dockerfile_inline, bake.target.secret, bake.target.ssh, metadata.buildx_build_provenance*)+ | `targets`+, `image_refs`+, `image_digest_refs`+, `digests`+, `platforms`+, `build_refs`+ | `buildx_version`+ | `bake_hcl_sha256`+, `bake_json_sha256`+, `metadata_json_sha256`+ | missing: Action does not exist yet. variable/function blocks are inputs only (Terraform variables replace them) and do not appear in --print output. |

### CLI-only

| kind | name | raw | structured | result | identity | version | hash | outputs status |
|---|---|---|---|---|---|---|---|---|
| resource | `docker_registry_manifest_list` | `manifest_json`+ | `manifest`+ |  | `image_ref`, `image_digest_ref`+, `digest`+, `media_type`+, `platforms`+, `manifest_digests`+ | `buildx_version` | `content_sha256`+ | missing: Type does not exist yet. Talks to the registry v2 HTTP API, so neither engine_api_version nor docker_server_version applies; buildx_version records the `imagetools create` semantics it mirrors. uses_cli false: assembles the index and pushes it through the registry v2 HTTP API, not `docker manifest`; so no docker_cli_version. |
| resource | `docker_stack` | `stack_yaml`*+ | `stack`(services.env_file, services.environment, services.secrets*)+ | `service_status`(spec*)+ | `id`+, `name`, `namespace`, `labels`, `service_ids`+, `network_ids`+, `secret_ids`+, `config_ids`+ | `compose_spec_version`, `engine_api_version`+, `docker_server_version`+ | `content_sha256`+ | partial: Today only id and name (plus the configured blocks). Missing: stack_yaml, stack (compose-spec shaped), service_status, namespace, labels, *_ids maps, versions. stack_yaml is the docker_stack -> compose direction (cli-only:stack-config is skipped as a type, it is only an output here). A whole-namespace Engine inspect_json was dropped to keep one raw (content_sha256); per-object inspect stays with docker_service/docker_network data sources. uses_cli false: deploys through the Engine API (NetworkCreate, ServiceCreate/Update/Delete, secret and config create) with the namespace label; it mirrors `docker stack deploy` but never runs it; so no docker_cli_version. |
| data source | `docker_registry_image_manifests` | `manifest_json`, `image_config_json`*, `provenance_json`*, `sbom_json` | `manifest`, `image_config`(config, history.created_by*) |  | `image_ref`, `image_digest_ref`, `digest`, `media_type`, `platforms` | `buildx_version` | `manifest_json_sha256`, `image_config_json_sha256`, `provenance_json_sha256`, `sbom_json_sha256` | partial: Same type and outputs as buildx:imagetools-inspect (docker_registry_image_manifests). Gap vs `docker manifest inspect --verbose`: for a list, the verbose view also expands every child manifest (its config and layers); the data source only reads the top-level document. The type reads the registry v2 API directly; DistributionInspect (GET /distribution/{name}/json) would give Descriptor+Platforms only. uses_cli false: reads the manifest from the registry v2 HTTP API, not `docker manifest inspect`; so no docker_cli_version. |
| data source | `docker_stack` | `services_json`* | `services`(spec*) |  | `name`, `service_ids`, `images` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: docker_services with a label filter returns services today but no replica counts, no tasks, no raw JSON. uses_cli false: GET /services and GET /tasks filtered by the namespace label; no `docker stack services`; so no docker_cli_version. |
| data source | `docker_stacks` | `stacks_json` | `stacks` |  | `names` | `engine_api_version`, `docker_server_version` | `content_sha256` | missing: uses_cli false: groups GET /services by the com.docker.stack.namespace label; no `docker stack ls`; so no docker_cli_version. |

## Totals

Counts as exists / partial / missing.

| layer | resources | data sources | actions | ephemeral | list resources | functions | blocks | skipped |
|---|---|---|---|---|---|---|---|---|
| engine | 7 (7 / 0 / 0) | 23 (23 / 0 / 0) | 22 (22 / 0 / 0) | 3 (3 / 0 / 0) | 4 (4 / 0 / 0) | 3 (3 / 0 / 0) | 0 | 7 |
| swarm | 6 (6 / 0 / 0) | 14 (14 / 0 / 0) | 11 (11 / 0 / 0) | 1 (1 / 0 / 0) | 4 (4 / 0 / 0) | 0 | 0 | 0 |
| compose | 11 (11 / 0 / 0) | 8 (8 / 0 / 0) | 17 (17 / 0 / 0) | 0 | 0 | 1 (1 / 0 / 0) | 0 | 30 |
| dockerfile | 0 | 1 (1 / 0 / 0) | 0 | 0 | 0 | 0 | 21 (21 / 0 / 0) | 0 |
| buildx | 2 (2 / 0 / 0) | 6 (6 / 0 / 0) | 1 (1 / 0 / 0) | 0 | 0 | 0 | 0 | 23 |
| cli-only | 2 (2 / 0 / 0) | 3 (3 / 0 / 0) | 0 | 0 | 0 | 0 | 0 | 21 |
| **all** | 28 (28 / 0 / 0) | 55 (55 / 0 / 0) | 51 (51 / 0 / 0) | 4 (4 / 0 / 0) | 8 (8 / 0 / 0) | 4 (4 / 0 / 0) | 21 (21 / 0 / 0) | 81 |

Engine API coverage: 108 of 108 swagger operations placed.

## Naming collisions

Shared names below are intentional: the items are facets of one existing type, not competing proposals.

- Shared data name `docker_registry_image_manifests`: cli-only:manifest-inspect, buildx:imagetools-inspect
- Shared resource name `docker_compose`: compose:services, compose:networks, compose:volumes, compose:configs, compose:secrets, compose:models, compose:jobs, compose:include, compose:name
- Shared resource name `docker_image`: engine:image, compose:service-build, buildx:build
- Shared resource name `docker_service`: swarm:service, compose:service-deploy
- `docker_container_file` (engine:container-file): Confusable with docker_container `upload` blocks (the write direction); also consider an action instead if large archives are expected.
- `-` (engine:image-build): Confusable with docker_image (resource whose `build` block already calls ImageBuild) and with docker_buildx_builder.
- `docker_builder_prune` (engine:build-prune): docker_builder_prune (Engine build cache) is easy to confuse with docker_buildx_builder (a BuildKit builder container); docker_build_cache_prune avoids it.
- `docker_registry_login` (engine:registry-auth): Confusable with ephemeral docker_registry_token and with provider registry_auth blocks.
- `-` (engine:network-attachment): Overlaps docker_container `networks`; managing the same pair in both would fight.
- `docker_plugin_create` (engine:plugin-create): Confusable with docker_plugin (install from a registry). PluginCreate has a delete counterpart, so it could also be a resource.
- `docker_swarm_node` (swarm:swarm-node): docker_swarm_node is a *join* resource; a user expecting it to manage an already-joined node (labels/availability only, NodeUpdate) would need a separate type, e.g. docker_node.
- `docker_service_logs` (swarm:service-logs): Could instead extend data.docker_logs with a service_id/task_id argument.
- `docker_task_logs` (swarm:task-logs): Could merge with docker_service_logs or data.docker_logs.
- `docker_config` (swarm:config): docker_config (swarm config object) vs compose top-level `configs` and the provider's own configuration block.
- `docker_config` (swarm:config-data): Same confusion as docker_config resource (swarm config vs compose configs).
- `docker_compose` (compose:services): A per-service type named docker_compose_service would be confusable with docker_service (swarm). Keep services inside docker_compose.
- `docker_compose` (compose:networks): docker_compose_network would be confusable with docker_network.
- `docker_compose` (compose:volumes): docker_compose_volume(s) would be confusable with docker_volume and with the proposed docker_compose_volumes data source (CLI `compose volumes`).
- `docker_compose` (compose:configs): Top-level `configs` vs docker_config (Swarm config resource) vs the proposed docker_compose_config data source (CLI `compose config`, rendered model).
- `docker_compose` (compose:secrets): docker_compose_secret would be confusable with docker_secret.
- `-` (compose:cli-up): docker_compose (resource) already means up; an action docker_compose_up beside it invites confusion.
- `docker_compose_start` (compose:cli-start): Would mirror single-container docker_container_start actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those.
- `docker_compose_stop` (compose:cli-stop): Would mirror single-container docker_container_stop actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those.
- `docker_compose_restart` (compose:cli-restart): Would mirror single-container docker_container_restart actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those.
- `docker_compose_kill` (compose:cli-kill): Would mirror single-container docker_container_kill actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those.
- `docker_compose_pause` (compose:cli-pause): Would mirror single-container docker_container_pause actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those.
- `docker_compose_unpause` (compose:cli-unpause): Would mirror single-container docker_container_unpause actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those.
- `docker_compose_scale` (compose:cli-scale): docker_service_scale (swarm action) already exists; docker_compose_scale is near-identical in purpose.
- `docker_compose_containers` (compose:cli-ps): Name options: docker_compose_ps (CLI-shaped) vs docker_compose_containers (plural-noun convention beside docker_containers).
- `docker_compose_projects` (compose:cli-ls): docker_compose_ls (CLI-shaped) vs docker_compose_projects (noun).
- `docker_compose_config` (compose:cli-config): Confusable with top-level compose `configs` and docker_config (swarm config resource). Alternatives: docker_compose_project, docker_compose_model, or function compose_render.
- `docker_compose_images` (compose:cli-images): Beside data.docker_image (singular) and list.docker_image.
- `docker_compose_volumes` (compose:cli-volumes): docker_compose_volumes vs docker_volume / top-level compose `volumes`.
- `docker_compose_port` (compose:cli-port): Confusable with the port_spec function.
- `-` (cli-only:context): A `docker_context` resource would be confusable with the provider-level `context` argument.
- `docker_registry_manifest_list` (cli-only:manifest-list): Same job as `docker buildx imagetools create` (buildx:imagetools-create points here). Do not also add docker_buildx_imagetools / docker_manifest. `docker manifest rm` only deletes the local draft, so destroy semantics (registry DELETE of the tag vs leave it) need a flag like docker_registry_image.keep_remotely.
- `docker_registry_image_manifests` (cli-only:manifest-inspect): buildx:imagetools-inspect maps to the same data source.
- `docker_stack` (cli-only:stack): Keep `docker_stack` (exists); do not rename to docker_swarm_stack even though it is a swarm-layer type.
- `docker_stack` (cli-only:stack-services): Data source `docker_stack` would share its name with the existing docker_stack resource (allowed by Terraform, same pattern as docker_image); alternative name docker_stack_services.
- `docker_image` (buildx:build): Do not add docker_buildx_build: it would duplicate docker_image. If exporters that produce no local image are wanted, that is an action (docker_buildx_bake or docker_image_build action), not a second build resource.
- `docker_buildx_bake` (buildx:bake): Overlaps docker_image (build block) for single targets.
- `docker_buildx_builder` (buildx:builder-inspect): Same name as the resource (resource/data source pair, allowed). For builders not created by Terraform, discovery is limited to buildx_buildkit_* containers on the daemon (buildx's builder store is client-side).
- `-` (buildx:stop): Low value: docker_buildx_builder with bootstrap=false or a must_run-style flag could cover it; consider skip.
- `-` (buildx:prune): Z1 will likely propose an Engine action for POST /build/prune (e.g. docker_build_cache_prune / docker_builder_prune). One type should take an optional `builder` and choose BuildKit gRPC vs POST /build/prune.
- `docker_buildx_disk_usage` (buildx:du): Overlaps Z1's system df data source (BuildCache section); merge or keep builder-scoped.
- `docker_buildx_history` (buildx:history-inspect): Could be folded into docker_buildx_history as a `ref` filter; naming question.
- `-` (buildx:imagetools-create): docker_buildx_imagetools_create would duplicate docker_registry_manifest_list.
- `docker_registry_image_manifests` (buildx:imagetools-inspect): Same type as cli-only:manifest-inspect.
- `docker_dockerfile.onbuild` (dockerfile:ONBUILD): Nests another instruction block; could instead be an `onbuild` attribute on each docker_dockerfile_* block.

## Naming questions

1. NetworkConnect/NetworkDisconnect: add a standalone `docker_network_attachment` resource (container x network pair), or make docker_container `networks` connect/disconnect in place instead of forcing replacement? (or both, with a conflict warning)
2. Build cache prune action name: `docker_builder_prune` (matches `docker builder prune`) or `docker_build_cache_prune` (avoids confusion with docker_buildx_builder)?
3. Standalone build: add an action `docker_image_build`, or keep builds only in docker_image's `build` block? If added, is the name too close to docker_image?
4. Managing an already-joined swarm node (labels/availability/role via NodeUpdate) that Terraform did not join: extend docker_swarm_node with an adopt mode, or add a separate `docker_node` resource (name matches existing data.docker_node)?
5. Node role actions: `docker_node_promote` / `docker_node_demote` (CLI words, matching docker_node_drain/activate) or one `docker_node_role` action with role = manager\|worker?
6. Logs: separate `docker_service_logs` and `docker_task_logs` data sources, or extend data.docker_logs with service_id / task_id arguments?
7. SystemAuth (/auth) returns an identity token: model as an ephemeral resource (e.g. `docker_registry_login`) next to ephemeral docker_registry_token, or skip it?
8. PluginCreate: action `docker_plugin_create` (one-shot) or a resource (it has PluginDelete) with a different name to avoid confusion with docker_plugin (install from registry)?
9. Container one-off read of files: data source `docker_container_file` (GET archive) vs an action; and copy-in at any time: action `docker_container_upload` or `docker_container_cp`?
10. Swarm rotation actions: separate `docker_swarm_rotate_unlock_key` and `docker_swarm_ca_rotate`, or one `docker_swarm_rotate` action with what = worker_token\|manager_token\|unlock_key\|ca (absorbing docker_swarm_rotate_tokens)?
11. Imperative compose actions beside the docker_compose resource: name them docker_compose_up/down (CLI-shaped), docker_compose_apply/destroy, or skip up/down since the resource covers them?
12. `compose config` render: data source docker_compose_config (visually collides with docker_config and top-level `configs`), docker_compose_project / docker_compose_model, or a provider function compose_render?
13. `compose ps` / `compose ls`: docker_compose_ps + docker_compose_ls (CLI names) or docker_compose_containers + docker_compose_projects (noun convention like docker_containers)?
14. `compose volumes`: add a docker_compose_volumes data source, or document data.docker_volume/list.docker_volume with a project label filter (avoids the docker_volume clash)?
15. Per-container verbs (start/stop/restart/kill/pause/unpause): separate docker_compose_* actions, or one set of docker_container_* actions with an optional project/service selector?
16. `compose scale` vs existing docker_service_scale: new docker_compose_scale action, or extend docker_service_scale to compose local projects?
17. Compose top-level elements: keep all inside docker_compose, or split docker_compose_service/network/volume (confusable with docker_service/docker_network/docker_volume)?
18. jobs: support in docker_compose (run once on apply) plus docker_compose_run for manual triggers, or leave unsupported but add a warning?
19. Manifest lists: `docker_registry_manifest_list` (proposed, registry family) vs `docker_manifest_list` vs `docker_buildx_imagetools`? One resource for both `docker manifest create/push` and `buildx imagetools create`.
20. BuildKit build: keep extending `docker_image` (build block) vs add `docker_buildx_build`? Proposed: extend docker_image, and add action `docker_buildx_bake` for multi-target / non-image outputs.
21. Stack: keep existing `docker_stack` vs a `docker_swarm_stack` prefix for consistency with docker_swarm_*? Proposed: keep docker_stack; new stack data sources `docker_stacks` and `docker_stack` (or `docker_stack_services`).
22. Context: skip `docker_context` (provider `context`/`host` arguments already select the daemon) vs a read-only `docker_context` data source that resolves a context's endpoint/TLS from ~/.docker? Proposed: skip.
23. Build cache prune: one action for both daemon build cache (POST /build/prune, Z1) and buildx builders (BuildKit gRPC): `docker_buildx_prune` vs `docker_build_cache_prune` vs `docker_builder_prune`?
24. Buildx builder data sources: `docker_buildx_builder` (singular, same name as the resource) + `docker_buildx_builders`; buildx history as `docker_buildx_history` (+ `docker_buildx_history_record`) or one data source with an optional ref?
25. docker buildx stop: action `docker_buildx_builder_stop` or skip (low value)?
26. Disk usage: `docker_buildx_disk_usage` separate from Z1's system df data source, or one data source with an optional builder?
27. Shape: (a) one type per instruction (docker_dockerfile_run, docker_dockerfile_from, ...) vs (b) a single data source data.docker_dockerfile with ordered nested blocks (stage { from, run, copy ... }) rendering Dockerfile text vs (c) a provider function. (b) recommended: ordering and stages fit nested blocks; 21 trivial types would bloat the registry.
28. Directives: docker_dockerfile_directive_syntax/escape/check types vs top-level attributes syntax, escape, check { skip = [...], error = bool } on data.docker_dockerfile (recommended).
29. Include MAINTAINER (deprecated) at all, or omit and steer users to LABEL org.opencontainers.image.authors?
30. ONBUILD: separate block nesting an instruction vs an `onbuild = true` attribute on each instruction block?
31. Stages: model FROM ... AS name as a `stage` block with `name` (recommended) rather than a FROM flag?
32. check rule names: validate against a pinned list (21 rules at frontend 1.28) or accept free strings?
33. Raw outputs that are not JSON: container logs `logs` (vs `logs_text`), exec `output` (vs `stdout`+`stderr`), image-build `build_log`, container-file `content_base64` (vs `content`). SCHEMA lists raw names as fixed or `<thing>_json`; which form for non-JSON streams?
34. Hash name when an item has two raw outputs (container-file stat_json + content_base64): used `<raw>_sha256` (`stat_json_sha256`, `content_base64_sha256`) per SCHEMA; confirm, or should file content keep `content_sha256`?
35. Action result output: `result` (whole API JSON body, structured) vs flattening each field (`space_reclaimed`, `exit_code`) to top-level outputs. Used `result` + identity lists of deleted IDs; container state-change actions use `state` (ContainerState).
36. List data sources: identity as `ids` / `names` list(string) vs per-element identity inside the structured list only.
37. Files written by actions (export, save, upload): sha256 exposed as identity `archive_sha256` because category hash requires a raw string output; or should these get a hash category exception?
38. docker_swarm_init exposes the cluster ID today as `cluster_id`; the contract uses `id` = ClusterInfo.ID. Keep `cluster_id` as an alias, or drop it?
39. Plural data sources: structured list named after the type (`services`, `nodes`, ...) with raw `<plural>_json` (this lane), or a uniform `items` / `items_json` across every plural data source?
40. Action identity for the target: `node_id` / `service_id` (this lane) or plain `id` like resources?
41. docker_compose exposes the project name as `id` and `project_name`; the contract's identity name is `name`. Add a `name` alias, rename project_name to name (breaking), or keep project_name?
42. Facets of docker_compose: one `project` attribute (whole project) as proposed, or per-slice attributes (`project_services`, `project_networks`, ...) that do not clash with today's computed `services`/`networks`/`volumes` ID maps?
43. Action results: `result` object {exit_code, stdout, stderr} (as proposed for docker_compose_exec / _run) or flat `exit_code` / `stdout` / `stderr` like docker_exec_output?
44. docker_image and data.docker_image expose `repo_digest`; the contract name is `image_digest_ref`: rename (breaking), add `image_digest_ref` and deprecate `repo_digest`, or keep `repo_digest` as the contract name?
45. data.docker_registry_image exposes `sha256_digest` (= contract `digest`) and `id` = `<name>@<digest>` (≈ `image_digest_ref`): add `digest` + `image_digest_ref` and deprecate `sha256_digest`, or keep the current names?
46. `image_id` duplicates `id` on docker_image and data.docker_image: keep both, or deprecate `image_id`?
47. data.docker_dockerfile: expose the original input text as a second raw output (`source_dockerfile` + `source_dockerfile_sha256`) beside the re-rendered `dockerfile`, or only the rendered form?
48. Structured output name on each dockerfile_block item: the lowercase keyword (`run`, `copy`, `from`, current) or one uniform name (`block`)?
49. data.docker_dockerfile image mode: name the provenance outputs `reconstructed` + `fidelity` (current, both identity) or a single `fidelity` (`exact`/`approximate`)?
50. docker_stack data source structured output: `services` + `tasks` (current) or split tasks into docker_tasks only and drop `tasks` here?
51. stack ls Services count: `services` in docker_stacks (upstream name) while docker_stack.stack.services is the compose service map; rename the docker_stacks field to `services_count`, or keep upstream `services`?
