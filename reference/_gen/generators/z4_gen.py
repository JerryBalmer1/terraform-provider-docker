import json, re, os, sys
HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.normpath(os.path.join(HERE, "..", ".."))  # reference/
FRAG = os.path.join(HERE, "..", "fragments")
DOCS = os.path.join(REF, "upstream", "dockerfile", "frontend", "dockerfile", "docs")
OUT = os.path.join(FRAG, "z4-dockerfile.json")
REF = "upstream/dockerfile/frontend/dockerfile/docs/reference.md"
BT = chr(96)  # backtick


def slug(h):
    s = h.strip().lower()
    s = re.sub(r"[^\w\- ]", "", s)
    return s.replace(" ", "-")


anchors = set()
incode = False
for line in open(os.path.join(DOCS, "reference.md"), encoding="utf-8"):
    if line.startswith(BT * 3):
        incode = not incode
        continue
    if not incode:
        m = re.match(r"^(#{1,6})\s+(.*)$", line)
        if m:
            anchors.add("#" + slug(m.group(2)))
    for a in re.findall(r'<a name="([^"]+)"', line):
        anchors.add("#" + a)


def F(name, anchor, options=None, min_syntax=None, note=None):
    f = {"name": name, "options": options or [], "anchor": anchor}
    if min_syntax:
        f["min_syntax"] = "dockerfile:" + min_syntax
    if note:
        f["note"] = note
    return f


SHELL_FORM = F("shell form", "#shell-form", ["<command> ..."])
EXEC_FORM = F("exec form", "#exec-form", ['["<executable>", "<param>", ...]'])
HEREDOC = F("heredoc", "#here-documents", ["<<EOF", "<<-EOF (strip leading tabs)", '<<"EOF" (no expansion)', "multiple heredocs per instruction"])
BIND = ["target|dst|destination (required)", "source", "from", "rw|readwrite"]

chmod = F("--chmod", "#copy---chmod", ["<octal perms>", "symbolic notation (+x, u=rwX,go=rX, g=u) (Dockerfile 1.14)", "$VAR"], "1.2")
chown = F("--chown", "#copy---chown", ["<user>", "<user>:<group>", "<uid>", "<uid>:<gid>", "<user>:"])
link = F("--link", "#copy---link", ["true", "false"], "1.4")
excl = F("--exclude", "#copy---exclude", ["<path pattern> (repeatable)"], "1.19")

