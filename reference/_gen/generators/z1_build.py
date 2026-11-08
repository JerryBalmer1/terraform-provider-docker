import json, os, sys, yaml, collections

HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.normpath(os.path.join(HERE, "..", ".."))  # reference/
FRAG = os.path.join(HERE, "..", "fragments")
OUT = os.path.join(FRAG, "z1-engine.json")

sw = yaml.safe_load(open(os.path.join(REF, "upstream/engine-api/api/swagger.yaml"), encoding="utf-8"))
OPS = {}
for p, v in sw["paths"].items():
    for m, o in v.items():
        if isinstance(o, dict) and "operationId" in o:
            OPS[o["operationId"]] = (m.upper(), p, o.get("tags") or [])

# op-level min_api, only where api/docs/CHANGELOG.md states the endpoint was added
MIN = {
    "PutContainerArchive": "1.20", "ContainerArchive": "1.20",
    "ContainerUpdate": "1.22",
    "VolumeList": "1.21", "VolumeCreate": "1.21", "VolumeInspect": "1.21", "VolumeDelete": "1.21",
    "VolumeUpdate": "1.42",
    "ContainerPrune": "1.25", "ImagePrune": "1.25", "VolumePrune": "1.25", "NetworkPrune": "1.25",
    "PluginList": "1.25", "PluginPull": "1.25", "PluginInspect": "1.25", "PluginSet": "1.25",
    "PluginEnable": "1.25", "PluginDisable": "1.25", "PluginPush": "1.25", "PluginCreate": "1.25",
    "PluginDelete": "1.25", "PluginUpgrade": "1.26",
    "SecretList": "1.25", "SecretCreate": "1.25", "SecretDelete": "1.25", "SecretInspect": "1.25", "SecretUpdate": "1.25",
    "DistributionInspect": "1.30", "Session": "1.31", "ImageAttestations": "1.55",
    "SystemPing": "1.11", "SystemPingHead": "1.40",
}

CLIDIR = "upstream/docker-cli/docs/reference/commandline/"
missing_cli = []


def cli(cmd, fname):
    path = CLIDIR + fname
    if not os.path.isfile(os.path.join(REF, path)):
        missing_cli.append(path)
    return {"type": "cli", "command": cmd, "file": path}


def op(opid, primary=True):
    m, p, _ = OPS[opid]
    s = {"type": "swagger", "operationId": opid, "method": m, "path": p}
    if MIN.get(opid):
        s["min_api"] = MIN[opid]
    if primary:
        s["primary"] = True
    return s


def sec(opid):
    return op(opid, primary=False)


ITEMS = []


def item(id, layer, kind, name, summary, sources, status, existing=(), min_api=None, update=None,
         status_note=None, skip_reason=None, collision=None):
    it = {"id": id, "layer": layer, "kind": kind, "name": name}
    if kind == "resource":
        it["update"] = update
    it["summary"] = summary
    it["sources"] = sources
    it["min_api"] = min_api
    it["status"] = status
    it["existing"] = list(existing)
    if status_note:
        it["status_note"] = status_note
    it["cli_only"] = False
    if skip_reason:
        it["skip_reason"] = skip_reason
    if collision:
        it["collision"] = collision
    ITEMS.append(it)


E, S = "engine", "swarm"

# ---------------- containers ----------------
item("engine:container", E, "resource", "docker_container",
     "Container lifecycle: create (+connect networks, upload files), start, in-place update of resources/restart policy and rename, remove.",
     [op("ContainerCreate"), sec("ContainerInspect"), op("ContainerUpdate"), op("ContainerRename"), op("ContainerDelete"),
      op("PutContainerArchive"), sec("ContainerStart"), sec("NetworkConnect"),
      cli("docker container create", "container_create.md"), cli("docker container run", "container_run.md"),
      cli("docker container update", "container_update.md"), cli("docker container rename", "container_rename.md"),
      cli("docker container rm", "container_rm.md"), cli("docker container cp (into a container)", "container_cp.md")],
     "exists", ["docker_container (resource)", "list.docker_container (list resource)"], update="in_place",
     status_note="Covers ContainerCreate/Update/Rename/Delete; PutContainerArchive only through `upload` blocks before first start; networks connected only at create (NetworkConnect), never connected/disconnected in place.")
item("engine:container-data", E, "data_source", "docker_container",
     "Look up one container (inspect) that Terraform does not manage.",
     [op("ContainerInspect"), cli("docker container inspect", "container_inspect.md"), cli("docker inspect", "inspect.md")],
     "missing")
