import json, os, sys
from collections import Counter

HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.normpath(os.path.join(HERE, "..", ".."))  # reference/
FRAG = os.path.join(HERE, "..", "fragments")
out = sys.argv[1] if len(sys.argv) > 1 else os.path.join(FRAG, "z2-compose.json")
os.chdir(REF)  # source paths below are relative to reference/
S = "upstream/compose-spec/"
SCH = S + "schema/compose-spec.json"
EMB = S + "compose-go-embedded.json"


def sch(ptr, emb=True):
    r = [{"type": "schema", "file": SCH, "pointer": ptr}]
    if emb:
        r.append({"type": "schema", "file": EMB, "pointer": ptr})
    return r


def doc(f, a):
    return {"type": "doc", "file": S + f, "anchor": a}


DC = "docker_compose (resource)"
items = []


def add(**k):
    it = {"id": k.pop("id"), "layer": "compose", "kind": k.pop("kind"), "name": k.pop("name", None)}
    if it["kind"] == "resource":
        it["update"] = k.pop("update", "in_place")
    it["summary"] = k.pop("summary")
    it["sources"] = k.pop("sources")
    it["min_api"] = None
    it["status"] = k.pop("status")
    it["existing"] = k.pop("existing", [])
    for f in ("status_note", "cli_only", "skip_reason", "collision"):
        if f in k:
            it[f] = k.pop(f)
    it.setdefault("cli_only", False)
    assert not k, k
    items.append(it)


# ---- spec top-level elements
add(id="compose:services", kind="resource", name="docker_compose",
    summary="services: long-running containers (local) or Swarm services (swarm), managed as part of the docker_compose project.",
    sources=sch("#/properties/services") + sch("#/$defs/service") + [doc("05-services.md", "#services-top-level-element"), doc("container_spec.md", "#container-specification"), doc("workload_spec.md", "#workload-specification")],
    status="partial", existing=[DC, "docker_stack (resource)", "docker_service (resource)", "docker_container (resource)"],
    status_note="docker_compose loads services with compose-go and creates containers (local) or Swarm services (swarm). Ignored with a warning: build (no image is an error, pull_policy build errors), develop, pre_start/post_start/pre_stop hooks, models, provider, credential_spec, blkio_config, use_api_socket, external_links, attach (local); swarm mode also ignores container_name, depends_on, network_mode, privileged, devices, ipc/pid/userns/uts/cgroup_parent, security_opt, shm_size, volumes_from, gpus, links, tmpfs, pull_policy other than missing/always.",
    collision="A per-service type named docker_compose_service would be confusable with docker_service (swarm). Keep services inside docker_compose.")
add(id="compose:networks", kind="resource", name="docker_compose",
    summary="networks: project networks created and labelled by docker_compose (bridge locally, overlay in swarm); external networks are referenced only.",
    sources=sch("#/properties/networks") + sch("#/$defs/network") + [doc("06-networks.md", "#networks-top-level-element")],
    status="exists", existing=[DC, "docker_network (resource)"],
    collision="docker_compose_network would be confusable with docker_network.")
add(id="compose:volumes", kind="resource", name="docker_compose",
    summary="volumes: named project volumes created by docker_compose (removed on destroy unless keep_volumes); external volumes referenced only.",
    sources=sch("#/properties/volumes") + sch("#/$defs/volume") + [doc("07-volumes.md", "#volumes-top-level-element")],
    status="exists", existing=[DC, "docker_volume (resource)"],
    collision="docker_compose_volume(s) would be confusable with docker_volume and with the proposed docker_compose_volumes data source (CLI `compose volumes`).")
add(id="compose:configs", kind="resource", name="docker_compose",
    summary="configs: file/environment/content-sourced config objects; copied into containers locally, created as Swarm configs in swarm mode.",
    sources=sch("#/properties/configs") + sch("#/$defs/config") + [doc("08-configs.md", "#configs-top-level-element")],
    status="partial", existing=[DC, "docker_config (resource)"],
    status_note="Local mode copies the content into the container (no bind mount); swarm mode creates Swarm configs but ignores driver and template_driver with a warning.",
    collision="Top-level `configs` vs docker_config (Swarm config resource) vs the proposed docker_compose_config data source (CLI `compose config`, rendered model).")
