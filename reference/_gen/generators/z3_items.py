"""Lane Z3 items: cli-only docker CLI commands, buildx commands + bake file elements, CLI plugins."""
import json, os, collections

HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.normpath(os.path.join(HERE, "..", ".."))  # reference/
FRAG = os.path.join(HERE, "..", "fragments")
OUT = FRAG
CLI = "upstream/docker-cli/docs/reference/commandline/"
TR = "upstream/docker-cli/cmd/docker-trust/docs/reference/"
BX = "upstream/buildx/docs/reference/"
BXD = "upstream/buildx/docs/"
HELP = "upstream/help/"
HELP_FILES = set(os.listdir(os.path.join(REF, "upstream", "help")))


def cli(cmd, base=CLI):
    """cli source for `docker <cmd>`; adds the help capture when present."""
    f = cmd.replace(" ", "_") + ".md"
    out = [{"type": "cli", "command": "docker " + cmd, "file": base + f}]
    h = "docker_" + cmd.replace(" ", "_") + ".txt"
    if h in HELP_FILES:
        out.append({"type": "cli", "command": "docker " + cmd + " --help", "file": HELP + h})
    return out


def bx(cmd):
    f = "buildx_" + cmd.replace(" ", "_") + ".md"
    out = [{"type": "cli", "command": "docker buildx " + cmd, "file": BX + f}]
    h = "docker_buildx_" + cmd.replace(" ", "_") + ".txt"
    if h in HELP_FILES:
        out.append({"type": "cli", "command": "docker buildx " + cmd + " --help", "file": HELP + h})
    return out


def help_only(cmd):
    return [{"type": "cli", "command": "docker " + cmd + " --help", "file": HELP + "docker_" + cmd.replace(" ", "_") + ".txt"}]


def sw(op, m, p):
    return {"type": "swagger", "operationId": op, "method": m, "path": p}


def srcs(*lists):
    out = []
    for l in lists:
        out.extend(l if isinstance(l, list) else [l])
    return out


items = []


def add(**kw):
    it = {"id": kw.pop("id"), "layer": kw.pop("layer"), "kind": kw.pop("kind"), "name": kw.pop("name", None)}
    if it["kind"] == "resource":
        it["update"] = kw.pop("update")
    it["summary"] = kw.pop("summary")
    it["sources"] = kw.pop("sources")
    it["min_api"] = kw.pop("min_api", None)
    it["status"] = kw.pop("status", "n/a" if it["kind"] == "skip" else "missing")
    it["existing"] = kw.pop("existing", [])
    if "status_note" in kw:
        it["status_note"] = kw.pop("status_note")
    it["cli_only"] = kw.pop("cli_only", True)
    if "skip_reason" in kw:
        it["skip_reason"] = kw.pop("skip_reason")
    if "collision" in kw:
        it["collision"] = kw.pop("collision")
    it.update(kw)
    items.append(it)


# =========================================================== SCOPE A: core CLI, no Engine endpoint
add(id="cli-only:context", layer="cli-only", kind="skip",
    summary="docker context create/update/rm/use/show/ls/inspect/export/import: named daemon endpoints stored in ~/.docker/contexts.",
    sources=srcs(*[cli("context " + s) for s in ["create", "update", "rm", "use", "show", "ls", "inspect", "export", "import"]], cli("context")),
    existing=["provider argument context", "provider arguments host / tls / ssh"],
    skip_reason="Client-side config store, no Engine API; the provider's `context`, `host`, `tls` and `ssh` arguments already select the daemon, and writing files under ~/.docker from Terraform would be a local-file side effect.",
    collision="A `docker_context` resource would be confusable with the provider-level `context` argument.")

add(id="cli-only:login", layer="cli-only", kind="skip",
    summary="docker login: validate registry credentials (POST /auth) and store them in config.json or a credential helper.",
    sources=srcs(cli("login"), sw("SystemAuth", "POST", "/auth")),
    existing=["provider block registry_auth", "docker_registry_token (ephemeral resource)"],
    cli_only=False,
    skip_reason="Only the validation half is Engine API (POST /auth, which Z1 owns); persisting credentials is client config. Terraform supplies credentials per run via provider registry_auth (password_wo) and ephemeral docker_registry_token.")