item("engine:containers", E, "data_source", "docker_containers", "List containers with filters.",
     [op("ContainerList"), cli("docker container ls", "container_ls.md"), cli("docker ps", "ps.md")],
     "exists", ["data.docker_containers (data source)", "list.docker_container (list resource)"])
item("engine:container-logs", E, "data_source", "docker_logs", "Read a container's logs (non-follow snapshot).",
     [op("ContainerLogs"), cli("docker container logs", "container_logs.md")],
     "exists", ["data.docker_logs (data source)"])
item("engine:container-top", E, "data_source", "docker_container_top", "Processes running in a container.",
     [op("ContainerTop"), cli("docker container top", "container_top.md")], "missing")
item("engine:container-changes", E, "data_source", "docker_container_changes",
     "Filesystem changes of a container against its image.",
     [op("ContainerChanges"), cli("docker container diff", "container_diff.md")], "missing")
item("engine:container-stats", E, "data_source", "docker_container_stats",
     "One-shot resource usage sample (stream=false, one-shot=true).",
     [op("ContainerStats"), cli("docker container stats --no-stream", "container_stats.md")], "missing")
item("engine:container-file", E, "data_source", "docker_container_file",
     "Read a file/directory stat and content out of a container (HEAD + GET archive).",
     [op("ContainerArchiveInfo"), op("ContainerArchive"), cli("docker container cp (out of a container)", "container_cp.md")],
     "missing", min_api="1.20",
     collision="Confusable with docker_container `upload` blocks (the write direction); also consider an action instead if large archives are expected.")
item("engine:container-upload", E, "action", "docker_container_upload",
     "Copy files into an existing (running) container at any time, not only before first start.",
     [sec("PutContainerArchive"), cli("docker container cp", "container_cp.md")],
     "partial", ["docker_container (resource)"], min_api="1.20",
     status_note="Only docker_container `upload` blocks (before first start) use PutContainerArchive.")
item("engine:container-export", E, "action", "docker_container_export", "Export a container filesystem as a tar.",
     [op("ContainerExport"), cli("docker container export", "container_export.md")],
     "exists", ["action.docker_container_export (action)"])
for verb, opid, f, note in [
    ("start", "ContainerStart", "container_start.md", "Start a stopped container (docker_container starts it only at create via must_run)."),
    ("stop", "ContainerStop", "container_stop.md", "Stop a running container."),
    ("restart", "ContainerRestart", "container_restart.md", "Restart a container."),
    ("kill", "ContainerKill", "container_kill.md", "Send a signal (default SIGKILL) to a container."),
    ("pause", "ContainerPause", "container_pause.md", "Freeze all processes of a container."),
    ("unpause", "ContainerUnpause", "container_unpause.md", "Resume a paused container."),
    ("wait", "ContainerWait", "container_wait.md", "Block until a container reaches a condition (not-running/next-exit/removed) and report its exit code."),
]:
    item("engine:container-" + verb, E, "action", "docker_container_" + verb, note,
         [op(opid), cli("docker container " + verb, f)], "missing")
item("engine:container-prune", E, "action", "docker_container_prune", "Remove stopped containers.",
     [op("ContainerPrune"), cli("docker container prune", "container_prune.md")],
     "partial", ["action.docker_system_prune (action)"], min_api="1.25",
     status_note="Only through docker_system_prune with containers=true (and other kinds switched off); no dedicated action.")

# ---------------- exec ----------------
item("engine:exec", E, "action", "docker_exec", "Run a command inside a running container.",
     [op("ContainerExec"), op("ExecStart"), op("ExecInspect"), cli("docker container exec", "container_exec.md")],
     "exists", ["action.docker_exec (action)", "ephemeral.docker_exec_output (ephemeral resource)"],
     status_note="ephemeral docker_exec_output uses the same three ops to return stdout as an ephemeral value.")

# ---------------- images ----------------
item("engine:image", E, "resource", "docker_image",
     "Local image: pull (ImageCreate fromImage) or build, inspect, remove.",
     [op("ImageCreate"), op("ImageBuild"), sec("ImageInspect"), op("ImageDelete"),
      cli("docker image pull", "image_pull.md"), cli("docker image build", "image_build.md"),
      cli("docker image rm", "image_rm.md")],
     "exists", ["docker_image (resource)", "list.docker_image (list resource)"], update="replace",
     status_note="ImageCreate fromSrc (import) lives in action docker_image_import.")
