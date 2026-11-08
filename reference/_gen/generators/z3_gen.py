"""Lane Z3 generator: docker CLI coverage map + cli-only/buildx/plugin items.
Reads the repo read-only; writes ../fragments/z3-cli-map.json (run z3_items.py after it for z3-cli.json)."""
import json, os, re, sys

HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.normpath(os.path.join(HERE, "..", ".."))  # reference/
FRAG = os.path.join(HERE, "..", "fragments")
UP = os.path.join(REF, "upstream")
OUT = FRAG
CLI_DOCS = os.path.join(UP, "docker-cli", "docs", "reference", "commandline")
TRUST_DOCS = os.path.join(UP, "docker-cli", "cmd", "docker-trust", "docs", "reference")
BX_DOCS = os.path.join(UP, "buildx", "docs", "reference")
HELP = os.path.join(UP, "help")

CLI_REL = "upstream/docker-cli/docs/reference/commandline/"
TRUST_REL = "upstream/docker-cli/cmd/docker-trust/docs/reference/"
BX_REL = "upstream/buildx/docs/reference/"
HELP_REL = "upstream/help/"

PLUGINS = ["agent", "ai", "debug", "desktop", "dhi", "extension", "init", "mcp",
           "model", "offload", "pass", "sandbox", "scout"]
OTHER_LANE_PLUGINS = ["compose", "buildx"]


def cmd_of(fname, prefix="docker"):
    base = fname[:-3] if fname.endswith(".md") else fname[:-4]
    return (prefix + " " + base.replace("_", " ")).strip()