add(id="cli-only:logout", layer="cli-only", kind="skip",
    summary="docker logout: remove stored registry credentials from config.json / credential helper.",
    sources=srcs(cli("logout")),
    existing=["provider block registry_auth"],
    skip_reason="Pure client config edit, no endpoint; Terraform never persists credentials, so there is nothing to log out of.")

add(id="cli-only:manifest-list", layer="cli-only", kind="resource", name="docker_registry_manifest_list", update="in_place",
    summary="Build a manifest list / OCI index from existing per-platform images in a registry, annotate entries (os/arch/variant/features) and push it under a tag; delete the tag on destroy.",
    sources=srcs(cli("manifest create"), cli("manifest annotate"), cli("manifest push"), cli("manifest rm"), cli("manifest"),
                 bx("imagetools create")),
    status="missing",
    existing=["docker_registry_image (resource)"],
    status_note="docker_registry_image pushes single images; nothing assembles an index from existing digests.",
    collision="Same job as `docker buildx imagetools create` (buildx:imagetools-create points here). Do not also add docker_buildx_imagetools / docker_manifest. `docker manifest rm` only deletes the local draft, so destroy semantics (registry DELETE of the tag vs leave it) need a flag like docker_registry_image.keep_remotely.",
    implementation_note="Registry HTTP API v2 (distribution), not Engine API; the provider already has registry auth + manifest fetch code for docker_registry_image_manifests.")

add(id="cli-only:manifest-inspect", layer="cli-only", kind="data_source", name="docker_registry_image_manifests",
    summary="docker manifest inspect: read an image manifest or manifest list straight from the registry.",
    sources=srcs(cli("manifest inspect"), bx("imagetools inspect"), sw("DistributionInspect", "GET", "/distribution/{name}/json")),
    status="exists", existing=["docker_registry_image_manifests (data source)", "docker_registry_image (data source)"],
    collision="buildx:imagetools-inspect maps to the same data source.")

add(id="cli-only:stack", layer="cli-only", kind="resource", name="docker_stack", update="in_place",
    summary="docker stack deploy / rm: client-side fan-out of a compose file into namespaced networks, secrets, configs and services (label com.docker.stack.namespace).",
    sources=srcs(cli("stack deploy"), cli("stack rm"), cli("stack"),
                 sw("ServiceCreate", "POST", "/services/create"), sw("ServiceUpdate", "POST", "/services/{id}/update"),
                 sw("ServiceDelete", "DELETE", "/services/{id}"), sw("NetworkCreate", "POST", "/networks/create")),
    status="exists", existing=["docker_stack (resource)", "docker_compose (resource, swarm mode)"],
    collision="Keep `docker_stack` (exists); do not rename to docker_swarm_stack even though it is a swarm-layer type.",
    stack_deploy_flags_note="--prune, --resolve-image and --with-registry-auth should be checked against docker_stack attributes by the implementing lane.")

add(id="cli-only:stacks", layer="cli-only", kind="data_source", name="docker_stacks",
    summary="docker stack ls: stacks derived by grouping services by the com.docker.stack.namespace label (name, service count).",
    sources=srcs(cli("stack ls"), sw("ServiceList", "GET", "/services")),
    status="partial", existing=["docker_services (data source)"],
    status_note="docker_services with a label filter returns the services but not the grouped stack list.",
    cli_only=True)

add(id="cli-only:stack-services", layer="cli-only", kind="data_source", name="docker_stack",
    summary="docker stack services: the services of one stack (GET /services filtered by namespace label), with replica counts from tasks.",
    sources=srcs(cli("stack services"), sw("ServiceList", "GET", "/services"), sw("TaskList", "GET", "/tasks")),
    status="partial", existing=["docker_services (data source)"],
    status_note="docker_services + label filter covers the list; running/desired replica counts (from tasks) are missing.",
    collision="Data source `docker_stack` would share its name with the existing docker_stack resource (allowed by Terraform, same pattern as docker_image); alternative name docker_stack_services.")

add(id="cli-only:stack-ps", layer="cli-only", kind="skip",
    summary="docker stack ps: tasks of a stack (GET /tasks filtered by namespace label).",
    sources=srcs(cli("stack ps"), sw("TaskList", "GET", "/tasks")),
    skip_reason="Same as the tasks list data source Z1 derives from GET /tasks with a label filter (com.docker.stack.namespace); no separate type.")

