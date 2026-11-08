"""Pass 4.5 Lane Z corrections (run once, after mech.py, sens.py, desc.py, qs.py).

    cd reference/_gen/fragments/outputs
    python -I -B ../../generators/lanez/p45c.py ../../generators/lanez

1. Logs: logs_json (+ its hash) replaced by the structured `log_entries`; `logs` serializes it and keeps one hash
   (`content_sha256`, the one-raw name); every logs item records its bounded `tail` / `since` inputs.
2. Result (V6): every action gets at least one `result` output.
3. Versions: every item gets `uses_cli`; docker_cli_version is removed from the five cli-only items, none of which
   shells out to the docker CLI.
4. Z.json notes rewritten to the decisions as applied.
"""
import sys
sys.path.insert(0, sys.argv[1])
from frag import load, save, out

D = {L: load(L) for L in "ABCDEZ"}
log = []
SW = "upstream/engine-api/api/swagger.yaml"


def sw(ptr):
    return {"type": "swagger", "file": SW, "pointer": ptr}


def item_of(iid):
    for L in "ABCDE":
        if iid in D[L]["items"]:
            return L, D[L]["items"][iid]
    raise KeyError(iid)


def add(L, it, new, after_cat=("identity",)):
    """Insert `new` after the last output whose category is in after_cat (else at the end)."""
    outs = it["outputs"]
    assert new["name"] not in {o["name"] for o in outs}, new["name"]
    if L in "AB":
        new.setdefault("provided", False)
    idx = [i for i, o in enumerate(outs) if o["category"] in after_cat]
    outs.insert(idx[-1] + 1 if idx else len(outs), new)


# --------------------------------------------------------------------------- 1. logs
LOGS = {  # id: (logs endpoint path, source field upstream, source pointer, source description)
    "engine:container-logs": ("/containers/{id}/logs", "Name (leading \"/\" removed)",
                              "#/definitions/ContainerInspectResponse/properties/Name",
                              "the container name (without the leading \"/\")"),
    "swarm:service-logs": ("/services/{id}/logs", "Spec.Name", "#/definitions/ServiceSpec/properties/Name",
                           "the service name"),
    "swarm:task-logs": ("/tasks/{id}/logs", "ID", "#/definitions/Task/properties/ID", "the task ID"),
    "compose:cli-logs": ("/containers/{id}/logs",
                         "Labels[com.docker.compose.service] + \".\" + Names[0] (leading \"/\" removed)",
                         "#/definitions/ContainerSummary/properties/Labels",
                         "\"<compose service>.<container name>\""),
}
TAIL_DEFAULT = 1000


def esc(path):
    return path.replace("/", "~1")


def entries_output(path, src_up, src_ptr, src_desc, kaa):
    p = "#/paths/%s/get" % esc(path)
    return {
        "name": "log_entries",
        "tf_type": "list(object({timestamp=string, stream=string, source=string, message=string}))",
        "category": "structured",
        "source": sw(p),
        "known_after_apply": kaa,
        "sensitive": False,
        "description": "One entry per log line in arrival order, read with timestamps=true and bounded by the tail/since "
                       "inputs; `source` is " + src_desc + ". Only `message` is sensitive (log lines can print secrets).",
        "fields": [
            {"name": "timestamp", "upstream": "timestamps=true line prefix (RFC 3339 nano)", "tf_type": "string",
             "pointer": p + "/parameters"},
            {"name": "stream", "upstream": "multiplexed frame header stream type: 1 = stdout, 2 = stderr; a TTY "
             "stream is not multiplexed (the Engine merges stdout and stderr at the source), so it is \"combined\"",
             "tf_type": "string", "pointer": p + "/responses/200"},
            {"name": "source", "upstream": src_up, "tf_type": "string", "pointer": src_ptr},
            {"name": "message", "upstream": "log line without the timestamp prefix and trailing newline",
             "tf_type": "string", "pointer": p + "/responses/200", "sensitive": True},
        ],
        "fields_complete": True,
    }


def inputs_for(iid, path):
    p = "#/paths/%s/get/parameters" % esc(path)
    if iid == "compose:cli-logs":
        src = lambda flag: {"type": "cli", "command": "docker compose logs", "flag": flag,
                            "file": "upstream/compose-cli/docs/reference/compose_logs.md"}
        tail_src, since_src, per = src("--tail"), src("--since"), "container"
    else:
        tail_src, since_src = sw(p), sw(p)
        per = {"engine:container-logs": "container", "swarm:service-logs": "service",
               "swarm:task-logs": "task"}[iid]
    return [
        {"name": "tail", "tf_type": "number", "default": TAIL_DEFAULT, "upstream": "tail", "source": tail_src,
         "description": "Lines to keep from the end of the logs per %s (Engine `tail`); default %d so state cannot "
                        "grow without bound. 0 reads none; there is no \"all\"." % (per, TAIL_DEFAULT)},
        {"name": "since", "tf_type": "string", "default": None, "upstream": "since", "source": since_src,
         "description": "Only lines at or after this time: RFC 3339, or a duration before now such as \"42m\" "
                        "(sent as the Engine's UNIX-timestamp `since`). Combined with tail when both are set."},
    ]