# ---------------------------------------------------------------- engine map
# CLI command (without "docker ") -> list of "METHOD /path" from swagger.yaml (API 1.56)
C = "/containers/{id}"
E = {
    "attach": ["POST %s/attach" % C, "GET %s/attach/ws" % C],
    "build": ["POST /build", "POST /session"],
    "builder build": ["POST /build", "POST /session"],
    "builder prune": ["POST /build/prune"],
    "commit": ["POST /commit"],
    "config create": ["POST /configs/create"], "config inspect": ["GET /configs/{id}"],
    "config ls": ["GET /configs"], "config rm": ["DELETE /configs/{id}"],
    "container attach": ["POST %s/attach" % C, "GET %s/attach/ws" % C],
    "container commit": ["POST /commit"],
    "container cp": ["HEAD %s/archive" % C, "GET %s/archive" % C, "PUT %s/archive" % C],
    "container create": ["POST /containers/create", "POST /images/create"],
    "container diff": ["GET %s/changes" % C],
    "container exec": ["POST %s/exec" % C, "POST /exec/{id}/start", "POST /exec/{id}/resize", "GET /exec/{id}/json"],
    "container export": ["GET %s/export" % C],
    "container inspect": ["GET %s/json" % C],
    "container kill": ["POST %s/kill" % C],
    "container logs": ["GET %s/logs" % C],
    "container ls": ["GET /containers/json"],
    "container pause": ["POST %s/pause" % C],
    "container port": ["GET %s/json" % C],
    "container prune": ["POST /containers/prune"],
    "container rename": ["POST %s/rename" % C],
    "container restart": ["POST %s/restart" % C],
    "container rm": ["DELETE %s" % C],
    "container run": ["POST /containers/create", "POST %s/start" % C, "POST %s/attach" % C, "POST %s/wait" % C, "POST %s/resize" % C],
    "container start": ["POST %s/start" % C],
    "container stats": ["GET %s/stats" % C],
    "container stop": ["POST %s/stop" % C],
    "container top": ["GET %s/top" % C],
    "container unpause": ["POST %s/unpause" % C],
    "container update": ["POST %s/update" % C],
    "container wait": ["POST %s/wait" % C],
    "events": ["GET /events"],
    "image build": ["POST /build", "POST /session"],
    "image history": ["GET /images/{name}/history"],
    "image import": ["POST /images/create"],
    "image inspect": ["GET /images/{name}/json"],
    "image load": ["POST /images/load"],
    "image ls": ["GET /images/json"],
    "image prune": ["POST /images/prune"],
    "image pull": ["POST /images/create"],
    "image push": ["POST /images/{name}/push"],
    "image rm": ["DELETE /images/{name}"],
    "image save": ["GET /images/{name}/get", "GET /images/get"],
    "image tag": ["POST /images/{name}/tag"],
    "info": ["GET /info"],
    "inspect": ["GET %s/json" % C, "GET /images/{name}/json", "GET /networks/{id}", "GET /volumes/{name}",
                "GET /nodes/{id}", "GET /services/{id}", "GET /tasks/{id}", "GET /secrets/{id}", "GET /configs/{id}", "GET /plugins/{name}/json"],
    "network connect": ["POST /networks/{id}/connect"],
    "network create": ["POST /networks/create"],
    "network disconnect": ["POST /networks/{id}/disconnect"],
    "network inspect": ["GET /networks/{id}"],
    "network ls": ["GET /networks"],
    "network prune": ["POST /networks/prune"],
    "network rm": ["DELETE /networks/{id}"],
    "node demote": ["POST /nodes/{id}/update"], "node promote": ["POST /nodes/{id}/update"],
    "node inspect": ["GET /nodes/{id}"], "node ls": ["GET /nodes"],
    "node ps": ["GET /tasks"], "node rm": ["DELETE /nodes/{id}"], "node update": ["POST /nodes/{id}/update"],
    "plugin create": ["POST /plugins/create"], "plugin disable": ["POST /plugins/{name}/disable"],
    "plugin enable": ["POST /plugins/{name}/enable"], "plugin inspect": ["GET /plugins/{name}/json"],
    "plugin install": ["GET /plugins/privileges", "POST /plugins/pull"], "plugin ls": ["GET /plugins"],
    "plugin push": ["POST /plugins/{name}/push"], "plugin rm": ["DELETE /plugins/{name}"],
    "plugin set": ["POST /plugins/{name}/set"], "plugin upgrade": ["GET /plugins/privileges", "POST /plugins/{name}/upgrade"],
    "search": ["GET /images/search"],
    "secret create": ["POST /secrets/create"], "secret inspect": ["GET /secrets/{id}"],
    "secret ls": ["GET /secrets"], "secret rm": ["DELETE /secrets/{id}"],
    "service create": ["POST /services/create"], "service inspect": ["GET /services/{id}"],
    "service logs": ["GET /services/{id}/logs", "GET /tasks/{id}/logs"], "service ls": ["GET /services"],
    "service ps": ["GET /tasks"], "service rm": ["DELETE /services/{id}"],
    "service rollback": ["POST /services/{id}/update"], "service scale": ["POST /services/{id}/update"],
    "service update": ["POST /services/{id}/update"],
    "swarm ca": ["GET /swarm", "POST /swarm/update"],
    "swarm init": ["POST /swarm/init"], "swarm join": ["POST /swarm/join"],
    "swarm join-token": ["GET /swarm", "POST /swarm/update"], "swarm leave": ["POST /swarm/leave"],
    "swarm unlock": ["POST /swarm/unlock"], "swarm unlock-key": ["GET /swarm/unlockkey", "POST /swarm/update"],
    "swarm update": ["POST /swarm/update"],
    "system df": ["GET /system/df"], "system events": ["GET /events"], "system info": ["GET /info"],
    "system prune": ["POST /containers/prune", "POST /networks/prune", "POST /images/prune", "POST /build/prune", "POST /volumes/prune"],
    "version": ["GET /version"],
    "volume create": ["POST /volumes/create"], "volume inspect": ["GET /volumes/{name}"],
    "volume ls": ["GET /volumes"], "volume prune": ["POST /volumes/prune"], "volume rm": ["DELETE /volumes/{name}"],
    "volume update": ["PUT /volumes/{name}"],
    "login": ["POST /auth"],
}
# top-level aliases -> canonical
ALIAS = {"cp": "container cp", "create": "container create", "diff": "container diff", "exec": "container exec",
         "export": "container export", "history": "image history", "images": "image ls", "import": "image import",
         "kill": "container kill", "load": "image load", "logs": "container logs", "pause": "container pause",
         "port": "container port", "ps": "container ls", "pull": "image pull", "push": "image push",
         "rename": "container rename", "restart": "container restart", "rm": "container rm", "rmi": "image rm",
         "run": "container run", "save": "image save", "start": "container start", "stats": "container stats",
         "stop": "container stop", "tag": "image tag", "top": "container top", "unpause": "container unpause",
         "update": "container update", "wait": "container wait"}
