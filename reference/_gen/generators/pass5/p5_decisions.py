"""Pass 5 Lane 0: apply the naming decisions (reference/DECISIONS.md) to the inventory fragments.

Provenance script, run once from the repo root on the fragments of commit 97ec6bf:

    python -I -B reference/_gen/generators/pass5/p5_decisions.py

It edits fragments/*.json and fragments/outputs/*.json in place and writes fragments/pass5.json (new items) and
fragments/outputs/P5.json (their outputs). Re-running it on the edited files fails (asserts) rather than doubling edits.
"""
import copy, json, os, re

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
Z = os.path.join(ROOT, "reference", "_gen", "fragments")
O = os.path.join(Z, "outputs")
SW = "upstream/engine-api/api/swagger.yaml"


def load(p):
    with open(p, encoding="utf-8") as f:
        text = f.read()
    m = re.match(r"\{\n( +)", text)
    return json.loads(text), (len(m.group(1)) if m else 2)


def save(p, d, ind):
    with open(p, "w", encoding="utf-8", newline="\n") as f:
        f.write(json.dumps(d, indent=ind, ensure_ascii=False) + "\n")


FRAG_FILES = ["z1-engine.json", "z2-compose.json", "z3-cli.json", "z4-dockerfile.json", "dockerfile-sources.json"]
OUT_FILES = ["A.json", "B.json", "C.json", "D.json", "E.json", "Z.json"]
frags = {f: load(os.path.join(Z, f)) for f in FRAG_FILES}
outs = {f: load(os.path.join(O, f)) for f in OUT_FILES}
items = {it["id"]: it for f in FRAG_FILES for it in frags[f][0]["items"]}
oitems = {}
for f in OUT_FILES:
    for iid, e in outs[f][0]["items"].items():
        oitems[iid] = e
assert "pass5.json" not in os.listdir(Z), "already applied"


def dec(it, *qs):
    it.setdefault("decisions", [])
    for q in qs:
        if q not in it["decisions"]:
            it["decisions"].append(q)


# ---------------------------------------------------------------- 1. item kinds and names
def skip(iid, q, reason):
    it = items[iid]
    it["skip_previous_name"] = it.get("name")
    it["kind"], it["name"], it["skip_reason"] = "skip", None, reason
    it.pop("update", None)
    dec(it, q)
    for f in OUT_FILES:
        outs[f][0]["items"].pop(iid, None)
    oitems.pop(iid, None)


skip("engine:network-attachment", "Q1", "Q1: no standalone attachment type; docker_container `networks` connects and disconnects in place (NetworkConnect / NetworkDisconnect).")
skip("engine:image-build", "Q3", "Q3: no standalone build action; builds live in docker_image's `build` block (BuildKit, Q20).")
skip("buildx:prune", "Q23", "Q2/Q23: folded into action docker_builder_prune (engine:build-prune), which prunes a buildx builder when `builder` is set.")
skip("buildx:stop", "Q25", "Q25: skipped; a builder is stopped by removing or replacing docker_buildx_builder.")
skip("compose:cli-up", "Q11", "Q11: no up action; creating or updating docker_compose is `up`.")
skip("compose:cli-down", "Q11", "Q11: no down action; destroying docker_compose is `down`.")

RENAME = {
    "engine:container-upload": ("docker_container_cp", "Q9"),
    "engine:container-logs": ("docker_container_logs", "Q6"),
    "swarm:swarm-ca-rotate": ("docker_swarm_rotate_ca", "Q10"),
    "buildx:history-ls": ("docker_buildx_history_records", "Q24"),
    "buildx:history-inspect": ("docker_buildx_history", "Q24"),
}
for iid, (n, q) in RENAME.items():
    items[iid]["previous_name"] = items[iid]["name"]
    items[iid]["name"] = n
    dec(items[iid], q)

ra = items["engine:registry-auth"]
ra["previous_name"], ra["kind"], ra["name"] = ra["name"], "ephemeral_resource", "docker_registry_login"
dec(ra, "Q7")