item("engine:image-build", E, "action", "docker_image_build",
     "One-shot image build without a managed image object (e.g. build-and-push pipelines).",
     [sec("ImageBuild"), cli("docker image build", "image_build.md"), cli("docker build", "build.md")],
     "partial", ["docker_image (resource)"],
     status_note="Builds exist only as the docker_image `build` block (legacy builder and BuildKit via /session).",
     collision="Confusable with docker_image (resource whose `build` block already calls ImageBuild) and with docker_buildx_builder.")
item("engine:image-data", E, "data_source", "docker_image", "Inspect a local image.",
     [op("ImageInspect"), cli("docker image inspect", "image_inspect.md")],
     "exists", ["data.docker_image (data source)"])
item("engine:images", E, "data_source", "docker_images", "List local images with filters.",
     [op("ImageList"), cli("docker image ls", "image_ls.md"), cli("docker images", "images.md")],
     "partial", ["list.docker_image (list resource)"],
     status_note="Only as a list resource for `terraform query`; no plural data source.")
item("engine:image-history", E, "data_source", "docker_image_history", "Layer history of a local image.",
     [op("ImageHistory"), cli("docker image history", "image_history.md")], "missing")
item("engine:image-attestations", E, "data_source", "docker_image_attestations",
     "In-toto attestations (SBOM, provenance) stored with a local image.",
     [op("ImageAttestations")], "missing", min_api="1.55")
item("engine:image-search", E, "data_source", "docker_image_search", "Search Docker Hub through the daemon.",
     [op("ImageSearch"), cli("docker search", "search.md")], "missing")
item("engine:tag", E, "resource", "docker_tag", "Tag a local image with an extra reference.",
     [op("ImageTag"), cli("docker image tag", "image_tag.md")],
     "exists", ["docker_tag (resource)"], update="replace",
     status_note="Untag on destroy reuses ImageDelete (primary home engine:image).")
item("engine:registry-image", E, "resource", "docker_registry_image", "Push a local image to a registry and track its digest.",
     [op("ImagePush"), sec("DistributionInspect"), cli("docker image push", "image_push.md")],
     "exists", ["docker_registry_image (resource)"], update="replace")
item("engine:registry-image-data", E, "data_source", "docker_registry_image",
     "Resolve an image reference's digest and platforms from the registry via the daemon.",
     [op("DistributionInspect")], "exists", ["data.docker_registry_image (data source)"], min_api="1.30",
     status_note="No docker CLI command calls this endpoint directly (docker manifest/buildx imagetools talk to the registry client-side).")
item("engine:image-commit", E, "action", "docker_image_commit", "Create an image from a container's changes.",
     [op("ImageCommit"), cli("docker container commit", "container_commit.md")], "missing")
item("engine:image-save", E, "action", "docker_image_save", "Export one or more images to a tar.",
     [op("ImageGet"), op("ImageGetAll"), cli("docker image save", "image_save.md")],
     "exists", ["action.docker_image_save (action)"])
item("engine:image-load", E, "action", "docker_image_load", "Load images from a tar.",
     [op("ImageLoad"), cli("docker image load", "image_load.md")], "exists", ["action.docker_image_load (action)"])
item("engine:image-import", E, "action", "docker_image_import", "Create an image from a filesystem tarball (ImageCreate fromSrc).",
     [sec("ImageCreate"), cli("docker image import", "image_import.md")], "exists", ["action.docker_image_import (action)"])
item("engine:image-prune", E, "action", "docker_image_prune", "Remove dangling or unused images.",
     [op("ImagePrune"), cli("docker image prune", "image_prune.md")], "exists",
     ["action.docker_image_prune (action)", "action.docker_system_prune (action)"], min_api="1.25")
item("engine:build-prune", E, "action", "docker_builder_prune", "Remove build cache.",
     [op("BuildPrune"), cli("docker builder prune", "builder_prune.md")],
     "partial", ["action.docker_system_prune (action)"],
     status_note="Only through docker_system_prune build_cache=true; no dedicated action with keep/reserved-space options.",
     collision="docker_builder_prune (Engine build cache) is easy to confuse with docker_buildx_builder (a BuildKit builder container); docker_build_cache_prune avoids it.")

# ---------------- system ----------------
item("engine:registry-auth", E, "data_source", "docker_registry_auth",
     "Validate registry credentials through the daemon and return an identity token when the registry issues one.",
     [op("SystemAuth"), cli("docker login", "login.md")],
     "partial", ["ephemeral.docker_registry_token (ephemeral resource)"],
     status_note="docker_registry_token gets a bearer token straight from the registry; nothing calls /auth. Should be ephemeral (returns a secret), which this schema has no kind for.",
     collision="Confusable with ephemeral docker_registry_token and with provider registry_auth blocks.")