add(id="compose:secrets", kind="resource", name="docker_compose",
    summary="secrets: file/environment-sourced secrets; copied to /run/secrets locally, created as Swarm secrets in swarm mode.",
    sources=sch("#/properties/secrets") + sch("#/$defs/secret") + [doc("09-secrets.md", "#secrets-top-level-element")],
    status="partial", existing=[DC, "docker_secret (resource)"],
    status_note="Local mode copies content into the container (no bind mount as compose does); swarm mode ignores driver and template_driver with a warning.",
    collision="docker_compose_secret would be confusable with docker_secret.")
add(id="compose:models", kind="resource", name="docker_compose",
    summary="models: AI models (Docker Model Runner) that services bind to via endpoint/model env vars.",
    sources=sch("#/properties/models") + sch("#/$defs/model") + [doc("models.md", "#models-top-level-element")],
    status="missing", existing=[DC],
    status_note="Parsed by compose-go but ignored: service `models` gets a warning in local mode; the top-level element is never acted on. Model Runner is not part of the Docker Engine API, so support needs a separate client.")
add(id="compose:jobs", kind="resource", name="docker_compose",
    summary="jobs: run-to-completion containers with manual or scheduled triggers (Compose NEXT).",
    sources=sch("#/properties/jobs") + sch("#/$defs/job") + sch("#/$defs/schedule") + [doc("jobs.md", "#jobs-top-level-element"), doc("jobs.md", "#triggers")],
    status="missing", existing=[DC],
    status_note="compose-go v2.16.2 parses jobs (Project.Jobs) but docker_compose never reads them: silently ignored, not even warned. Scheduled triggers need a scheduler outside Terraform; manual runs map to the proposed docker_compose_run action. The embedded compose-go schema lacks jobs.*.extends.")
add(id="compose:include", kind="resource", name="docker_compose",
    summary="include: pull other compose files (with own project_directory/env_file) into the model.",
    sources=sch("#/properties/include") + sch("#/$defs/include") + [doc("14-include.md", "#include")],
    status="partial", existing=[DC],
    status_note="Local-path includes are resolved by the compose-go loader; remote (git/oci) includes are disabled by design (compose_parse.go).")
add(id="compose:name", kind="resource", name="docker_compose",
    summary="name: project name used to prefix and label every object.",
    sources=sch("#/properties/name") + [doc("04-version-and-name.md", "#name-top-level-element")],
    status="exists", existing=[DC],
    status_note="docker_compose.project_name defaults to the file's top-level name.")
add(id="compose:version", kind="skip",
    summary="version: obsolete top-level element, informational only.",
    sources=sch("#/properties/version") + [doc("04-version-and-name.md", "#version-top-level-element-obsolete")],
    status="n/a", skip_reason="obsolete; Compose ignores it (warns) and so does the loader")
# sub-blocks
add(id="compose:service-build", kind="resource", name="docker_image",
    summary="service.build: build the service image (context, dockerfile, args, secrets, cache, platforms...).",
    sources=sch("#/$defs/service/properties/build") + [doc("build.md", "#compose-build-specification")],
    status="partial", existing=["docker_image (resource)", "docker_buildx_builder (resource)"],
    status_note="docker_compose does not build (warns; errors when image is unset); docker_image covers the build as a separate resource. A build action would be docker_compose_build.")
add(id="compose:service-deploy", kind="resource", name="docker_service",
    summary="service.deploy: replicas, mode, resources, restart/update/rollback policy, placement, labels.",
    sources=sch("#/$defs/service/properties/deploy") + sch("#/$defs/deployment") + [doc("deploy.md", "#compose-deploy-specification")],
    status="partial", existing=[DC, "docker_service (resource)", "docker_stack (resource)"],
    status_note="docker_compose swarm mode maps deploy to the Swarm service spec; local mode uses replicas (scale), global mode and resources. Not its own Terraform type: it is the docker_service spec.")
add(id="compose:service-develop", kind="skip",
    summary="service.develop: watch rules (sync/rebuild/restart) used by `compose watch`.",
    sources=sch("#/$defs/service/properties/develop") + sch("#/$defs/development") + [doc("develop.md", "#compose-develop-specification")],
    status="n/a", skip_reason="developer inner-loop file watching; not declarative (docker_compose warns and ignores it)")