# Dockerfile blocks are nested blocks / attributes of data.docker_dockerfile (Q27), not Terraform types.
BLOCK = {"FROM": ("docker_dockerfile.stage", "Q31"), "ONBUILD": ("docker_dockerfile.onbuild", "Q30"),
         "MAINTAINER": ("docker_dockerfile.maintainer", "Q29"),
         "directive-syntax": ("docker_dockerfile.syntax", "Q28"), "directive-escape": ("docker_dockerfile.escape", "Q28"),
         "directive-check": ("docker_dockerfile.check", "Q28")}
for iid, it in items.items():
    if it["kind"] == "dockerfile_block":
        key = iid.split(":", 1)[1]
        n, q = BLOCK.get(key, ("docker_dockerfile." + key.lower(), "Q27"))
        it["previous_name"], it["name"] = it["name"], n
        dec(it, "Q27", q)
items["dockerfile:MAINTAINER"]["summary"] = "MAINTAINER (deprecated): parsed when reading text or image history; no input block. Use LABEL org.opencontainers.image.authors (Q29)."
items["dockerfile:ONBUILD"]["summary"] = "ONBUILD: the `onbuild = true` attribute on any instruction block (Q30)."
items["dockerfile:FROM"]["summary"] = "FROM: the `stage { name, from, platform, ... }` block (FROM ... AS name) (Q31)."

DECS = {"engine:container": ["Q1", "Q9"], "engine:build-prune": ["Q2", "Q23"], "engine:image": ["Q3", "Q20", "Q44", "Q46"],
        "engine:image-data": ["Q44", "Q46"], "engine:plugin-create": ["Q8"], "engine:container-file": ["Q9", "Q33", "Q34"],
        "engine:registry-image": ["Q45"], "engine:registry-image-data": ["Q45"], "engine:exec": ["Q33", "Q34", "Q35"],
        "swarm:swarm-node": ["Q4"], "swarm:node-promote": ["Q5", "Q40"], "swarm:node-demote": ["Q5", "Q40"],
        "swarm:service-logs": ["Q6"], "swarm:task-logs": ["Q6"], "swarm:swarm-rotate-unlock-key": ["Q10"],
        "swarm:swarm-rotate-tokens": ["Q10"], "swarm:swarm-init": ["Q38"], "compose:cli-config": ["Q12"],
        "compose:cli-ps": ["Q13"], "compose:cli-ls": ["Q13"], "compose:cli-volumes": ["Q14"], "compose:cli-scale": ["Q16"],
        "compose:jobs": ["Q18"], "compose:cli-run": ["Q18", "Q43"], "compose:cli-exec": ["Q43"], "compose:name": ["Q41", "Q42"],
        "cli-only:manifest-list": ["Q19"], "buildx:build": ["Q20"], "buildx:bake": ["Q20"], "cli-only:stack": ["Q21"],
        "cli-only:stacks": ["Q21", "Q51"], "cli-only:stack-services": ["Q21", "Q50"], "buildx:builder-inspect": ["Q24"],
        "buildx:builders": ["Q24"], "buildx:du": ["Q26"], "dockerfile:data-dockerfile": ["Q27", "Q47", "Q48", "Q49"]}
for iid, qs in DECS.items():
    dec(items[iid], *qs)
for k in ("cli-start", "cli-stop", "cli-restart", "cli-kill", "cli-pause", "cli-unpause"):
    dec(items["compose:" + k], "Q15")
for k in ("start", "stop", "restart", "kill", "pause", "unpause"):
    dec(items["engine:container-" + k], "Q15", "Q40")
for iid in ("compose:" + x for x in ("services", "networks", "volumes", "configs", "secrets", "models", "include")):
    dec(items[iid], "Q17", "Q42")
items["compose:jobs"]["summary"] += " Pass 5 (Q18): a plan-time warning when a project defines jobs; run-on-apply is a later design pass."

# NetworkConnect / NetworkDisconnect now serve docker_container (in place), still homed on the skipped item for coverage.
c = items["engine:container"]
for s in items["engine:network-attachment"]["sources"]:
    if s.get("type") == "swagger" and not any(x.get("operationId") == s["operationId"] for x in c["sources"]):
        c["sources"].append({k: v for k, v in s.items() if k != "primary"})