item("engine:info", E, "data_source", "docker_info", "Daemon and host information (docker info).",
     [op("SystemInfo"), cli("docker system info", "system_info.md"), cli("docker info", "info.md")],
     "missing", status_note="Provider calls Info internally (swarm state, OS) but exposes no data source.")
item("engine:version", E, "data_source", "docker_version", "Engine version, API version and components.",
     [op("SystemVersion"), cli("docker version", "version.md")], "missing")
item("engine:system-df", E, "data_source", "docker_system_df", "Disk usage by images, containers, volumes and build cache.",
     [op("SystemDataUsage"), cli("docker system df", "system_df.md")], "missing")
item("engine:events", E, "skip", None, "Daemon event stream.",
     [op("SystemEvents"), cli("docker system events", "system_events.md")], "n/a",
     skip_reason="Unbounded streaming endpoint; no Terraform lifecycle fits (a bounded since/until snapshot data source is possible later).")
item("engine:ping", E, "skip", None, "Daemon liveness and API version negotiation.",
     [op("SystemPing"), op("SystemPingHead")], "n/a", min_api="1.11",
     skip_reason="Used internally by provider configure for API version negotiation; no user-facing object.")
item("engine:attach", E, "skip", None, "Attach to a container's stdio.",
     [op("ContainerAttach"), op("ContainerAttachWebsocket"), cli("docker container attach", "container_attach.md")], "n/a",
     skip_reason="Interactive hijacked/websocket stream.")
item("engine:resize", E, "skip", None, "Resize a container or exec TTY.",
     [op("ContainerResize"), op("ExecResize")], "n/a", skip_reason="Only meaningful for an interactive TTY session.")
item("engine:session", E, "skip", None, "Hijacked gRPC session for BuildKit.",
     [op("Session")], "n/a", min_api="1.31",
     skip_reason="Deprecated (API 1.53) transport; used internally by the docker_image BuildKit build path, not a user-facing object.")

# ---------------- volumes ----------------
item("engine:volume", E, "resource", "docker_volume", "Named volume: create, inspect, remove; cluster (CSI) volumes update in place.",
     [op("VolumeCreate"), sec("VolumeInspect"), op("VolumeUpdate"), op("VolumeDelete"),
      cli("docker volume create", "volume_create.md"), cli("docker volume update", "volume_update.md"),
      cli("docker volume rm", "volume_rm.md")],
     "partial", ["docker_volume (resource)", "list.docker_volume (list resource)"], min_api="1.21", update="in_place",
     status_note="Local volumes complete; no ClusterVolumeSpec (swarm CSI volumes) and VolumeUpdate (1.42, cluster volumes only) is unused.")
item("engine:volume-data", E, "data_source", "docker_volume", "Inspect a volume.",
     [op("VolumeInspect"), cli("docker volume inspect", "volume_inspect.md")], "exists", ["data.docker_volume (data source)"], min_api="1.21")
item("engine:volumes", E, "data_source", "docker_volumes", "List volumes with filters.",
     [op("VolumeList"), cli("docker volume ls", "volume_ls.md")], "partial", ["list.docker_volume (list resource)"], min_api="1.21",
     status_note="Only as a list resource; no plural data source.")
item("engine:volume-prune", E, "action", "docker_volume_prune", "Remove unused volumes.",
     [op("VolumePrune"), cli("docker volume prune", "volume_prune.md")], "partial", ["action.docker_system_prune (action)"], min_api="1.25",
     status_note="Only through docker_system_prune volumes=true.")

# ---------------- networks ----------------
item("engine:network", E, "resource", "docker_network", "Network: create, inspect, remove (all settings force replacement).",
     [op("NetworkCreate"), sec("NetworkInspect"), op("NetworkDelete"),
      cli("docker network create", "network_create.md"), cli("docker network rm", "network_rm.md")],
     "exists", ["docker_network (resource)", "list.docker_network (list resource)"], update="replace")
item("engine:network-data", E, "data_source", "docker_network", "Inspect a network.",
     [op("NetworkInspect"), cli("docker network inspect", "network_inspect.md")], "exists", ["data.docker_network (data source)"])
item("engine:networks", E, "data_source", "docker_networks", "List networks with filters.",
     [op("NetworkList"), cli("docker network ls", "network_ls.md")], "partial", ["list.docker_network (list resource)"],
     status_note="Only as a list resource; no plural data source.")