for slug, f, a, extra in [
    ("fragments", "10-fragments.md", "#fragments", " (YAML anchors/aliases)"),
    ("extensions", "11-extension.md", "#extension", " (x-* keys; ignored by docker_compose)"),
    ("interpolation", "12-interpolation.md", "#interpolation", " (fed by docker_compose env / env_file and the compose_env function)"),
    ("merge", "13-merge.md", "#merge-and-override", " (docker_compose.files merges in order like repeated -f)"),
    ("profiles", "15-profiles.md", "#profiles", " (docker_compose.profiles selects them)"),
]:
    add(id="compose:" + slug, kind="skip", summary=slug + ": compose file-format mechanic" + extra + ".",
        sources=[doc(f, a)], status="n/a", skip_reason="file-format mechanic, handled by the compose loader")

# ---- CLI
C = "upstream/compose-cli/docs/reference/"


def cli(sub, md=True):
    cmd = "docker compose" + ("" if not sub else " " + sub.replace("_", " "))
    base = "compose" + ("" if not sub else "_" + sub)
    r = [{"type": "cli", "command": cmd, "file": C + base + ".md"}] if md else []
    r.append({"type": "cli", "command": cmd, "file": C + "docker_" + base + ".yaml"})
    return r


def c(sub, kind, name, summary, status, existing=None, note=None, skip=None, coll=None, md=True):
    k = dict(id="compose:cli-" + (sub or "root").replace("_", "-"), kind=kind, name=name, summary=summary,
             sources=cli(sub, md), status=status, existing=existing or [], cli_only=True)
    if note:
        k["status_note"] = note
    if skip:
        k["skip_reason"] = skip
    if coll:
        k["collision"] = coll
    add(**k)


c("", "skip", None, "Root `docker compose` command group.", "n/a", skip="command group only; global flags (-f, -p, --profile, --env-file) are docker_compose attributes")
c("up", "action", "docker_compose_up", "Create/recreate and start the project. Engine: network/volume create, image pull, container create/start (+ wait).", "partial", [DC],
  note="The docker_compose resource does up on create/update; climap maps `compose up` to it. A standalone action would serve projects not managed by Terraform.",
  coll="docker_compose (resource) already means up; an action docker_compose_up beside it invites confusion.")
c("down", "action", "docker_compose_down", "Stop and remove the project's containers and networks (optionally volumes, images). Engine: container stop/delete, network delete, volume delete.", "partial", [DC],
  note="Destroying docker_compose (keep_volumes) is down; no action for projects outside Terraform.")
for sub, verb, eng in [("start", "Start existing service containers", "POST /containers/{id}/start"),
                       ("stop", "Stop service containers", "POST /containers/{id}/stop"),
                       ("restart", "Restart service containers", "POST /containers/{id}/restart"),
                       ("kill", "Send a signal to service containers", "POST /containers/{id}/kill"),
                       ("pause", "Pause service containers", "POST /containers/{id}/pause"),
                       ("unpause", "Unpause service containers", "POST /containers/{id}/unpause")]:
    c(sub, "action", "docker_compose_" + sub, verb + ", selected by project/service labels. Engine: GET /containers/json (label filter) + " + eng + ".", "missing",
      coll="Would mirror single-container docker_container_" + sub + " actions if the engine lane proposes them; decide separate compose types vs a project/service selector on those.")
c("create", "skip", None, "Create service containers without starting them.", "n/a", skip="half of `up`; the docker_compose resource owns container creation")
c("rm", "skip", None, "Remove stopped service containers.", "n/a", skip="docker_compose reconciles and removes its own containers; one-off cleanup is docker_system_prune")
c("build", "action", "docker_compose_build", "Build every service with a `build:` section. Engine: POST /build or BuildKit session.", "partial", ["docker_image (resource)"],
  note="docker_compose ignores build: and climap says no equivalent; docker_image builds one image per resource.")
c("pull", "action", "docker_compose_pull", "Pull all service images. Engine: POST /images/create.", "partial", ["docker_image (resource)", DC],
  note="docker_compose pulls missing images during apply; climap maps it to docker_image.")
c("push", "action", "docker_compose_push", "Push all service images. Engine: POST /images/{name}/push.", "partial", ["docker_registry_image (resource)"],
  note="docker_registry_image pushes one image per resource; no project-wide push.")
c("run", "action", "docker_compose_run", "Run a one-off container for a service or job. Engine: container create/start/wait/delete.", "missing",
  note="climap: no equivalent. Also the natural manual trigger for compose `jobs`.")
c("exec", "action", "docker_compose_exec", "Exec a command in a service container. Engine: POST /containers/{id}/exec + /exec/{id}/start.", "partial", ["docker_exec (action)", "docker_exec_output (ephemeral resource)"],
  note="climap maps it to docker_exec with the <project>-<service>-<n> container name; no service selector.")