# Status, status_note and existing are computed by merge.py from the registration (Pass 5); drop the hand-set values.
for it in items.values():
    for k in ("status", "status_note", "existing"):
        it.pop(k, None)


# ---------------------------------------------------------------- 2. outputs edits
def find(iid, name):
    m = [o for o in oitems[iid]["outputs"] if o["name"] == name]
    assert len(m) == 1, (iid, name)
    return m[0]


def drop(iid, name):
    oitems[iid]["outputs"] = [o for o in oitems[iid]["outputs"] if o["name"] != name]


# Q33: exec / run raw streams are stdout + stderr.
for iid in [i for i, e in oitems.items() if items[i]["kind"] == "action" and any(o["name"] == "output" and o["category"] == "raw" for o in e["outputs"])]:
    o = find(iid, "output")
    o["name"] = "stdout"
    for h in oitems[iid]["outputs"]:
        if h.get("hash_of") == "output":
            h["hash_of"] = "stdout"
    o["description"] = "The command's standard output (Q33)."
    e = copy.deepcopy(o)
    e["name"], e["description"] = "stderr", "The command's standard error (Q33)."
    oitems[iid]["outputs"].insert(oitems[iid]["outputs"].index(o) + 1, e)

# Q49 / Q47: docker_dockerfile drops `reconstructed`, adds the source text as a second raw output.
drop("dockerfile:data-dockerfile", "reconstructed")
df = find("dockerfile:data-dockerfile", "dockerfile")
src = copy.deepcopy(df)
src.update(name="source_dockerfile", description="The Dockerfile text exactly as read from content / path / url, before parsing (Q47); null in image mode.")
src.pop("serializes", None)
oitems["dockerfile:data-dockerfile"]["outputs"].insert(oitems["dockerfile:data-dockerfile"]["outputs"].index(df) + 1, src)

# Q41: docker_compose project name is `name`; id = name.
find("compose:name", "project_name")["name"] = "name"
find("compose:name", "name")["description"] = "The compose project name (compose-spec top-level `name`, Q41)."
find("compose:name", "id")["description"] = "Equal to `name` (Q41)."

# Q50 / Q51.
drop("cli-only:stack-services", "tasks")
for fd in find("cli-only:stacks", "stacks")["fields"]:
    if fd["name"] == "services":
        fd["name"], fd["tf_type"] = "services_count", "number"

# Q38: docker_swarm_init keeps cluster_id beside id.
si = oitems["swarm:swarm-init"]["outputs"]
if not any(o["name"] == "cluster_id" for o in si):
    x = copy.deepcopy(find("swarm:swarm-init", "id"))
    x.update(name="cluster_id", description="The swarm's cluster ID (ClusterInfo.ID), equal to `id` (Q38).")
    si.insert(si.index(find("swarm:swarm-init", "id")) + 1, x)

# Q7: the identity token never reaches state; the raw response is sensitive as a whole.
find("engine:registry-auth", "auth_json")["sensitive"] = True
for fd in find("engine:registry-auth", "auth")["fields"]:
    if fd["name"] == "identity_token":
        fd["sensitive"] = True


# ---------------------------------------------------------------- 3. normalizers
def fold_results(iid):
    """Q35: exactly one `result` output per action, holding every result-category value."""
    e = oitems[iid]
    res = [o for o in e["outputs"] if o["category"] == "result"]
    if len(res) == 1 and res[0]["name"] == "result":
        return
    fields = []
    for r in res:
        if r["name"] == "result" and r.get("fields"):
            fields += r["fields"]
            continue
        fd = {"name": r["name"], "upstream": (r.get("source") or {}).get("pointer", "").rsplit("/", 1)[-1] or r["name"],
              "tf_type": r["tf_type"]}
        if r.get("fields"):
            fd["fields"] = r["fields"]
        if r.get("sensitive"):
            fd["sensitive"] = True
        fields.append(fd)
    if not fields:
        return
    new = {"name": "result", "tf_type": "object({...})", "category": "result", "source": res[0]["source"],
           "known_after_apply": True, "sensitive": False,
           "description": "The action's outcome as one object (Q35): " + ", ".join(f["name"] for f in fields) + ".",
           "fields": fields, "fields_complete": all(r.get("fields_complete", True) for r in res)}
    at = e["outputs"].index(res[0])
    e["outputs"] = [o for o in e["outputs"] if o["category"] != "result"]
    e["outputs"].insert(at, new)