item("engine:network-attachment", E, "resource", "docker_network_attachment",
     "Connect an existing container to a network (create) and disconnect it (destroy), independent of the container's lifecycle.",
     [op("NetworkConnect"), op("NetworkDisconnect"),
      cli("docker network connect", "network_connect.md"), cli("docker network disconnect", "network_disconnect.md")],
     "partial", ["docker_container (resource)"], update="replace",
     status_note="docker_container connects its `networks` at create only; NetworkDisconnect is never called and there is no standalone attachment.",
     collision="Overlaps docker_container `networks`; managing the same pair in both would fight.")
item("engine:network-prune", E, "action", "docker_network_prune", "Remove unused networks.",
     [op("NetworkPrune"), cli("docker network prune", "network_prune.md")], "partial", ["action.docker_system_prune (action)"], min_api="1.25",
     status_note="Only through docker_system_prune networks=true.")

# ---------------- plugins ----------------
item("engine:plugin", E, "resource", "docker_plugin",
     "Managed plugin: privileges + pull (install), enable/disable, set settings, upgrade, remove.",
     [op("GetPluginPrivileges"), op("PluginPull"), sec("PluginInspect"), op("PluginEnable"), op("PluginDisable"),
      op("PluginSet"), op("PluginUpgrade"), op("PluginDelete"),
      cli("docker plugin install", "plugin_install.md"), cli("docker plugin enable", "plugin_enable.md"),
      cli("docker plugin disable", "plugin_disable.md"), cli("docker plugin set", "plugin_set.md"),
      cli("docker plugin upgrade", "plugin_upgrade.md"), cli("docker plugin rm", "plugin_rm.md")],
     "partial", ["docker_plugin (resource)"], min_api="1.25", update="in_place",
     status_note="PluginUpgrade (1.26) is never used: a changed remote reference forces replacement instead of an in-place upgrade.")
item("engine:plugin-data", E, "data_source", "docker_plugin", "Inspect an installed plugin.",
     [op("PluginInspect"), cli("docker plugin inspect", "plugin_inspect.md")], "exists", ["data.docker_plugin (data source)"], min_api="1.25")
item("engine:plugins", E, "data_source", "docker_plugins", "List installed plugins.",
     [op("PluginList"), cli("docker plugin ls", "plugin_ls.md")], "missing", min_api="1.25")
item("engine:plugin-create", E, "action", "docker_plugin_create", "Create a plugin from a rootfs + config.json tarball.",
     [op("PluginCreate"), cli("docker plugin create", "plugin_create.md")], "missing", min_api="1.25",
     collision="Confusable with docker_plugin (install from a registry). PluginCreate has a delete counterpart, so it could also be a resource.")
item("engine:plugin-push", E, "action", "docker_plugin_push", "Push a locally created plugin to a registry.",
     [op("PluginPush"), cli("docker plugin push", "plugin_push.md")], "missing", min_api="1.25")

# ---------------- swarm ----------------
item("swarm:swarm-init", S, "resource", "docker_swarm_init",
     "Initialise a swarm on the provider host; update spec (autolock, CA, raft, dispatcher) in place; read the unlock key; leave on destroy.",
     [op("SwarmInit"), sec("SwarmInspect"), op("SwarmUpdate"), op("SwarmUnlockkey"), sec("SwarmLeave"),
      cli("docker swarm init", "swarm_init.md"), cli("docker swarm update", "swarm_update.md"),
      cli("docker swarm unlock-key", "swarm_unlock-key.md")],
     "exists", ["docker_swarm_init (resource)"], update="in_place",
     status_note="SwarmUpdate is used for autolock in place; other spec fields' in-place coverage not audited here.")
item("swarm:swarm-data", S, "data_source", "docker_swarm", "Inspect the swarm (ID, spec, join tokens, CA, data path port).",
     [op("SwarmInspect"), cli("docker swarm join-token", "swarm_join-token.md")],
     "partial", ["data.docker_swarm_tokens (data source)", "ephemeral.docker_swarm_tokens (ephemeral resource)"],
     status_note="Only join tokens and ID are exposed; spec/CA/TLS info has no data source.")
item("swarm:swarm-node", S, "resource", "docker_swarm_node",
     "Join a host to the swarm (via its own daemon), manage its role/availability/labels through the manager, leave and remove on destroy.",
     [op("SwarmJoin"), op("SwarmLeave"), op("NodeUpdate"), sec("NodeInspect"), op("NodeDelete"),
      cli("docker swarm join", "swarm_join.md"), cli("docker swarm leave", "swarm_leave.md"),
      cli("docker node update", "node_update.md"), cli("docker node rm", "node_rm.md")],
     "exists", ["docker_swarm_node (resource)", "list.docker_swarm_node (list resource)"], update="in_place",
     collision="docker_swarm_node is a *join* resource; a user expecting it to manage an already-joined node (labels/availability only, NodeUpdate) would need a separate type, e.g. docker_node.")