for iid, (path, src_up, src_ptr, src_desc) in LOGS.items():
    L, it = item_of(iid)
    outs = it["outputs"]
    names = [o["name"] for o in outs]
    logs = out(D[L], iid, "logs")
    kaa = logs["known_after_apply"]
    # drop logs_json, its hash, the old per-raw hash of logs, and compose's old `entries`
    for gone in ("logs_json", "logs_json_sha256", "logs_sha256", "entries"):
        if gone in names:
            outs.remove(out(D[L], iid, gone))
    logs["source"] = {"type": "derived", "from": "log_entries",
                      "how": "one line per entry, \"<timestamp> <stream> <source> <message>\", joined with \\n"}
    logs["serializes"] = "log_entries"
    logs["sensitive"] = True
    logs["description"] = ("The log entries as plain text, one line per entry: \"<timestamp> <stream> <source> "
                           "<message>\". Sensitive: log lines can print secrets.")
    i = outs.index(logs)
    h = {"name": "content_sha256", "tf_type": "string", "category": "hash",
         "source": {"type": "derived", "from": "logs", "how": "hex sha256 of the UTF-8 bytes of logs"},
         "known_after_apply": kaa, "sensitive": False, "hash_of": "logs", "description": "sha256 of logs."}
    e = entries_output(path, src_up, src_ptr, src_desc, kaa)
    if L in "AB":
        h["provided"] = False
        e["provided"] = False
    outs.insert(i + 1, e)
    outs.insert(i + 2, h)
    it.pop("no_structured_reason", None)
    it["inputs"] = inputs_for(iid, path)
    log.append((L, iid, "logs_json + hashes removed; log_entries added; logs hash -> content_sha256; inputs tail/since"))

# outputs_note text that named the removed outputs
NOTE_FIX = {
    "engine:container-logs": "Exposed today: none. Missing: container_id, logs, log_entries, content_sha256, "
                             "engine_api_version, docker_server_version, and the tail/since inputs.",
}
for iid, txt in NOTE_FIX.items():
    item_of(iid)[1]["outputs_note"] = txt

# --------------------------------------------------------------------------- 2. result outputs for actions (V6)
FSC = "#/definitions/FilesystemChange"
CS = "#/definitions/ContainerSummary/properties/"
II = "#/definitions/ImageInspect/properties/"
STAT = "#/paths/~1containers~1{id}~1archive/head/responses/200/headers/X-Docker-Container-Path-Stat"


def archive_size(what):
    return {"name": "archive_size", "tf_type": "number", "category": "result",
            "source": {"type": "derived", "from": what, "how": "byte count of the tar stream"},
            "known_after_apply": True, "sensitive": False,
            "description": "Bytes in the tar archive the action " + what.split(" ", 1)[0] + "."}


def path_stat(desc):
    return {"name": "path_stat", "tf_type": "object({...})", "category": "result", "source": sw(STAT),
            "known_after_apply": True, "sensitive": False, "description": desc,
            "fields": [{"name": n, "upstream": u, "tf_type": t, "pointer": STAT} for n, u, t in
                       (("name", "name", "string"), ("size", "size", "number"), ("mode", "mode", "number"),
                        ("mtime", "mtime", "string"), ("link_target", "linkTarget", "string"))],
            "fields_complete": True}


def changes():
    return {"name": "changes", "tf_type": "list(object({...}))", "category": "result",
            "source": sw("#/paths/~1containers~1{id}~1changes/get/responses/200/schema"),
            "known_after_apply": True, "sensitive": False,
            "description": "The container filesystem changes the commit captured (GET /containers/{id}/changes "
                           "just before the commit).",
            "fields": [{"name": "path", "upstream": "Path", "tf_type": "string", "pointer": FSC + "/properties/Path"},
                       {"name": "kind", "upstream": "Kind", "tf_type": "number", "pointer": "#/definitions/ChangeType"}],
            "fields_complete": True}


IMG_FIELDS = [("size", "Size", "number"), ("created", "Created", "string"),
              ("architecture", "Architecture", "string"), ("os", "Os", "string"), ("variant", "Variant", "string")]