def hashes(iid):
    """Q34: one raw output -> content_sha256; two or more -> <raw>_sha256 each."""
    e = oitems[iid]
    raws = [o for o in e["outputs"] if o["category"] == "raw"]
    hs = [o for o in e["outputs"] if o["category"] == "hash"]
    for r in raws:
        want = "content_sha256" if len(raws) == 1 else r["name"] + "_sha256"
        h = [o for o in hs if o.get("hash_of") == r["name"]]
        if h:
            h[0]["name"] = want
        else:
            e["outputs"].append({"name": want, "tf_type": "string", "category": "hash",
                                 "source": {"type": "derived", "from": r["name"], "how": "sha256 of the UTF-8 bytes"},
                                 "known_after_apply": r.get("known_after_apply", True), "sensitive": False,
                                 "hash_of": r["name"], "description": "sha256 of `%s` (Q34)." % r["name"]})


def add_input(iid, inp):
    e = oitems[iid]
    e.setdefault("inputs", [])
    if not any(i["name"] == inp["name"] for i in e["inputs"]):
        e["inputs"].append(inp)


RESULT_PATH = {"name": "result_path", "tf_type": "string", "default": None, "upstream": None,
               "source": {"type": "derived", "from": "result", "how": "Q35: after a successful run, the action's result object as JSON (UTF-8, sorted keys, trailing newline)"},
               "description": "Optional local file the action writes its result object to (Q35)."}


def target(name, op, param, what):
    return {"name": name, "tf_type": "string", "default": None, "upstream": param,
            "source": {"type": "swagger", "file": SW, "pointer": "#/paths/%s" % op},
            "description": "The %s the action targets (Q40)." % what}


NODE_T = target("node_id", "~1nodes~1{id}/post", "id", "swarm node (ID or hostname)")
SVC_T = target("service_id", "~1services~1{id}~1update/post", "id", "swarm service (ID or name)")
CTR_T = target("container_id", "~1containers~1{id}~1json/get", "id", "container (ID or name)")

for iid, e in oitems.items():
    if items[iid]["kind"] == "action":
        fold_results(iid)
        add_input(iid, RESULT_PATH)
        if iid.startswith("swarm:node-"):
            add_input(iid, NODE_T)
        elif iid.startswith("swarm:service-"):
            add_input(iid, SVC_T)
        elif iid.startswith("engine:container-") and iid not in ("engine:container-prune",) or iid == "engine:exec":
            add_input(iid, CTR_T)
    hashes(iid)

add_input("engine:container-logs", {"name": "container", "tf_type": "string", "default": None, "upstream": "id",
                                    "source": {"type": "swagger", "file": SW, "pointer": "#/paths/~1containers~1{id}~1logs/get"},
                                    "description": "The container whose logs are read, by ID or name (Q6)."})
add_input("engine:build-prune", {"name": "builder", "tf_type": "string", "default": None, "upstream": "--builder",
                                 "source": {"type": "cli", "command": "docker buildx prune", "file": "upstream/buildx/docs/reference/buildx_prune.md"},
                                 "description": "Unset: prune the Engine build cache (BuildPrune). Set: prune that buildx builder (Q2/Q23)."})
find("compose:name", "name")  # sanity

# ---------------------------------------------------------------- 4. new items (types registered today but not in the inventory, and Q4)
V_API = {"name": "engine_api_version", "tf_type": "string", "category": "version",
         "source": {"type": "swagger", "file": SW, "pointer": "#/definitions/SystemVersion/properties/ApiVersion"},
         "known_after_apply": True, "sensitive": False, "description": "Engine API version negotiated with the daemon."}
V_SRV = {"name": "docker_server_version", "tf_type": "string", "category": "version",
         "source": {"type": "swagger", "file": SW, "pointer": "#/definitions/SystemVersion/properties/Version"},
         "known_after_apply": True, "sensitive": False, "description": "Docker Engine version from GET /version."}