add(id="cli-only:stack-config", layer="cli-only", kind="skip",
    summary="docker stack config: print the merged, interpolated compose file a stack deploy would use.",
    sources=srcs(cli("stack config")),
    existing=["tools/compose2stack", "provider::docker::compose_env (function)"],
    skip_reason="Pure client-side compose rendering with no daemon call; covered by tools/compose2stack and the compose lane.")

add(id="cli-only:checkpoint", layer="cli-only", kind="skip",
    summary="docker checkpoint create/ls/rm: CRIU checkpoint of a running container (experimental).",
    sources=srcs(cli("checkpoint create"), cli("checkpoint ls"), cli("checkpoint rm"), cli("checkpoint")),
    cli_only=True,
    skip_reason="Experimental (needs experimental daemon + CRIU, Linux only); daemon endpoints /containers/{id}/checkpoints exist but are undocumented and absent from swagger.yaml 1.56. Revisit only if moby documents them (then: action docker_container_checkpoint).")

add(id="cli-only:system-dial-stdio", layer="cli-only", kind="skip",
    summary="docker system dial-stdio: proxy stdio to the daemon socket (used by ssh:// connections).",
    sources=srcs(cli("system dial-stdio")),
    skip_reason="Transport plumbing for the CLI's ssh:// helper; the provider's ssh block is pure Go and does not need it.")

add(id="cli-only:trust", layer="cli-only", kind="skip",
    summary="docker trust inspect/sign/revoke/key generate|load/signer add|remove: Docker Content Trust (Notary v1) signing.",
    sources=srcs(*[cli(s, TR) for s in ["trust", "trust inspect", "trust key", "trust key generate", "trust key load",
                                        "trust revoke", "trust sign", "trust signer", "trust signer add", "trust signer remove"]]),
    skip_reason="Notary v1 client against a Notary server, no Engine API; split out of docker/cli into the docker-trust plugin and DCT is being retired by Docker Hub. Signing belongs to cosign/notation-style providers.")

# =========================================================== SCOPE B: buildx v0.38.2
BK = "BuildKit gRPC/session, not Engine API"
add(id="buildx:build", layer="buildx", kind="resource", name="docker_image", update="replace",
    summary="docker buildx build: build with BuildKit (multi-platform, exporters, cache import/export, secrets, ssh, attestations).",
    sources=srcs(bx("build"), cli("build"), cli("image build"), cli("builder build"), sw("ImageBuild", "POST", "/build"), sw("Session", "POST", "/session")),
    status="partial", existing=["docker_image (resource, build block)", "docker_buildx_builder (resource)"],
    status_note="docker_image.build has context, dockerfile, target, args, labels, platform(s), pull, no_cache, cache_from, secrets, builder. Missing: --output/exporters (local, tar, oci, registry with push), --cache-to, --ssh, --attest/--sbom/--provenance, --annotation, --network, --add-host, --build-context (named contexts), --shm-size, --ulimit, --call/--check, --iidfile/metadata, --policy (0.38).",
    cli_only=True, channel=BK,
    collision="Do not add docker_buildx_build: it would duplicate docker_image. If exporters that produce no local image are wanted, that is an action (docker_buildx_bake or docker_image_build action), not a second build resource.")

add(id="buildx:bake", layer="buildx", kind="action", name="docker_buildx_bake",
    summary="docker buildx bake / docker bake: build one or more targets from a bake file (HCL/JSON/compose) in one BuildKit session.",
    sources=srcs(bx("bake"), cli("bake"), {"type": "doc", "file": BXD + "bake-reference.md", "anchor": "#file-format"}),
    cli_only=True, channel=BK,
    design_note="Action, not resource: bake outputs are often not local images (registry push, local/tar exporters) and the target set is chosen per run. The action takes either a bake file path + target names, or inline `target` blocks mirroring bake's target attributes; Terraform variables/for_each replace bake variables/groups/matrix. Images that Terraform should own stay docker_image.",
    collision="Overlaps docker_image (build block) for single targets.")