c("cp", "action", "docker_compose_cp", "Copy files between a service container and the local filesystem. Engine: PUT/GET /containers/{id}/archive.", "partial", ["docker_container (resource)"],
  note="docker_container has upload blocks for containers it manages; nothing for compose containers or for copying out.")
c("commit", "action", "docker_compose_commit", "Create an image from a service container. Engine: POST /commit.", "missing")
c("export", "action", "docker_compose_export", "Export a service container filesystem as a tar. Engine: GET /containers/{id}/export.", "partial", ["docker_container_export (action)"],
  note="docker_container_export takes a container, not a project/service.")
c("scale", "action", "docker_compose_scale", "Set the replica count of services. Engine: container create/start/stop/delete (local).", "partial", [DC, "docker_service_scale (action)"],
  note="Declaratively via deploy.replicas in docker_compose content; docker_service_scale covers swarm services.",
  coll="docker_service_scale (swarm action) already exists; docker_compose_scale is near-identical in purpose.")
c("wait", "action", "docker_compose_wait", "Block until service containers stop. Engine: POST /containers/{id}/wait.", "partial", [DC],
  note="docker_compose has wait/wait_timeout for readiness after apply, not for exit.")
c("publish", "action", "docker_compose_publish", "Publish the compose application as an OCI artifact to a registry.", "missing",
  note="Registry (OCI distribution) API, not Engine API.")
c("watch", "skip", None, "Watch build context and sync/rebuild/restart on change.", "n/a", skip="interactive, long-running file watcher")
c("attach", "skip", None, "Attach stdio to a service container.", "n/a", skip="interactive attach stream")
c("events", "skip", None, "Stream real-time container events.", "n/a", skip="streaming")
c("stats", "skip", None, "Live stream of container resource usage.", "n/a", skip="streaming (a --no-stream snapshot could be a data source later)")
c("version", "skip", None, "Show compose version.", "n/a", skip="client version only; provider embeds compose-go v2.16.2")
c("ps", "data_source", "docker_compose_containers", "List containers of a project. Engine: GET /containers/json with com.docker.compose.project label filter.", "partial",
  [DC, "data.docker_containers (data source)", "list.docker_container (list resource)"],
  note="docker_compose.services gives container IDs for managed projects; docker_containers can filter by label for others.",
  coll="Name options: docker_compose_ps (CLI-shaped) vs docker_compose_containers (plural-noun convention beside docker_containers).")
c("ls", "data_source", "docker_compose_projects", "List compose projects on the host. Engine: GET /containers/json grouped by project label.", "missing",
  coll="docker_compose_ls (CLI-shaped) vs docker_compose_projects (noun).")
c("config", "data_source", "docker_compose_config", "Parse, interpolate, merge and render the compose model in canonical form (no Engine call).", "missing",
  note="tools/compose2stack converts once at authoring time (a tool, not a type); compose_env only builds env. No type renders the model.",
  coll="Confusable with top-level compose `configs` and docker_config (swarm config resource). Alternatives: docker_compose_project, docker_compose_model, or function compose_render.")
c("convert", "skip", None, "Alias of `compose config`.", "n/a", skip="alias of config", md=False)
c("images", "data_source", "docker_compose_images", "List images used by project containers. Engine: GET /containers/json + /images/{id}/json.", "missing",
  coll="Beside data.docker_image (singular) and list.docker_image.")
c("volumes", "data_source", "docker_compose_volumes", "List volumes of a project. Engine: GET /volumes with project label filter.", "partial",
  [DC, "data.docker_volume (data source)", "list.docker_volume (list resource)"], note="docker_compose.volumes lists managed project volumes; no lookup for others.",
  coll="docker_compose_volumes vs docker_volume / top-level compose `volumes`.")
c("logs", "data_source", "docker_compose_logs", "Read logs of service containers. Engine: GET /containers/{id}/logs.", "partial", ["data.docker_logs (data source)"],
  note="climap maps it to docker_logs per container; no project/service selector.")
c("top", "data_source", "docker_compose_top", "Processes running in service containers. Engine: GET /containers/{id}/top.", "missing")
c("port", "data_source", "docker_compose_port", "Public port for a service port binding. Engine: GET /containers/{id}/json NetworkSettings.Ports.", "missing",
  coll="Confusable with the port_spec function.")