item("swarm:swarm-unlock", S, "action", "docker_swarm_unlock", "Unlock a locked (autolock) manager after a daemon restart.",
     [op("SwarmUnlock"), cli("docker swarm unlock", "swarm_unlock.md")], "missing",
     status_note="docker_swarm_init only detects the locked state and asks the user to unlock.")
item("swarm:swarm-rotate-tokens", S, "action", "docker_swarm_rotate_tokens", "Rotate worker/manager join tokens.",
     [sec("SwarmUpdate"), cli("docker swarm join-token --rotate", "swarm_join-token.md")],
     "exists", ["action.docker_swarm_rotate_tokens (action)"])
item("swarm:swarm-rotate-unlock-key", S, "action", "docker_swarm_rotate_unlock_key", "Rotate the manager unlock key.",
     [sec("SwarmUpdate"), cli("docker swarm unlock-key --rotate", "swarm_unlock-key.md")], "missing")
item("swarm:swarm-ca-rotate", S, "action", "docker_swarm_ca_rotate", "Rotate the swarm root CA (or install an external one).",
     [sec("SwarmUpdate"), cli("docker swarm ca --rotate", "swarm_ca.md")], "missing")
item("swarm:node-data", S, "data_source", "docker_node", "Inspect a swarm node.",
     [op("NodeInspect"), cli("docker node inspect", "node_inspect.md")], "exists", ["data.docker_node (data source)"])
item("swarm:nodes", S, "data_source", "docker_nodes", "List swarm nodes.",
     [op("NodeList"), cli("docker node ls", "node_ls.md")], "exists",
     ["data.docker_nodes (data source)", "list.docker_swarm_node (list resource)"])
item("swarm:node-drain", S, "action", "docker_node_drain", "Set a node's availability to drain.",
     [sec("NodeUpdate"), cli("docker node update --availability drain", "node_update.md")], "exists", ["action.docker_node_drain (action)"])
item("swarm:node-activate", S, "action", "docker_node_activate", "Set a node's availability to active.",
     [sec("NodeUpdate"), cli("docker node update --availability active", "node_update.md")], "exists", ["action.docker_node_activate (action)"])
item("swarm:node-promote", S, "action", "docker_node_promote", "Promote a worker to manager.",
     [sec("NodeUpdate"), cli("docker node promote", "node_promote.md")], "partial", ["docker_swarm_node (resource)"],
     status_note="Only as docker_swarm_node `role` for nodes Terraform joined; no imperative action.")
item("swarm:node-demote", S, "action", "docker_node_demote", "Demote a manager to worker.",
     [sec("NodeUpdate"), cli("docker node demote", "node_demote.md")], "partial", ["docker_swarm_node (resource)"],
     status_note="Only as docker_swarm_node `role` for nodes Terraform joined; no imperative action.")
item("swarm:service", S, "resource", "docker_service", "Swarm service: create, update in place, remove.",
     [op("ServiceCreate"), sec("ServiceInspect"), op("ServiceUpdate"), op("ServiceDelete"),
      cli("docker service create", "service_create.md"), cli("docker service update", "service_update.md"),
      cli("docker service rm", "service_rm.md")],
     "exists", ["docker_service (resource)", "list.docker_service (list resource)"], update="in_place")
item("swarm:service-data", S, "data_source", "docker_service", "Inspect one service.",
     [op("ServiceInspect"), cli("docker service inspect", "service_inspect.md")], "missing",
     status_note="Only the plural data.docker_services exists.")
item("swarm:services", S, "data_source", "docker_services", "List services.",
     [op("ServiceList"), cli("docker service ls", "service_ls.md")], "exists",
     ["data.docker_services (data source)", "list.docker_service (list resource)"])
item("swarm:service-logs", S, "data_source", "docker_service_logs", "Snapshot of a service's aggregated task logs.",
     [op("ServiceLogs"), cli("docker service logs", "service_logs.md")], "missing",
     collision="Could instead extend data.docker_logs with a service_id/task_id argument.")
item("swarm:service-scale", S, "action", "docker_service_scale", "Set replicas of a replicated service.",
     [sec("ServiceUpdate"), cli("docker service scale", "service_scale.md")], "exists", ["action.docker_service_scale (action)"])