add(id="buildx:builder", layer="buildx", kind="resource", name="docker_buildx_builder", update="in_place",
    summary="docker buildx create / rm: create and remove a BuildKit builder instance.",
    sources=srcs(bx("create"), bx("rm")),
    status="partial", existing=["docker_buildx_builder (resource)"],
    status_note="Only the docker-container driver, single node: no kubernetes/remote/cloud drivers, no --append/--node/--leave multi-node builders, no --platform pinning per node, no --driver-opt beyond image/network/env/restart/memory etc. Builder is a container + state volume created via Engine API; buildx's local store (~/.docker/buildx/instances) is not written, so `docker buildx ls` will not list it.",
    cli_only=True, channel="Engine API for the docker-container driver's container/volume; BuildKit gRPC for bootstrap/workers")

add(id="buildx:builder-inspect", layer="buildx", kind="data_source", name="docker_buildx_builder",
    summary="docker buildx inspect: driver, nodes, endpoint, status, BuildKit version, platforms, GC policy of a builder.",
    sources=srcs(bx("inspect")),
    status="missing", existing=["docker_buildx_builder (resource, computed attrs)"],
    cli_only=True, channel=BK,
    collision="Same name as the resource (resource/data source pair, allowed). For builders not created by Terraform, discovery is limited to buildx_buildkit_* containers on the daemon (buildx's builder store is client-side).")

add(id="buildx:builders", layer="buildx", kind="data_source", name="docker_buildx_builders",
    summary="docker buildx ls: builders and their nodes, status, platforms.",
    sources=srcs(bx("ls")),
    cli_only=True, channel=BK,
    status_note="Only daemon-visible builders (buildx_buildkit_* containers, plus the built-in `default`/docker driver) can be listed without reading ~/.docker/buildx.")

add(id="buildx:use", layer="buildx", kind="skip",
    summary="docker buildx use: set the current builder in the client's buildx config.",
    sources=srcs(bx("use")),
    existing=["docker_image.build.builder (attribute)"],
    skip_reason="Client-side default selection; Terraform references the builder explicitly via docker_image.build.builder.")

add(id="buildx:stop", layer="buildx", kind="action", name="docker_buildx_builder_stop",
    summary="docker buildx stop: stop a builder's BuildKit node (the buildx_buildkit_* container) without removing it.",
    sources=srcs(bx("stop"), sw("ContainerStop", "POST", "/containers/{id}/stop")),
    cli_only=True,
    channel="For the docker-container driver this is Engine ContainerStop; for remote drivers there is nothing to stop.",
    collision="Low value: docker_buildx_builder with bootstrap=false or a must_run-style flag could cover it; consider skip.")

add(id="buildx:prune", layer="buildx", kind="action", name="docker_buildx_prune",
    summary="docker buildx prune: remove build cache on a builder (filters, keep-storage/min-free-space, --all).",
    sources=srcs(bx("prune"), sw("BuildPrune", "POST", "/build/prune")),
    status="partial", existing=["docker_system_prune (action)"],
    status_note="docker_system_prune can include the daemon's build cache (POST /build/prune) for the built-in builder only; nothing prunes a docker-container builder's cache.",
    cli_only=True, channel=BK + " (docker driver falls back to POST /build/prune)",
    collision="Z1 will likely propose an Engine action for POST /build/prune (e.g. docker_build_cache_prune / docker_builder_prune). One type should take an optional `builder` and choose BuildKit gRPC vs POST /build/prune.")

add(id="buildx:du", layer="buildx", kind="data_source", name="docker_buildx_disk_usage",
    summary="docker buildx du: build cache records and sizes on a builder.",
    sources=srcs(bx("du"), sw("SystemDataUsage", "GET", "/system/df")),
    cli_only=True, channel=BK + " (docker driver: GET /system/df BuildCache)",
    collision="Overlaps Z1's system df data source (BuildCache section); merge or keep builder-scoped.")

add(id="buildx:dial-stdio", layer="buildx", kind="skip",
    summary="docker buildx dial-stdio: proxy the BuildKit API over stdio.",
    sources=srcs(bx("dial-stdio")),
    skip_reason="Transport plumbing for other tools; the provider dials BuildKit directly.")

add(id="buildx:dap-build", layer="buildx", kind="skip",
    summary="docker buildx dap build: run a build under a Debug Adapter Protocol debugger.",
    sources=srcs(bx("dap"), bx("dap build")),
    skip_reason="Interactive IDE debugging session; not declarative.")