V_DIST = {"name": "distribution_api_version", "tf_type": "string", "category": "version",
          "source": {"type": "derived", "from": "registry response", "how": "the Docker-Distribution-API-Version response header (e.g. registry/2.0)"},
          "known_after_apply": True, "sensitive": False, "description": "The registry's Docker-Distribution-API-Version header."}


def sw(op, method, path):
    return {"type": "swagger", "operationId": op, "method": method, "path": path}


def ident(name, tf, ptr, desc, kaa=True):
    return {"name": name, "tf_type": tf, "category": "identity", "source": {"type": "swagger", "file": SW, "pointer": ptr},
            "known_after_apply": kaa, "sensitive": False, "description": desc}


NEW = []
NEWOUT = {}


def new(iid, layer, kind, name, summary, sources, **kw):
    it = {"id": iid, "layer": layer, "kind": kind, "name": name, "summary": summary, "sources": sources, "min_api": None,
          "cli_only": False}
    it.update(kw)
    NEW.append(it)
    return it


# Q4: docker_node (manage an already-joined node), outputs as data.docker_node.
new("swarm:node", "swarm", "resource", "docker_node",
    "Labels, availability and role of an already-joined swarm node (NodeUpdate), without joining or leaving (Q4).",
    [sw("NodeInspect", "GET", "/nodes/{id}"), sw("NodeUpdate", "POST", "/nodes/{id}/update"),
     {"type": "cli", "command": "docker node update", "file": "upstream/docker-cli/docs/reference/commandline/node_update.md"}],
    update="in_place", decisions=["Q4"])
NEWOUT["swarm:node"] = copy.deepcopy(oitems["swarm:node-data"])
NEWOUT["swarm:node"]["outputs_status"] = "missing"
NEWOUT["swarm:node"]["outputs_note"] = "Pass 5 (Q4): same outputs as data.docker_node."
for o in NEWOUT["swarm:node"]["outputs"]:
    if o["category"] not in ("version",):
        o["known_after_apply"] = True

# action docker_system_prune (registered since Pass 3).
new("engine:system-prune", "engine", "action", "docker_system_prune",
    "Prune stopped containers, unused networks, dangling (or all unused) images, build cache and optionally volumes, like `docker system prune`.",
    [sw("ContainerPrune", "POST", "/containers/prune"), sw("NetworkPrune", "POST", "/networks/prune"),
     sw("ImagePrune", "POST", "/images/prune"), sw("BuildPrune", "POST", "/build/prune"), sw("VolumePrune", "POST", "/volumes/prune"),
     {"type": "cli", "command": "docker system prune", "file": "upstream/docker-cli/docs/reference/commandline/system_prune.md"}])
fields = []
for ref in ("engine:container-prune", "engine:network-prune", "engine:image-prune", "engine:build-prune", "engine:volume-prune"):
    fields += [copy.deepcopy(fd) for fd in find(ref, "result")["fields"] if fd["name"] != "space_reclaimed" and fd["name"] not in [x["name"] for x in fields]]
fields.append({"name": "space_reclaimed", "upstream": "SpaceReclaimed", "tf_type": "number",
               "pointer": "#/paths/~1containers~1prune/post/responses/200/schema/properties/SpaceReclaimed"})
NEWOUT["engine:system-prune"] = {"outputs": [
    ident("containers_deleted", "list(string)", "#/paths/~1containers~1prune/post/responses/200/schema/properties/ContainersDeleted", "IDs of the deleted containers."),
    {"name": "result", "tf_type": "object({...})", "category": "result",
     "source": {"type": "derived", "from": "ContainerPrune, NetworkPrune, ImagePrune, BuildPrune, VolumePrune", "how": "the five prune responses merged; space_reclaimed is their sum"},
     "known_after_apply": True, "sensitive": False, "description": "Every prune response merged into one object (Q35).",
     "fields": fields, "fields_complete": True},
    V_API, V_SRV], "uses_cli": False, "outputs_status": "partial", "outputs_note": "Reports space reclaimed in progress today.",
    "inputs": [RESULT_PATH]}