item("swarm:service-rollback", S, "action", "docker_service_rollback", "Roll a service back to its previous spec.",
     [sec("ServiceUpdate"), cli("docker service rollback", "service_rollback.md")], "exists", ["action.docker_service_rollback (action)"])
item("swarm:service-force-update", S, "action", "docker_service_force_update", "Force a rolling restart of a service's tasks.",
     [sec("ServiceUpdate"), cli("docker service update --force", "service_update.md")], "exists", ["action.docker_service_force_update (action)"])
item("swarm:tasks", S, "data_source", "docker_tasks", "List swarm tasks (filter by service, node, desired-state).",
     [op("TaskList"), cli("docker service ps", "service_ps.md"), cli("docker node ps", "node_ps.md"), cli("docker stack ps", "stack_ps.md")],
     "missing", status_note="TaskList is used internally (service convergence, stack, actions) but not exposed.")
item("swarm:task-data", S, "data_source", "docker_task", "Inspect one swarm task.",
     [op("TaskInspect"), cli("docker inspect", "inspect.md")], "missing")
item("swarm:task-logs", S, "data_source", "docker_task_logs", "Snapshot of one task's logs.",
     [op("TaskLogs"), cli("docker service logs <task-id>", "service_logs.md")], "missing",
     collision="Could merge with docker_service_logs or data.docker_logs.")
item("swarm:secret", S, "resource", "docker_secret", "Swarm secret: create, remove; labels updatable in place via SecretUpdate.",
     [op("SecretCreate"), sec("SecretInspect"), op("SecretUpdate"), op("SecretDelete"),
      cli("docker secret create", "secret_create.md"), cli("docker secret rm", "secret_rm.md")],
     "exists", ["docker_secret (resource)", "list.docker_secret (list resource)"], min_api="1.25", update="in_place",
     status_note="SecretUpdate (labels only) is unused: label changes force replacement today.")
item("swarm:secret-data", S, "data_source", "docker_secret", "Inspect one secret's metadata.",
     [op("SecretInspect"), cli("docker secret inspect", "secret_inspect.md")], "missing", min_api="1.25")
item("swarm:secrets", S, "data_source", "docker_secrets", "List secrets.",
     [op("SecretList"), cli("docker secret ls", "secret_ls.md")], "exists",
     ["data.docker_secrets (data source)", "list.docker_secret (list resource)"], min_api="1.25")
item("swarm:config", S, "resource", "docker_config", "Swarm config: create, remove; labels updatable in place via ConfigUpdate.",
     [op("ConfigCreate"), sec("ConfigInspect"), op("ConfigUpdate"), op("ConfigDelete"),
      cli("docker config create", "config_create.md"), cli("docker config rm", "config_rm.md")],
     "exists", ["docker_config (resource)", "list.docker_config (list resource)"], update="in_place",
     status_note="ConfigUpdate (labels only) is unused: label changes force replacement today.",
     collision="docker_config (swarm config object) vs compose top-level `configs` and the provider's own configuration block.")
item("swarm:config-data", S, "data_source", "docker_config", "Inspect one swarm config.",
     [op("ConfigInspect"), cli("docker config inspect", "config_inspect.md")], "missing",
     collision="Same confusion as docker_config resource (swarm config vs compose configs).")
item("swarm:configs", S, "data_source", "docker_configs", "List swarm configs.",
     [op("ConfigList"), cli("docker config ls", "config_ls.md")], "exists",
     ["data.docker_configs (data source)", "list.docker_config (list resource)"])

# ---------------- coverage ----------------
prim = collections.Counter()
seen = set()
for it in ITEMS:
    for s in it["sources"]:
        if s["type"] == "swagger":
            seen.add(s["operationId"])
            if s.get("primary"):
                prim[s["operationId"]] += 1
uncovered = sorted(set(OPS) - set(k for k, v in prim.items() if v == 1))
dups = sorted(k for k, v in prim.items() if v > 1)
unknown = sorted(seen - set(OPS))
ids = collections.Counter(it["id"] for it in ITEMS)
assert not [i for i, c in ids.items() if c > 1], ids
cov = f"{len(OPS)} operations, {len(OPS) - len(uncovered)} covered, {len(uncovered)} uncovered (each with exactly one `primary: true` swagger source); duplicates-as-primary: {len(dups)}; unknown operationIds: {len(unknown)}"
print(cov, uncovered, dups, unknown)
print("missing cli files:", missing_cli)