add(id="buildx:debug-build", layer="buildx", kind="skip",
    summary="docker buildx debug build: interactive debug shell on build failure (experimental).",
    sources=srcs(bx("debug"), bx("debug build")),
    skip_reason="Experimental, interactive; also newer than the installed buildx v0.34.1 (not in help capture).")

add(id="buildx:history-ls", layer="buildx", kind="data_source", name="docker_buildx_history",
    summary="docker buildx history ls: build records on a builder (ref, name, status, duration, cache ratio).",
    sources=srcs(bx("history ls"), bx("history")),
    cli_only=True, channel=BK + " (ListenBuildHistory)",
    status_note="Low priority; useful only for auditing builds Terraform ran.")

add(id="buildx:history-inspect", layer="buildx", kind="data_source", name="docker_buildx_history_record",
    summary="docker buildx history inspect: details of one build record (args, platforms, outputs, materials, errors).",
    sources=srcs(bx("history inspect")),
    cli_only=True, channel=BK,
    collision="Could be folded into docker_buildx_history as a `ref` filter; naming question.")

for slug, cmd, reason in [
    ("history-inspect-attachment", "history inspect attachment", "Returns raw attachment blobs (SBOM/provenance) of a record; low value as state, and docker_image attestations are Z1's ImageAttestations territory."),
    ("history-logs", "history logs", "Build log stream; logs are not Terraform state."),
    ("history-export", "history export", "Exports a Docker Desktop .dockerbuild bundle (Desktop UI artifact)."),
    ("history-import", "history import", "Imports a Docker Desktop .dockerbuild bundle (Desktop UI artifact)."),
    ("history-open", "history open", "Opens the Docker Desktop UI."),
    ("history-rm", "history rm", "Deleting build records is housekeeping; covered well enough by buildx prune / builder recreation. Revisit as an action if asked."),
    ("history-trace", "history trace", "Starts a local Jaeger UI for the trace; interactive."),
    ("version", "version", "Client version print."),
    ("policy-eval", "policy eval", "Build-policy (Rego) development tooling, client-local; build policy itself is a --policy input on build/bake."),
    ("policy-test", "policy test", "Build-policy (Rego) development tooling, client-local."),
    ("policy-json-schema", "policy json-schema", "Prints a JSON schema; newer than installed buildx v0.34.1."),
]:
    s = bx(cmd)
    if cmd.startswith("policy") and slug == "policy-eval":
        s = srcs(bx("policy"), s)
    add(id="buildx:" + slug, layer="buildx", kind="skip", summary="docker buildx " + cmd + ".", sources=s, skip_reason=reason)

add(id="buildx:replay-build", layer="buildx", kind="skip",
    summary="docker buildx replay build: rebuild an image from its provenance attestation and pinned materials (new in buildx 0.38).",
    sources=srcs(bx("replay"), bx("replay build")),
    skip_reason="New and experimental reproducibility tooling, newer than installed buildx v0.34.1; revisit as an action docker_buildx_replay once stable.")

add(id="buildx:imagetools-create", layer="buildx", kind="skip",
    summary="docker buildx imagetools create: create/push a manifest list or index from existing images (with annotations), server-side copy.",
    sources=srcs(bx("imagetools create"), bx("imagetools")),
    existing=[],
    skip_reason="Same object as cli-only:manifest-list (proposed docker_registry_manifest_list); one resource covers both `docker manifest create/push` and imagetools create.",
    collision="docker_buildx_imagetools_create would duplicate docker_registry_manifest_list.")

add(id="buildx:imagetools-inspect", layer="buildx", kind="data_source", name="docker_registry_image_manifests",
    summary="docker buildx imagetools inspect: manifest/index, platforms, provenance and SBOM of a registry image.",
    sources=srcs(bx("imagetools inspect")),
    status="partial", existing=["docker_registry_image_manifests (data source)"],
    status_note="Manifests and platforms are covered; provenance/SBOM attestation content (--format '{{json .Provenance}}') is not.",
    cli_only=True,
    collision="Same type as cli-only:manifest-inspect.")