# data docker_registry_image_tags (Pass 3).
new("engine:registry-image-tags", "engine", "data_source", "docker_registry_image_tags",
    "Every tag of a repository, read straight from its registry (GET /v2/<name>/tags/list, paginated).",
    [{"type": "cli", "command": "docker buildx imagetools inspect", "file": "upstream/buildx/docs/reference/buildx_imagetools_inspect.md"}],
    cli_only=True)
DER = lambda h: {"type": "derived", "from": "GET /v2/<name>/tags/list", "how": h}
NEWOUT["engine:registry-image-tags"] = {"outputs": [
    {"name": "name", "tf_type": "string", "category": "identity", "source": DER("the repository name, normalized"), "known_after_apply": False, "sensitive": False, "description": "The repository."},
    {"name": "tags", "tf_type": "list(string)", "category": "identity", "source": DER("the tags array, every page"), "known_after_apply": False, "sensitive": False, "description": "Every tag, in registry order."},
    {"name": "tags_json", "tf_type": "string", "category": "raw", "source": DER("the merged pages as {name, tags} JSON"), "known_after_apply": False, "sensitive": False, "serializes": "tag_list", "description": "The merged tags/list response."},
    {"name": "tag_list", "tf_type": "object({...})", "category": "structured", "source": DER("OCI distribution spec content discovery response"), "known_after_apply": False, "sensitive": False,
     "fields": [{"name": "name", "upstream": "name", "tf_type": "string"}, {"name": "tags", "upstream": "tags", "tf_type": "list(string)"}], "fields_complete": True},
    {"name": "content_sha256", "tf_type": "string", "category": "hash", "source": {"type": "derived", "from": "tags_json", "how": "sha256 of the UTF-8 bytes"}, "known_after_apply": False, "sensitive": False, "hash_of": "tags_json"},
    V_DIST], "uses_cli": False}

# data + ephemeral docker_swarm_tokens (Pass 3).
JT = {"name": "join_tokens", "tf_type": "object({...})", "category": "structured",
      "source": {"type": "swagger", "file": SW, "pointer": "#/definitions/JoinTokens"}, "known_after_apply": False, "sensitive": True,
      "sensitive_reason": "Both fields are swarm join tokens: whoever holds one can join a node to the swarm.",
      "fields": [{"name": "worker", "upstream": "Worker", "tf_type": "string", "pointer": "#/definitions/JoinTokens/properties/Worker"},
                 {"name": "manager", "upstream": "Manager", "tf_type": "string", "pointer": "#/definitions/JoinTokens/properties/Manager"}],
      "fields_complete": True}
SID = ident("id", "string", "#/definitions/ClusterInfo/properties/ID", "The swarm's cluster ID.", False)
new("swarm:swarm-tokens-data", "swarm", "data_source", "docker_swarm_tokens",
    "The swarm's worker and manager join tokens (SwarmInspect JoinTokens), stored in state as sensitive values.",
    [sw("SwarmInspect", "GET", "/swarm"), {"type": "cli", "command": "docker swarm join-token", "file": "upstream/docker-cli/docs/reference/commandline/swarm_join-token.md"}])
NEWOUT["swarm:swarm-tokens-data"] = {"outputs": [SID, JT, V_API, V_SRV], "uses_cli": False,
                                     "no_raw_reason": "Only the join tokens; the whole SwarmInspect response is data.docker_swarm's inspect_json."}
new("swarm:swarm-tokens-ephemeral", "swarm", "ephemeral_resource", "docker_swarm_tokens",
    "The swarm's join tokens as ephemeral values, never written to plan or state.",
    [sw("SwarmInspect", "GET", "/swarm")])
NEWOUT["swarm:swarm-tokens-ephemeral"] = {"outputs": [copy.deepcopy(SID), copy.deepcopy(JT), V_API, V_SRV], "uses_cli": False}

# ephemeral docker_registry_token (Pass 3): talks only to the registry's token service.
new("engine:registry-token", "engine", "ephemeral_resource", "docker_registry_token",
    "A short-lived registry bearer token from the registry's token service, never written to plan or state.",
    [{"type": "cli", "command": "docker login", "file": "upstream/docker-cli/docs/reference/commandline/login.md"}], cli_only=True)