def image_summary(name, tf_type, desc):
    return {"name": name, "tf_type": tf_type, "category": "result", "source": sw("#/definitions/ImageInspect"),
            "known_after_apply": True, "sensitive": False, "description": desc,
            "fields": [{"name": n, "upstream": u, "tf_type": t, "pointer": II + u} for n, u, t in IMG_FIELDS],
            "fields_complete": True}


def container_states(desc):
    return {"name": "container_states", "tf_type": "list(object({...}))", "category": "result",
            "source": sw("#/paths/~1containers~1json/get/responses/200/schema"),
            "known_after_apply": True, "sensitive": False, "description": desc,
            "fields": [{"name": "id", "upstream": "Id", "tf_type": "string", "pointer": CS + "Id"},
                       {"name": "name", "upstream": "Names[0] (leading \"/\" removed)", "tf_type": "string",
                        "pointer": CS + "Names"},
                       {"name": "service", "upstream": "Labels[com.docker.compose.service]", "tf_type": "string",
                        "pointer": CS + "Labels"},
                       {"name": "state", "upstream": "State", "tf_type": "string", "pointer": CS + "State"},
                       {"name": "status", "upstream": "Status", "tf_type": "string", "pointer": CS + "Status"}],
            "fields_complete": True}


def final_status(name, tf_type, ptr, desc):
    return {"name": name, "tf_type": tf_type, "category": "result", "source": sw(ptr),
            "known_after_apply": True, "sensitive": False, "description": desc}


AFTER = "after the action, from GET /containers/json filtered by the project label."
RESULTS = {
    "engine:container-export": archive_size("streamed from GET /containers/{id}/export"),
    "engine:container-upload": path_stat("Stat of the target path after the upload (HEAD /containers/{id}/archive, "
                                         "X-Docker-Container-Path-Stat)."),
    "engine:image-build": image_summary("image", "object({...})", "Summary of the image the build produced "
                                                                  "(GET /images/{id}/json)."),
    "engine:image-commit": changes(),
    "engine:image-import": image_summary("image", "object({...})", "Summary of the imported image "
                                                                   "(GET /images/{id}/json)."),
    "engine:image-load": archive_size("read from the input archive for POST /images/load"),
    "engine:image-save": archive_size("streamed from GET /images/get"),
    "engine:plugin-create": final_status("enabled", "bool", "#/definitions/Plugin/properties/Enabled",
                                         "Whether the plugin is enabled after create (a created plugin starts "
                                         "disabled)."),
    "engine:plugin-push": final_status("push_status", "string", "#/definitions/PushImageInfo/properties/status",
                                       "Last status message of the push stream (\"<tag>: digest: sha256:… size: N\")."),
    "swarm:swarm-rotate-unlock-key": final_status(
        "autolock_managers", "bool", "#/definitions/SwarmSpec/properties/EncryptionConfig/properties/AutoLockManagers",
        "Whether autolock is on after the rotation (the new key only matters when it is)."),
    "compose:cli-build": image_summary("built_images", "map(object({...}))",
                                       "Service name -> summary of the image built for it (GET /images/{id}/json)."),
    "compose:cli-commit": changes(),
    "compose:cli-cp": path_stat("Stat of the container-side path after the copy (HEAD /containers/{id}/archive)."),
    "compose:cli-down": {"name": "removed_counts", "tf_type": "object({...})", "category": "result",
                         "source": {"type": "derived", "from": "removed_*", "how": "length of each removed_* list"},
                         "known_after_apply": True, "sensitive": False,
                         "description": "How many containers, networks, volumes and images `down` removed.",
                         "fields": [{"name": n, "upstream": "len(removed_%s)" % k, "tf_type": "number",
                                     "pointer": "#/paths/~1%s~1{id}/delete" % p}
                                    for n, k, p in (("containers", "container_ids", "containers"),
                                                    ("networks", "network_ids", "networks"),
                                                    ("volumes", "volume_names", "volumes"),
                                                    ("images", "image_ids", "images"))],
                         "fields_complete": True},
    "compose:cli-export": archive_size("streamed from GET /containers/{id}/export"),
    "compose:cli-kill": container_states("State of each service container " + AFTER),
    "compose:cli-pause": container_states("State of each service container " + AFTER),
    "compose:cli-publish": {"name": "layers", "tf_type": "list(object({...}))", "category": "result",
                            "source": {"type": "cli", "command": "docker compose publish",
                                       "file": "upstream/compose-cli/docs/reference/compose_publish.md"},
                            "known_after_apply": True, "sensitive": False,
                            "description": "OCI descriptors of the layers pushed in the published artifact "
                                           "(compose files, env files).",
                            "fields": [{"name": n, "upstream": u, "tf_type": t,
                                        "pointer": "OCI image-spec descriptor " + u}
                                       for n, u, t in (("media_type", "mediaType", "string"),
                                                       ("digest", "digest", "string"), ("size", "size", "number"))],
                            "fields_complete": True},
    "compose:cli-pull": final_status("pull_status", "map(string)", "#/definitions/CreateImageInfo/properties/status",
                                     "Service name -> last status of the pull stream (\"Image is up to date for …\" "
                                     "or \"Downloaded newer image for …\")."),
    "compose:cli-push": final_status("push_status", "map(string)", "#/definitions/PushImageInfo/properties/status",
                                     "Service name -> last status of the push stream "
                                     "(\"<tag>: digest: sha256:… size: N\")."),
    "compose:cli-restart": container_states("State of each service container " + AFTER),
    "compose:cli-scale": container_states("State of each service container " + AFTER),
    "compose:cli-start": container_states("State of each service container " + AFTER),
    "compose:cli-stop": container_states("State of each service container " + AFTER),
    "compose:cli-unpause": container_states("State of each service container " + AFTER),
    "compose:cli-up": container_states("State of each service container " + AFTER),
}
for iid, new in RESULTS.items():
    L, it = item_of(iid)
    assert not any(o["category"] == "result" for o in it["outputs"]), iid
    add(L, it, new)
    log.append((L, iid, "+result " + new["name"]))