doc = {
    "lane": "Z1",
    "notes": [
        "Coverage self-check: " + cov + ".",
        "Source: upstream/engine-api/api/swagger.yaml (Engine API 1.56), parsed with PyYAML; 108 operations across tags Container, Image, System, Exec, Volume, Network, Plugin, Node, Swarm, Service, Task, Secret, Config, Distribution, Session.",
        "Schema extension: swagger source entries carry `primary: true` on the one item that is the operation's home; other items that also use the op list it without `primary` (e.g. ContainerInspect in docker_container resource vs data source, SwarmUpdate in rotate actions). Swagger sources also carry `min_api` where the CHANGELOG states the endpoint was added.",
        "min_api: the swagger descriptions carry no version text (only Session's deprecation). Values come from upstream/engine-api/api/docs/CHANGELOG.md endpoint-added lines: archive 1.20, volumes 1.21, container update 1.22, prune/plugins/secrets 1.25, plugin upgrade 1.26, distribution 1.30, session 1.31, VolumeUpdate 1.42, image attestations 1.55, _ping GET 1.11 / HEAD 1.40. Swarm/node/service/task/config/network/exec/image core endpoints predate CHANGELOG 'added' lines: null. Item-level min_api is the stated min of the item's create/primary op.",
        "Status was judged by grepping internal/provider/*.go (non-test) for moby client calls: unused in provider code are ContainerStop/Restart/Kill/Pause/Unpause/Wait/Top/Stats/Diff/Commit/StatPath/CopyFromContainer, ImageHistory/Search/Attestations, NetworkDisconnect, VolumeUpdate, PluginUpgrade/Push/Create/List, SwarmUnlock, SecretUpdate, ConfigUpdate, TaskInspect, Service/Task logs, Events, DiskUsage, RegistryLogin(/auth).",
        "docker_system_prune covers ContainerPrune, NetworkPrune, VolumePrune and BuildPrune through boolean switches, so the dedicated per-kind prune actions are marked partial rather than missing.",
        "client.PluginInstall calls GetPluginPrivileges + PluginPull, client.ImagePull calls ImageCreate; both counted as used by the existing resources.",
        "docker_stack, docker_compose and compose2stack have no Engine endpoint of their own (client-side); out of scope for this engine lane.",
        "ImageDelete is shared by docker_image (primary) and docker_tag (untag on destroy).",
    ],
    "naming_questions": [
        "NetworkConnect/NetworkDisconnect: add a standalone `docker_network_attachment` resource (container x network pair), or make docker_container `networks` connect/disconnect in place instead of forcing replacement? (or both, with a conflict warning)",
        "Build cache prune action name: `docker_builder_prune` (matches `docker builder prune`) or `docker_build_cache_prune` (avoids confusion with docker_buildx_builder)?",
        "Standalone build: add an action `docker_image_build`, or keep builds only in docker_image's `build` block? If added, is the name too close to docker_image?",
        "Managing an already-joined swarm node (labels/availability/role via NodeUpdate) that Terraform did not join: extend docker_swarm_node with an adopt mode, or add a separate `docker_node` resource (name matches existing data.docker_node)?",
        "Node role actions: `docker_node_promote` / `docker_node_demote` (CLI words, matching docker_node_drain/activate) or one `docker_node_role` action with role = manager|worker?",
        "Logs: separate `docker_service_logs` and `docker_task_logs` data sources, or extend data.docker_logs with service_id / task_id arguments?",
        "SystemAuth (/auth) returns an identity token: model as an ephemeral resource (e.g. `docker_registry_login`) next to ephemeral docker_registry_token, or skip it?",
        "PluginCreate: action `docker_plugin_create` (one-shot) or a resource (it has PluginDelete) with a different name to avoid confusion with docker_plugin (install from registry)?",
        "Container one-off read of files: data source `docker_container_file` (GET archive) vs an action; and copy-in at any time: action `docker_container_upload` or `docker_container_cp`?",
        "Swarm rotation actions: separate `docker_swarm_rotate_unlock_key` and `docker_swarm_ca_rotate`, or one `docker_swarm_rotate` action with what = worker_token|manager_token|unlock_key|ca (absorbing docker_swarm_rotate_tokens)?",
    ],
    "items": ITEMS,
}
with open(OUT, "w", encoding="utf-8", newline="\n") as f:
    json.dump(doc, f, indent=2, ensure_ascii=False)
    f.write("\n")
kinds = collections.Counter((i["kind"], i["status"]) for i in ITEMS)
for k, v in sorted(kinds.items()):
    print(k, v)
print(len(ITEMS), "items")