GROUPS = {"builder", "checkpoint", "config", "container", "context", "image", "manifest", "network", "node",
          "plugin", "secret", "service", "stack", "swarm", "system", "volume", "trust", "trust key", "trust signer"}

# cli-only commands -> item id
CLI_ONLY = {
    "bake": "buildx:bake",
    "checkpoint create": "cli-only:checkpoint", "checkpoint ls": "cli-only:checkpoint", "checkpoint rm": "cli-only:checkpoint",
    "context create": "cli-only:context", "context export": "cli-only:context", "context import": "cli-only:context",
    "context inspect": "cli-only:context", "context ls": "cli-only:context", "context rm": "cli-only:context",
    "context show": "cli-only:context", "context update": "cli-only:context", "context use": "cli-only:context",
    "login": "cli-only:login", "logout": "cli-only:logout",
    "manifest annotate": "cli-only:manifest-list", "manifest create": "cli-only:manifest-list",
    "manifest push": "cli-only:manifest-list", "manifest rm": "cli-only:manifest-list",
    "manifest inspect": "cli-only:manifest-inspect",
    "stack deploy": "cli-only:stack", "stack rm": "cli-only:stack", "stack ls": "cli-only:stacks",
    "stack services": "cli-only:stack-services", "stack ps": "cli-only:stack-ps", "stack config": "cli-only:stack-config",
    "system dial-stdio": "cli-only:system-dial-stdio",
    "trust inspect": "cli-only:trust", "trust key generate": "cli-only:trust", "trust key load": "cli-only:trust",
    "trust revoke": "cli-only:trust", "trust sign": "cli-only:trust", "trust signer add": "cli-only:trust",
    "trust signer remove": "cli-only:trust",
}

NOTE_BY_CMD = {
    "build": "In CLI v29 `docker build` is an alias of `docker buildx build` (help resolves to buildx); the classic path is POST /build, BuildKit uses POST /session + the undocumented /grpc upgrade.",
    "image build": "Alias of `docker buildx build` in CLI v29 (see build).",
    "builder build": "Alias of `docker buildx build` in CLI v29; help for `docker builder` resolved to `docker buildx`, so children were not walked.",
    "builder prune": "Not in the help capture: `docker builder` resolved to buildx (help shows `docker buildx prune`).",
    "login": "POST /auth only validates credentials; storing them is client-side (config.json / credential helper). Item cli-only:login documents the client part.",
    "version": "Server part is GET /version; the client part is local build info (nothing to model).",
    "swarm init": "Help capture oddity: `docker swarm init --help` resolved to the docker-init plugin.",
    "checkpoint create": "Daemon has experimental, undocumented /containers/{id}/checkpoints endpoints not in swagger.yaml.",
    "checkpoint ls": "Daemon has experimental, undocumented /containers/{id}/checkpoints endpoints not in swagger.yaml.",
    "checkpoint rm": "Daemon has experimental, undocumented /containers/{id}/checkpoints/{checkpoint} endpoint not in swagger.yaml.",
    "system dial-stdio": "Proxies stdio to the daemon socket; no endpoint of its own.",
    "manifest inspect": "Talks to the registry directly (GET /distribution/{name}/json gives only the descriptor + platforms).",
}

# ---------------------------------------------------------------- help tree
help_files = sorted(f for f in os.listdir(HELP) if f.startswith("docker") and f.endswith(".txt"))
help_cmds = {}
for f in help_files:
    c = cmd_of(f, "").replace("docker", "docker", 1).strip()
    help_cmds[c] = f
def is_core(c):
    parts = c.split()
    return len(parts) == 1 or parts[1] not in PLUGINS + OTHER_LANE_PLUGINS
core_help = {c: f for c, f in help_cmds.items() if is_core(c)}