c("alpha", "skip", None, "Experimental command group.", "n/a", skip="command group only")
c("alpha_dry-run", "skip", None, "Dry-run mode for commands.", "n/a", skip="Terraform plan already is the dry run")
c("alpha_generate", "skip", None, "Generate a compose file from existing containers.", "n/a", skip="experimental authoring helper; output is a file, not infra (terraform query + list resources cover discovery)")
c("alpha_publish", "skip", None, "Experimental alias of publish.", "n/a", skip="duplicate of docker_compose_publish")
c("alpha_scale", "skip", None, "Experimental alias of scale.", "n/a", skip="duplicate of docker_compose_scale")
c("alpha_viz", "skip", None, "Render a graphviz graph of the compose file.", "n/a", skip="visualisation only; terraform graph covers it")
c("alpha_watch", "skip", None, "Experimental alias of watch.", "n/a", skip="interactive file watcher")
c("bridge", "skip", None, "Convert compose files into another model (Kubernetes/Helm).", "n/a", skip="command group; targets non-Docker platforms")
c("bridge_convert", "skip", None, "Convert compose to Kubernetes manifests / Helm via transformation images.", "n/a", skip="out of scope (Kubernetes output)")
c("bridge_transformations", "skip", None, "Manage bridge transformation images.", "n/a", skip="command group")
c("bridge_transformations_create", "skip", None, "Create a transformation image.", "n/a", skip="out of scope (compose-bridge tooling)")
c("bridge_transformations_list", "skip", None, "List transformation images.", "n/a", skip="out of scope (compose-bridge tooling)")

notes = [
    "All CLI items are client-side (cli_only=true): `docker compose` composes Engine API calls selected by com.docker.compose.* labels; summaries name the calls.",
    "The provider pins compose-go v2.16.2. Its embedded schema (upstream/compose-spec/compose-go-embedded.json) differs from compose-spec main only in: container_spec mem_limit, memswap_limit, shm_size typed number|string (spec: integer|string), and no jobs.*.extends (Compose NEXT). It already has jobs, models, include, schedule.",
    "Spec items handled by docker_compose use name docker_compose (kind resource) rather than inventing per-element types; splitting is a naming question.",
    "docker_compose silently ignores top-level jobs (no warning) and warns on models; worth a TODO even without new types.",
    "docker_stack is HCL-native (service blocks), not compose-file driven; docker_compose mode=swarm is the compose-file path to a stack. tools/compose2stack is a one-shot converter, not a Terraform type.",
    "compose_convert has only a yaml (no .md) in compose-cli v5.1.4 docs; it is an alias of config. docker_compose.yaml is the root command.",
    "climap compose rows: up, down, ps, up --scale -> docker_compose; exec -> docker_exec; logs -> docker_logs; pull -> docker_image; config/convert -> tools/compose2stack; build, run/watch/alpha -> none.",
]
qs = [
    "Imperative compose actions beside the docker_compose resource: name them docker_compose_up/down (CLI-shaped), docker_compose_apply/destroy, or skip up/down since the resource covers them?",
    "`compose config` render: data source docker_compose_config (visually collides with docker_config and top-level `configs`), docker_compose_project / docker_compose_model, or a provider function compose_render?",
    "`compose ps` / `compose ls`: docker_compose_ps + docker_compose_ls (CLI names) or docker_compose_containers + docker_compose_projects (noun convention like docker_containers)?",
    "`compose volumes`: add a docker_compose_volumes data source, or document data.docker_volume/list.docker_volume with a project label filter (avoids the docker_volume clash)?",
    "Per-container verbs (start/stop/restart/kill/pause/unpause): separate docker_compose_* actions, or one set of docker_container_* actions with an optional project/service selector?",
    "`compose scale` vs existing docker_service_scale: new docker_compose_scale action, or extend docker_service_scale to compose local projects?",
    "Compose top-level elements: keep all inside docker_compose, or split docker_compose_service/network/volume (confusable with docker_service/docker_network/docker_volume)?",
    "jobs: support in docker_compose (run once on apply) plus docker_compose_run for manual triggers, or leave unsupported but add a warning?",
]
json.dump({"lane": "Z2", "notes": notes, "naming_questions": qs, "items": items},
          open(out, "w", encoding="utf-8", newline="\n"), indent=2, ensure_ascii=False)
ids = [i["id"] for i in items]
assert len(ids) == len(set(ids))
print(len(items), Counter(i["kind"] for i in items), Counter(i["status"] for i in items))