TD = lambda h: {"type": "derived", "from": "registry token service response", "how": h}
NEWOUT["engine:registry-token"] = {"outputs": [
    {"name": "registry", "tf_type": "string", "category": "identity", "source": TD("the registry host the token is for"), "known_after_apply": False, "sensitive": False},
    {"name": "token", "tf_type": "object({...})", "category": "structured", "source": TD("the Docker token authentication response"), "known_after_apply": True, "sensitive": False,
     "fields": [{"name": "token", "upstream": "token", "tf_type": "string", "sensitive": True},
                {"name": "expires_in", "upstream": "expires_in", "tf_type": "number"},
                {"name": "issued_at", "upstream": "issued_at", "tf_type": "string"}], "fields_complete": True},
    V_DIST], "uses_cli": False}

# ephemeral docker_exec_output (Pass 3).
new("engine:exec-output", "engine", "ephemeral_resource", "docker_exec_output",
    "Runs a command in a running container and returns its exit code, stdout and stderr as ephemeral values.",
    [sw("ContainerExec", "POST", "/containers/{id}/exec"), sw("ExecStart", "POST", "/exec/{id}/start"), sw("ExecInspect", "GET", "/exec/{id}/json")])
eo = copy.deepcopy(oitems["engine:exec"])
eo["inputs"] = [i for i in eo.get("inputs", []) if i["name"] != "result_path"]
eo.pop("outputs_status", None), eo.pop("outputs_note", None)
NEWOUT["engine:exec-output"] = eo

# list resources and provider functions: registered types without an Engine object of their own.
for iid, layer, nm, res in [("engine:list-container", "engine", "docker_container", "docker_container"),
                            ("engine:list-image", "engine", "docker_image", "docker_image"),
                            ("engine:list-network", "engine", "docker_network", "docker_network"),
                            ("engine:list-volume", "engine", "docker_volume", "docker_volume"),
                            ("swarm:list-swarm-node", "swarm", "docker_swarm_node", "docker_swarm_node"),
                            ("swarm:list-service", "swarm", "docker_service", "docker_service"),
                            ("swarm:list-secret", "swarm", "docker_secret", "docker_secret"),
                            ("swarm:list-config", "swarm", "docker_config", "docker_config")]:
    new(iid, layer, "list_resource", nm, "Lists existing objects as `%s` resources for `terraform query` and bulk import." % res, [])
for iid, layer, nm, s in [("engine:function-parse-image-ref", "engine", "parse_image_ref", "Splits an image reference into registry, repository, tag and digest."),
                          ("engine:function-image-ref", "engine", "image_ref", "Builds a fully qualified image reference from its parts."),
                          ("engine:function-port-spec", "engine", "port_spec", "Parses a `docker run -p` port spec into host_ip, host_port, container_port and protocol."),
                          ("compose:function-compose-env", "compose", "compose_env", "Turns a map of environment variables into sorted KEY=VALUE strings.")]:
    new(iid, layer, "function", nm, s, [])

for f in FRAG_FILES:
    save(os.path.join(Z, f), *frags[f])
for f in OUT_FILES:
    save(os.path.join(O, f), *outs[f])
save(os.path.join(Z, "pass5.json"), {
    "lane": "P5",
    "notes": ["Pass 5 Lane 0: types registered before Pass 5 that the harvest did not list (ephemeral resources, list resources, "
              "functions, data.docker_registry_image_tags, data.docker_swarm_tokens, action.docker_system_prune) and the new "
              "docker_node resource (Q4). list_resource and function items have no outputs: the contract (V1/V2) covers the "
              "types that read or change Docker objects."],
    "naming_questions": [],
    "items": NEW}, 2)
save(os.path.join(O, "P5.json"), {"lane": "P5", "notes": ["Pass 5 Lane 0: outputs of the items in fragments/pass5.json."],
                                  "naming_questions": [], "items": NEWOUT}, 2)
print("skipped 6, renamed", len(RENAME) + 1, "new items", len(NEW))