# --------------------------------------------------------------------------- 3. versions: uses_cli everywhere
CLI_ONLY = {
    "cli-only:stack": "deploys through the Engine API (NetworkCreate, ServiceCreate/Update/Delete, secret and config "
                      "create) with the namespace label; it mirrors `docker stack deploy` but never runs it",
    "cli-only:stacks": "groups GET /services by the com.docker.stack.namespace label; no `docker stack ls`",
    "cli-only:stack-services": "GET /services and GET /tasks filtered by the namespace label; no `docker stack services`",
    "cli-only:manifest-list": "assembles the index and pushes it through the registry v2 HTTP API, not `docker manifest`",
    "cli-only:manifest-inspect": "reads the manifest from the registry v2 HTTP API, not `docker manifest inspect`",
}
for L in "ABCDE":
    for iid, it in D[L]["items"].items():
        it["uses_cli"] = False
for iid, why in CLI_ONLY.items():
    L, it = item_of(iid)
    it["outputs"].remove(out(D[L], iid, "docker_cli_version"))
    it["outputs_note"] = (it.get("outputs_note", "") + " " if it.get("outputs_note") else "") + \
        "uses_cli false: " + why + "; so no docker_cli_version."
    log.append((L, iid, "uses_cli=false, -docker_cli_version"))

# --------------------------------------------------------------------------- 4. Z notes
z = D["Z"]["notes"]


def replace_note(prefix, text):
    m = [i for i, n in enumerate(z) if n.startswith(prefix)]
    assert len(m) == 1, prefix
    z[m[0]] = text


replace_note("Logs:", "Logs (Pass 4.5 Lane Z corrections): every logs item (engine:container-logs, swarm:service-logs, "
             "swarm:task-logs, compose:cli-logs) has raw `logs` (text, one line per entry: \"<timestamp> <stream> "
             "<source> <message>\") hashed as `content_sha256`, and structured `log_entries` "
             "list(object({timestamp, stream, source, message})); stream is stdout|stderr, or combined for a TTY; only "
             "message is sensitive. logs_json is gone. Each records `inputs` tail (default 1000) and since; rule L1.")
replace_note("Version names:", "Version names (Pass 4.5 Lane Z corrections): V3 reads the registry in SCHEMA.md. "
             "docker_cli_version is registered but only allowed on items with uses_cli true; every item now carries "
             "uses_cli, and all are false (the provider never runs the docker CLI), so docker_cli_version was removed "
             "from stack, stacks, stack-services, manifest-list and manifest-inspect.")
z.append("Result (Pass 4.5 Lane Z corrections): rule V6, every action has a result output. 26 actions had none and "
         "got one: archive_size (exports, image save/load), path_stat (upload, cp), changes (commits), image / "
         "built_images (build, import), enabled (plugin create), push_status / pull_status (plugin push, compose "
         "push/pull), autolock_managers (rotate unlock key), removed_counts (compose down), layers (compose publish), "
         "container_states (compose kill/pause/restart/scale/start/stop/unpause/up).")

for L in "ABCDEZ":
    save(L, D[L])
for l in log:
    print(*l)
print("changes", len(log))