# bake file elements
BR = BXD + "bake-reference.md"
add(id="buildx:bake-target", layer="buildx", kind="skip",
    summary="Bake `target` block: one build (context, dockerfile, args, tags, platforms, cache-from/to, output, secret, ssh, attest, matrix, inherits, call, entitlements, policy...).",
    sources=[{"type": "doc", "file": BR, "anchor": "#target"}],
    skip_reason="Not a Terraform type: becomes a nested `target` block of the docker_buildx_bake action (and its attributes mirror docker_image.build). `inherits` and `matrix` map to Terraform locals/for_each.")
add(id="buildx:bake-group", layer="buildx", kind="skip",
    summary="Bake `group` block: a named set of targets built together.",
    sources=[{"type": "doc", "file": BR, "anchor": "#group"}],
    skip_reason="A list of target names; in Terraform this is the docker_buildx_bake action's `targets` argument or a for_each over targets.")
add(id="buildx:bake-variable", layer="buildx", kind="skip",
    summary="Bake `variable` block: typed, overridable inputs with env-var defaults.",
    sources=[{"type": "doc", "file": BR, "anchor": "#variable"}],
    skip_reason="Terraform variables/locals replace them; when the action consumes an existing bake file, overrides go through a `set`/`variables` map argument.")
add(id="buildx:bake-function", layer="buildx", kind="skip",
    summary="Bake `function` blocks and the bake standard library (go-cty functions, mostly the same set Terraform has).",
    sources=[{"type": "doc", "file": BR, "anchor": "#function"}, {"type": "doc", "file": BXD + "bake-stdlib.md"}],
    skip_reason="Bake HCL is itself HCL built on the same go-cty stdlib; Terraform functions (plus provider functions) replace user functions.")

# =========================================================== SCOPE C: CLI plugins
PLUG = {
    "agent": ("Docker AI Agent Runner (docker-agent): run, share and serve AI agents.", "Docker Desktop AI product plugin; talks to model providers and its own runtime, not the Engine API."),
    "ai": ("Ask Gordon: Docker Desktop AI assistant.", "Interactive Docker Desktop product feature; no Engine API."),
    "debug": ("docker debug: shell into any image or container with a toolbox.", "Docker Desktop (Pro) plugin; composite of Engine calls (create helper container, exec) plus proprietary toolbox; interactive. docker_exec covers the declarative part."),
    "desktop": ("Docker Desktop control (start/stop/restart, engine, kubernetes, update, logs, diagnose).", "Controls the Docker Desktop app via its backend socket, not the Engine API."),
    "dhi": ("Docker Hardened Images catalog, mirror, auth, attestations.", "Docker Hub product API (DHI), not Engine API; mirroring could later be a registry-side resource in a Hub provider."),
    "extension": ("Docker Desktop extensions install/ls/rm/update/dev/validate.", "Docker Desktop extension manager; not Engine API."),
    "init": ("docker init: generate Dockerfile, compose.yaml and .dockerignore starter files.", "Interactive local file scaffolding; no daemon involved."),
    "mcp": ("Docker MCP Toolkit: catalog, gateway, servers, secrets, OAuth, profiles.", "Docker Desktop MCP product; own gateway/catalog APIs, not Engine API (gateway runs MCP servers as containers through Docker but via its own logic)."),
    "model": ("Docker Model Runner: pull/run/list/package/push models, runner install/start/stop.", "Model Runner has its own HTTP API (OpenAI-compatible, /models, /engines via the Desktop socket or localhost:12434, or a docker/model-runner container on plain Engine), not the Engine API. Plausible future provider surface (docker_model resource for pull/rm, install-runner as a container), but it is a separate API; out of scope for Engine-API parity."),
    "offload": ("Docker Offload: run builds/containers on Docker cloud.", "Docker cloud product session; not Engine API (it switches the daemon endpoint)."),
    "pass": ("Docker Pass secrets manager (beta): get/set/ls/rm secrets, run with secrets.", "Local/Desktop secrets store; Terraform has its own secret handling (write-only args, ephemeral)."),
    "sandbox": ("Docker Sandboxes: microVM sandboxes for coding agents (create, exec, run, network, save).", "Docker Desktop sandbox product with its own VM backend API, not Engine API."),
    "scout": ("Docker Scout: CVEs, SBOM, policy, recommendations, compare, repo enrollment.", "Docker Scout SaaS/analysis tool; not Engine API. Vulnerability data could later be a data source in a Scout/Hub provider."),
}
for p, (summary, reason) in PLUG.items():
    h = "docker_%s.txt" % p
    s = [{"type": "cli", "command": "docker %s --help" % p, "file": HELP + h}] if h in HELP_FILES else []
    s.append({"type": "cli", "command": "docker --help", "file": HELP + "docker.txt"})
    add(id="plugin:" + p, layer="cli-only", kind="skip", summary=summary, sources=s, skip_reason=reason)