blocks = [
    ("FROM", "#from", "Start a build stage from a base image (or scratch / a previous stage).", [
        F("--platform", "#from", ["<os>/<arch>[/<variant>]", "$BUILDPLATFORM", "$TARGETPLATFORM"]),
        F("image", "#from", ["<image>", "<image>:<tag>", "<image>@<digest>", "scratch", "<stage name>"]),
        F("AS name", "#from", ["<stage name>"]),
        F("global ARG substitution", "#understand-how-arg-and-from-interact", ["ARG before first FROM"]),
    ]),
    ("RUN", "#run", "Execute a command at build time and commit the result as a new layer.", [
        SHELL_FORM, EXEC_FORM, HEREDOC,
        F("--device", "#run---device", ["<cdi name>", "vendor/device", "vendor/device=name", "vendor/device=*", "<class>", "required"], "1.27", "Needs BuildKit 0.20.0+ and the device entitlement."),
        F("--mount", "#run---mount", ["type=bind|cache|tmpfs|secret|ssh (default bind)"], "1.2"),
        F("--mount=type=bind", "#run---mounttypebind", BIND, "1.2"),
        F("--mount=type=cache", "#run---mounttypecache", ["id", "target|dst|destination (required)", "ro|readonly", "sharing=shared|private|locked", "from", "source", "mode (default 0755)", "uid (default 0)", "gid (default 0)"], "1.2"),
        F("--mount=type=tmpfs", "#run---mounttypetmpfs", ["target|dst|destination (required)", "size"], "1.2"),
        F("--mount=type=secret", "#run---mounttypesecret", ["id", "target|dst|destination", "env (Dockerfile 1.10)", "required (default false)", "mode (default 0400)", "uid (default 0)", "gid (default 0)"], "1.2"),
        F("--mount=type=ssh", "#run---mounttypessh", ['id (default "default")', "target|dst|destination", "required (default false)", "mode (default 0600)", "uid (default 0)", "gid (default 0)"], "1.2"),
        F("--network", "#run---network", ["default", "none", "host"], "1.3"),
        F("--network=default", "#run---networkdefault", [], "1.3"),
        F("--network=none", "#run---networknone", [], "1.3"),
        F("--network=host", "#run---networkhost", [], "1.3", "Needs the network.host entitlement."),
        F("--security", "#run---security", ["sandbox (default)", "insecure"], "1.20", "insecure needs the security.insecure entitlement."),
    ]),
    ("CMD", "#cmd", "Set the default command (or default ENTRYPOINT arguments) for containers from the image.", [
        EXEC_FORM, F("exec form (params to ENTRYPOINT)", "#cmd", ['["<param>", ...]']), SHELL_FORM,
    ]),
    ("LABEL", "#label", "Add key=value metadata labels to the image.", [
        F("<key>=<value>", "#label", ["repeatable", "quoted values"]),
    ]),
    ("MAINTAINER", "#maintainer-deprecated", "Deprecated: set the image author field; use LABEL org.opencontainers.image.authors instead.", [
        F("<name>", "#maintainer-deprecated", ["<name>"]),
    ]),
    ("EXPOSE", "#expose", "Document the ports the container listens on.", [
        F("<port>[/<protocol>]", "#expose", ["<port>", "<port>/tcp (default)", "<port>/udp", "repeatable"]),
    ]),
    ("ENV", "#env", "Set environment variables for later instructions and the running container.", [
        F("<key>=<value>", "#env", ["repeatable", "quoted values", "legacy ENV <key> <value> form"]),
    ]),
    ("ADD", "#add", "Add files from the build context, a URL, or a Git repository (with optional unpacking) to the image.", [
        F("sources/destination", "#add", ["<src> ... <dest>", '["<src>", ... "<dest>"] (paths with whitespace)', "context path / pattern", "local tar archive", "URL", "Git repository URL"]),
        F("--keep-git-dir", "#add---keep-git-dir", ["true", "false"], "1.1"),
        F("--checksum", "#add---checksum", ["<git commit sha or prefix>", "sha256:<hash>"], "1.6"),
        dict(chmod, anchor="#add---chmod"), dict(chown, anchor="#add---chown"), dict(link, anchor="#add---link"),
        F("--unpack", "#add---unpack", ["true", "false"], "1.17"),
        dict(excl, anchor="#add---exclude"),
    ]),
    ("COPY", "#copy", "Copy files from the build context, a build stage, a named context, or an image into the image.", [
        F("sources/destination", "#copy", ["<src> ... <dest>", '["<src>", ... "<dest>"] (paths with whitespace)']),
        HEREDOC,
        F("--from", "#copy---from", ["<stage name>", "<image>", "<named context>"]),
        chmod, chown, link,
        F("--parents", "#copy---parents", ["true", "false", "./ pivot in source path"], "1.20"),
        excl,
    ]),
    ("ENTRYPOINT", "#entrypoint", "Configure the executable the container runs.", [EXEC_FORM, SHELL_FORM]),
    ("VOLUME", "#volume", "Declare mount points for externally mounted volumes.", [
        F("paths", "#volume", ['["<path>", ...] (JSON array)', "<path> <path> ... (plain string)"]),
    ]),
    ("USER", "#user", "Set the user (and optionally group) for later instructions and the running container.", [
        F("user", "#user", ["<user>[:<group>]", "<UID>[:<GID>]"]),
    ]),
    ("WORKDIR", "#workdir", "Set the working directory for later instructions and the running container.", [
        F("path", "#workdir", ["<absolute path>", "<relative path> (relative to previous WORKDIR)", "$VAR"]),
    ]),
    ("ARG", "#arg", "Declare build-time variables (with optional defaults).", [
        F("<name>[=<default>]", "#arg", ["repeatable"]),
        F("predefined ARGs", "#predefined-args", ["HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "FTP_PROXY", "ftp_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy"]),
        F("automatic platform ARGs", "#automatic-platform-args-in-the-global-scope", ["TARGETPLATFORM", "TARGETOS", "TARGETARCH", "TARGETVARIANT", "BUILDPLATFORM", "BUILDOS", "BUILDARCH", "BUILDVARIANT"]),
        F("BuildKit built-in build args", "#buildkit-built-in-build-args", ["BUILDKIT_BUILD_NAME", "BUILDKIT_CACHE_MOUNT_NS", "BUILDKIT_CONTEXT_KEEP_GIT_DIR", "BUILDKIT_GIT_ADVICE", "BUILDKIT_INLINE_CACHE", "BUILDKIT_MULTI_PLATFORM", "BUILDKIT_SANDBOX_HOSTNAME", "BUILDKIT_SYNTAX", "SOURCE_DATE_EPOCH (Dockerfile 1.5, BuildKit 0.11)"]),
    ]),
    ("ONBUILD", "#onbuild", "Register a trigger instruction that runs when the image is used as a base for another build.", [
        F("trigger", "#onbuild", ["<INSTRUCTION> (any except ONBUILD, FROM, MAINTAINER)"]),
        F("trigger with --from / --mount from", "#copy-or-mount-from-stage-image-or-context", ["COPY --from=<stage|image|context>", "RUN --mount=from=<...>"], "1.11"),
    ]),
    ("STOPSIGNAL", "#stopsignal", "Set the signal sent to stop the container.", [
        F("signal", "#stopsignal", ["SIG<NAME>", "<unsigned number>"]),
    ]),
    ("HEALTHCHECK", "#healthcheck", "Define (or disable with NONE) the container health check.", [
        F("CMD", "#healthcheck", ["shell form command", "exec form array"]),
        F("NONE", "#healthcheck", []),
        F("--interval", "#healthcheck", ["DURATION (default 30s)"]),
        F("--timeout", "#healthcheck", ["DURATION (default 30s)"]),
        F("--start-period", "#healthcheck", ["DURATION (default 0s)"]),
        F("--start-interval", "#healthcheck", ["DURATION (default 5s)"], None, "Needs Docker Engine 25.0+."),
        F("--retries", "#healthcheck", ["N (default 3)"]),
    ]),
    ("SHELL", "#shell", "Override the default shell used by shell-form instructions.", [
        F("exec form", "#shell", ['["executable", "parameters"] (JSON array only)']),
    ]),
]

NOTE = "No Dockerfile composer in the provider; docker_image (resource) build {context, dockerfile} only builds from an existing Dockerfile file."
items = []
for ins, anc, summ, flags in blocks:
    it = {"id": "dockerfile:" + ins, "layer": "dockerfile", "kind": "dockerfile_block",
          "name": "docker_dockerfile_" + ins.lower(), "summary": summ,
          "sources": [{"type": "doc", "file": REF, "anchor": anc}],
          "min_api": None, "status": "missing", "existing": [],
          "status_note": NOTE, "cli_only": False, "flags": flags}
    if ins == "MAINTAINER":
        it["status_note"] = ("Deprecated instruction (build check MaintainerDeprecated). Kept as a block for parse/round-trip "
                             "completeness; a generator should emit LABEL org.opencontainers.image.authors instead. " + NOTE)
    if ins == "ONBUILD":
        it["collision"] = "Nests another instruction block; could instead be an `onbuild` attribute on each docker_dockerfile_* block."
    items.append(it)

rules = []
for fn in sorted(os.listdir(os.path.join(DOCS, "rules"))):
    if fn == "_index.md":
        continue
    txt = open(os.path.join(DOCS, "rules", fn), encoding="utf-8").read()
    title = re.search(r"^title:\s*(\S+)", txt, re.M).group(1)
    f = {"name": title, "options": [], "anchor": "rules/" + fn}
    if "This check is experimental" in txt:
        f["experimental"] = True
        f["note"] = "Experimental check, not enabled by default."
    rules.append(f)

directives = [
    ("syntax", "#syntax", "Parser directive selecting the Dockerfile frontend image/version.", [
        F("syntax", "#syntax", ["docker/dockerfile:1", "docker/dockerfile:1.x", "<custom frontend image>"])]),
    ("escape", "#escape", "Parser directive setting the escape character.", [
        F("escape", "#escape", ["\\ (default)", BT])]),
    ("check", "#check", "Parser directive configuring build checks (skip list, error on warnings).",
     [F("skip", "#check", ["<check name>[,<check name>...]", "all"], "1.8"),
      F("error", "#check", ["true", "false"], "1.8")] + rules),
]
for d, anc, summ, flags in directives:
    srcs = [{"type": "doc", "file": REF, "anchor": anc}]
    if d == "check":
        srcs += [{"type": "doc", "file": "upstream/dockerfile/frontend/dockerfile/docs/" + r["anchor"]} for r in rules]
        srcs.append({"type": "doc", "file": "upstream/dockerfile/frontend/dockerfile/docs/rules/_index.md"})
    it = {"id": "dockerfile:directive-" + d, "layer": "dockerfile", "kind": "dockerfile_block",
          "name": "docker_dockerfile_directive_" + d, "summary": summ, "sources": srcs,
          "min_api": None, "status": "missing", "existing": [],
          "status_note": "No Dockerfile composer in the provider; docker_image (resource) builds from an existing Dockerfile file only.",
          "cli_only": False, "flags": flags}
    items.append(it)

bad = []
for it in items:
    for s in it["sources"]:
        if s["file"] == REF and s["anchor"] not in anchors:
            bad.append((it["id"], s["anchor"]))
    for f in it["flags"]:
        a = f["anchor"]
        if a.startswith("#") and a not in anchors:
            bad.append((it["id"], f["name"], a))
        if a.startswith("rules/") and not os.path.exists(os.path.join(DOCS, a)):
            bad.append((it["id"], a))
if bad:
    print("BAD ANCHORS", bad)
    sys.exit(1)

doc = {
    "lane": "Z4",
    "notes": [
        "Source: Dockerfile frontend 1.28.0 reference.md. syntax.md and experimental.md are redirect stubs ('This page has moved to reference.md'); no labs-only instructions or flags are documented in this snapshot, so no flag carries \"labs\": true.",
        "min_syntax comes from the 'Minimum Dockerfile version' tables (RUN, ADD, COPY) and inline 'since Dockerfile v1.x' text; blank table cells (COPY --from, COPY/ADD --chown) get no min_syntax. Sub-option versions (secret env 1.10, chmod symbolic 1.14, SOURCE_DATE_EPOCH 1.5) are noted inside options.",
        "RUN --device also needs BuildKit 0.20.0; HEALTHCHECK --start-interval needs Docker Engine 25.0; RUN --network=host, --security=insecure and --device need buildkitd entitlements (noted on the flag).",
        "Shell form, exec form and heredoc are flag entries on the instructions that accept them; heredocs are documented for RUN and COPY only.",
        "check directive flags = skip, error, then one flag per build-check rule (21 rules from rules/*.md; InvalidDefinitionDescription is experimental). Rule anchors are rules/<file>.md paths.",
        "MAINTAINER kept as a dockerfile_block (not skip): it still parses in 1.28 and appears in existing Dockerfiles; status_note marks it deprecated.",
        "Not itemised: .dockerignore (context-side file, not an instruction), environment replacement rules (cross-cutting syntax), comments.",
        "All '#' anchors verified against GitHub-style slugs of reference.md headings by z4_gen.py; rules files verified to exist.",
        "Status 'missing' for every block: the provider has no Dockerfile composer. Related: docker_image (resource) build block takes context + dockerfile path and builds via the Engine API / BuildKit session."
    ],
    "naming_questions": [
        "Shape: (a) one type per instruction (docker_dockerfile_run, docker_dockerfile_from, ...) vs (b) a single data source data.docker_dockerfile with ordered nested blocks (stage { from, run, copy ... }) rendering Dockerfile text vs (c) a provider function. (b) recommended: ordering and stages fit nested blocks; 21 trivial types would bloat the registry.",
        "Directives: docker_dockerfile_directive_syntax/escape/check types vs top-level attributes syntax, escape, check { skip = [...], error = bool } on data.docker_dockerfile (recommended).",
        "Include MAINTAINER (deprecated) at all, or omit and steer users to LABEL org.opencontainers.image.authors?",
        "ONBUILD: separate block nesting an instruction vs an `onbuild = true` attribute on each instruction block?",
        "Stages: model FROM ... AS name as a `stage` block with `name` (recommended) rather than a FROM flag?",
        "check rule names: validate against a pinned list (21 rules at frontend 1.28) or accept free strings?"
    ],
    "items": items,
}
with open(OUT, "w", encoding="utf-8", newline="\n") as fh:
    json.dump(doc, fh, indent=2, ensure_ascii=False)
    fh.write("\n")
print("blocks", len(items), "flags", sum(len(i["flags"]) for i in items), "rules", len(rules))