# ---------------------------------------------------------------- doc files
doc_entries = []
for f in sorted(os.listdir(CLI_DOCS)):
    if f.endswith(".md"):
        doc_entries.append((f, CLI_REL + f))
for f in sorted(os.listdir(TRUST_DOCS)):
    if f.endswith(".md"):
        doc_entries.append((f, TRUST_REL + f))


def classify(sub):
    """sub = command without 'docker ' prefix. Returns (engine_api, handled_by)."""
    canon = ALIAS.get(sub, sub)
    if sub in CLI_ONLY:
        return E.get(canon, []), CLI_ONLY[sub]
    if canon in E:
        return E[canon], "engine"
    if sub in GROUPS or sub == "":
        return [], "group"
    return [], "UNMAPPED"


commands = []
seen = set()
for f, rel in doc_entries:
    if f == "index.md":
        commands.append({"cli": None, "file": rel, "in_help": None, "engine_api": [], "handled_by": "not-a-command",
                         "note": "Reference index page, not a command."})
        continue
    cli = "docker" if f == "docker.md" else cmd_of(f)
    sub = cli[len("docker "):] if cli != "docker" else ""
    api, hb = classify(sub)
    e = {"cli": cli, "file": rel, "in_help": cli in core_help, "engine_api": api, "handled_by": hb}
    if sub in ALIAS:
        e["alias_of"] = "docker " + ALIAS[sub]
    if sub in NOTE_BY_CMD:
        e["note"] = NOTE_BY_CMD[sub]
    if cli in core_help:
        e["help_file"] = HELP_REL + core_help[cli]
    commands.append(e)
    seen.add(cli)

help_missing_docs = []
for c, f in sorted(core_help.items()):
    if c not in seen:
        sub = c[len("docker "):]
        api, hb = classify(sub)
        commands.append({"cli": c, "file": None, "help_file": HELP_REL + f, "in_help": True, "engine_api": api, "handled_by": hb})
        help_missing_docs.append(c)

docs_missing_help = [c["cli"] for c in commands if c.get("file") and c["cli"] and c["in_help"] is False]

# buildx commands (scope B) and plugins (scope C) in the map too, for coverage
bx_help = {c: f for c, f in help_cmds.items() if c.startswith("docker buildx")}
BX_ID = {
    "docker buildx": "group", "docker buildx bake": "buildx:bake", "docker buildx build": "buildx:build",
    "docker buildx create": "buildx:builder", "docker buildx rm": "buildx:builder",
    "docker buildx inspect": "buildx:builder-inspect", "docker buildx ls": "buildx:builders",
    "docker buildx use": "buildx:use", "docker buildx stop": "buildx:stop", "docker buildx prune": "buildx:prune",
    "docker buildx du": "buildx:du", "docker buildx dial-stdio": "buildx:dial-stdio",
    "docker buildx dap": "group", "docker buildx dap build": "buildx:dap-build",
    "docker buildx debug": "group", "docker buildx debug build": "buildx:debug-build",
    "docker buildx history": "group", "docker buildx history export": "buildx:history-export",
    "docker buildx history import": "buildx:history-import", "docker buildx history inspect": "buildx:history-inspect",
    "docker buildx history inspect attachment": "buildx:history-inspect-attachment",
    "docker buildx history logs": "buildx:history-logs", "docker buildx history ls": "buildx:history-ls",
    "docker buildx history open": "buildx:history-open", "docker buildx history rm": "buildx:history-rm",
    "docker buildx history trace": "buildx:history-trace",
    "docker buildx imagetools": "group", "docker buildx imagetools create": "buildx:imagetools-create",
    "docker buildx imagetools inspect": "buildx:imagetools-inspect",
    "docker buildx policy": "group", "docker buildx policy eval": "buildx:policy-eval",
    "docker buildx policy json-schema": "buildx:policy-json-schema", "docker buildx policy test": "buildx:policy-test",
    "docker buildx replay": "group", "docker buildx replay build": "buildx:replay-build",
    "docker buildx version": "buildx:version",
}
BX_API = {"docker buildx build": ["POST /session"], "docker buildx prune": ["POST /build/prune"],
          "docker buildx du": ["GET /system/df"], "docker buildx create": [], "docker buildx stop": []}