# =========================================================== write
ids = [i["id"] for i in items]
dups = [k for k, v in collections.Counter(ids).items() if v > 1]
assert not dups, dups
doc = {
    "lane": "Z3",
    "notes": [
        "Scope: docker CLI 29.5.3 docs (commandline/*.md + docker-trust plugin docs), buildx v0.38.2 docs, CLI plugins seen in the Docker Desktop 4.78 help capture. Compose plugin is another lane.",
        "Items exist only for commands with no Engine API endpoint (or whose Engine endpoint covers only a minor part: login). Every other core command is handled_by \"engine\" in z3-cli-map.json for lane Z1.",
        "buildx create/rm are grouped into one resource item (schema rule: one resource per object); other buildx commands get one item each. Group pages (buildx, history, imagetools, policy, dap, debug, replay) are folded into their children's sources.",
        "Buildx commands newer than the installed v0.34.1 (docs present, help absent): buildx debug, debug build, policy json-schema, replay, replay build. `history trace` is in both.",
        "cli_only=true marks client-side/BuildKit/registry/plugin work. Items whose `channel` field mentions BuildKit talk gRPC to the builder (with the docker driver, through POST /session + the undocumented /grpc upgrade), not the documented Engine API.",
        "manifest list proposal reuses the docker_registry_* family (registry API v2), not docker_buildx_* or docker_manifest_*.",
        "Help capture was taken on a non-swarm Docker Desktop: swarm-only commands are missing from help for environmental reasons (see z3-cli-map.json summary).",
    ],
    "naming_questions": [
        "Manifest lists: `docker_registry_manifest_list` (proposed, registry family) vs `docker_manifest_list` vs `docker_buildx_imagetools`? One resource for both `docker manifest create/push` and `buildx imagetools create`.",
        "BuildKit build: keep extending `docker_image` (build block) vs add `docker_buildx_build`? Proposed: extend docker_image, and add action `docker_buildx_bake` for multi-target / non-image outputs.",
        "Stack: keep existing `docker_stack` vs a `docker_swarm_stack` prefix for consistency with docker_swarm_*? Proposed: keep docker_stack; new stack data sources `docker_stacks` and `docker_stack` (or `docker_stack_services`).",
        "Context: skip `docker_context` (provider `context`/`host` arguments already select the daemon) vs a read-only `docker_context` data source that resolves a context's endpoint/TLS from ~/.docker? Proposed: skip.",
        "Build cache prune: one action for both daemon build cache (POST /build/prune, Z1) and buildx builders (BuildKit gRPC): `docker_buildx_prune` vs `docker_build_cache_prune` vs `docker_builder_prune`?",
        "Buildx builder data sources: `docker_buildx_builder` (singular, same name as the resource) + `docker_buildx_builders`; buildx history as `docker_buildx_history` (+ `docker_buildx_history_record`) or one data source with an optional ref?",
        "docker buildx stop: action `docker_buildx_builder_stop` or skip (low value)?",
        "Disk usage: `docker_buildx_disk_usage` separate from Z1's system df data source, or one data source with an optional builder?",
    ],
    "items": items,
}
with open(os.path.join(OUT, "z3-cli.json"), "w", encoding="utf-8", newline="\n") as fh:
    json.dump(doc, fh, indent=1, ensure_ascii=False)
    fh.write("\n")
c = collections.Counter((i["layer"], i["kind"], i["status"]) for i in items)
for k, v in sorted(c.items()):
    print(k, v)
print("total", len(items))
# cross-check handled_by ids exist
m = json.load(open(os.path.join(OUT, "z3-cli-map.json"), encoding="utf-8"))
need = {e["handled_by"] for sec in ("commands", "buildx_commands", "plugin_commands") for e in m[sec]
        if e["handled_by"] not in ("engine", "group", "not-a-command")}
print("missing ids:", sorted(need - set(ids)))
print("unreferenced ids:", sorted(set(ids) - need))