bx_docs = sorted(f for f in os.listdir(BX_DOCS) if f.endswith(".md"))
bx_cmds = []
bx_seen = set()
for f in bx_docs:
    c = cmd_of(f)
    e = {"cli": c, "file": BX_REL + f, "in_help": c in bx_help, "engine_api": BX_API.get(c, []),
         "handled_by": BX_ID.get(c, "UNMAPPED"), "group": "buildx"}
    if c in BX_API:
        e["note"] = "BuildKit gRPC is the real channel; with the `docker` driver the daemon serves it via POST /session + /grpc, and the listed endpoint is the docker-driver fallback."
    if c in bx_help:
        e["help_file"] = HELP_REL + bx_help[c]
    bx_cmds.append(e)
    bx_seen.add(c)
for c, f in sorted(bx_help.items()):
    if c not in bx_seen:
        bx_cmds.append({"cli": c, "file": None, "help_file": HELP_REL + f, "in_help": True, "engine_api": [],
                        "handled_by": BX_ID.get(c, "UNMAPPED"), "group": "buildx"})
bx_newer = [e["cli"] for e in bx_cmds if e["file"] and not e["in_help"]]
bx_help_only = [e["cli"] for e in bx_cmds if not e["file"]]

plugin_cmds = []
for p in PLUGINS:
    c = "docker " + p
    plugin_cmds.append({"cli": c, "file": None, "help_file": HELP_REL + help_cmds[c] if c in help_cmds else None,
                        "in_help": c in help_cmds, "engine_api": [], "handled_by": "plugin:" + p, "group": "plugin",
                        "subcommand_help_files": sum(1 for k in help_cmds if k.startswith(c + " "))})

core = [c for c in commands if c["cli"] and c["handled_by"] != "not-a-command"]
summary = {
    "core_commands_total": len(core),
    "core_doc_files": sum(1 for _ in doc_entries),
    "core_help_commands": len(core_help),
    "engine": sum(1 for c in core if c["handled_by"] == "engine"),
    "cli_only_items": sum(1 for c in core if c["handled_by"].startswith("cli-only:") or c["handled_by"].startswith("buildx:")),
    "groups": sum(1 for c in core if c["handled_by"] == "group"),
    "unmapped": [c["cli"] for c in core + bx_cmds if c["handled_by"] == "UNMAPPED"],
    "docs_missing_from_help": docs_missing_help,
    "help_missing_from_docs": help_missing_docs,
    "buildx_doc_files": len(bx_docs), "buildx_help_files": len(bx_help),
    "buildx_docs_newer_than_installed_v0.34.1": bx_newer,
    "buildx_help_only": bx_help_only,
}
mapdoc = {
    "lane": "Z3",
    "notes": [
        "handled_by: engine = has a swagger.yaml (API 1.56) endpoint, item made by lane Z1; group = management-command doc page with no operation of its own; cli-only:*/buildx:*/plugin:* = item id in z3-cli.json.",
        "Help capture is from Docker Desktop 4.78 (CLI 29.5.3) with the daemon NOT a swarm manager, so node/service/secret/config/stack and most swarm subcommands are hidden from --help: their absence is environmental, not a docs/CLI mismatch.",
        "checkpoint is experimental (hidden without experimental CLI); trust lives in the separate docker-trust plugin (cmd/docker-trust), not installed; system dial-stdio is hidden.",
        "`docker builder` help resolved to `docker buildx`, so `builder build` / `builder prune` children were not walked.",
        "`docker swarm init --help` resolved to the docker-init plugin (help-capture oddity).",
        "login has POST /auth but it only validates credentials; mapped to item cli-only:login (engine_api kept).",
    ],
    "summary": summary,
    "commands": commands,
    "buildx_commands": bx_cmds,
    "plugin_commands": plugin_cmds,
}
with open(os.path.join(OUT, "z3-cli-map.json"), "w", encoding="utf-8", newline="\n") as fh:
    json.dump(mapdoc, fh, indent=1, ensure_ascii=False)
    fh.write("\n")
print(json.dumps(summary, indent=1))
