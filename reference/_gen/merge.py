"""Merge reference/_gen/fragments/*.json into reference/inventory.json and reference/INVENTORY.md.

Run through `Invoke-Build Inventory` from the repo root. Stdlib only. Exits 1 on any validation error
(the outputs are still written, so the error can be inspected in the diff)."""
import json, os, sys, collections, re, argparse

ap = argparse.ArgumentParser(description="Merge inventory fragments (no arguments: write reference/inventory.json + INVENTORY.md).")
ap.add_argument("--dry-run", action="store_true", help="validate only; write nothing under reference/")
ap.add_argument("--emit", help="also write the merged inventory.json to this path (for validate.py)")
ap.add_argument("--outputs", action="append", help="load only this outputs/<lane>.json (repeatable); default all")
ARGS = ap.parse_args()

HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.normpath(os.path.join(HERE, ".."))  # reference/
Z = os.path.join(HERE, "fragments")
FRAGS = ["z1-engine.json", "z2-compose.json", "z3-cli.json", "z4-dockerfile.json"]
# Fragments added after the harvest; each is read only when the file exists, in this order.
EXTRA_FRAGS = ["dockerfile-sources.json", "pass5.json"]
OUTPUTS_DIR = os.path.join(Z, "outputs")  # outputs/<lane>.json, joined onto items by id (SCHEMA.md)
OUT_CATEGORIES = ["raw", "structured", "result", "identity", "version", "hash"]
OUT_SOURCE_TYPES = ["swagger", "schema", "doc", "cli", "sources_md", "derived"]
ITEM_OUT_KEYS = {"no_raw_reason", "no_structured_reason", "outputs_status", "outputs_note", "uses_cli", "inputs"}
TF_TYPE = re.compile(r"^(string|number|bool|dynamic|(list|set|map)\(.+\)|object\(\{.*\}\))$", re.S)
LAYERS = ["engine", "swarm", "compose", "dockerfile", "buildx", "cli-only"]
KINDS = ["resource", "data_source", "action", "ephemeral_resource", "list_resource", "function", "dockerfile_block", "skip"]
# Terraform kind label per inventory kind (collisions, the `registered` field).
TFKIND = {"resource": "resource", "data_source": "data", "action": "action", "ephemeral_resource": "ephemeral",
          "list_resource": "list", "function": "function", "dockerfile_block": "block"}
# Status is computed, never hand-set (Pass 5): from the registration in internal/provider/registry_gen.go
# (generated from this inventory by tools/registrygen) and the stub markers in internal/provider/*.go.
PROVIDER = os.path.normpath(os.path.join(REF, "..", "internal", "provider"))
REGISTRY_GEN = os.path.join(PROVIDER, "registry_gen.go")
MARKER = re.compile(r"//\s*STUB\(pass5\)\s+([a-z-]+:[A-Za-z0-9._-]+)(?:[ \t]+partial:[ \t]*(.*))?")
STATUSES = ["exists", "partial", "missing"]



def field_flags(fields, prefix):
    """Yield (dotted path, sensitive value) for every FIELD that sets sensitive, at any depth."""
    for fd in fields:
        p = prefix + fd.get("name", "?")
        if "sensitive" in fd:
            yield p, fd["sensitive"]
        yield from field_flags(fd.get("fields") or [], p + ".")


items, notes, questions, errors = [], {}, [], []
for fn in FRAGS + [x for x in EXTRA_FRAGS if os.path.exists(os.path.join(Z, x))]:
    with open(os.path.join(Z, fn), encoding="utf-8") as f:
        d = json.load(f)
    notes[d.get("lane", fn)] = d.get("notes", [])
    questions += [(d.get("lane", fn), q) for q in d.get("naming_questions", [])]
    for it in d["items"]:
        it["_lane"] = d.get("lane", fn)
        items.append(it)

cli_map = None
p = os.path.join(Z, "z3-cli-map.json")
if os.path.exists(p):
    with open(p, encoding="utf-8") as f:
        cli_map = json.load(f)

# ---- validation
ids = collections.Counter(i["id"] for i in items)
for k, n in ids.items():
    if n > 1:
        errors.append("duplicate id %s x%d" % (k, n))
for it in items:
    if it.get("layer") not in LAYERS:
        errors.append("%s bad layer %r" % (it["id"], it.get("layer")))
    if it.get("kind") not in KINDS:
        errors.append("%s bad kind %r" % (it["id"], it.get("kind")))
    if it["kind"] == "skip":
        if not it.get("skip_reason"):
            errors.append("%s skip without reason" % it["id"])
    else:
        if not it.get("name"):
            errors.append("%s no name" % it["id"])
        for k in ("status", "status_note", "existing"):
            if k in it:
                errors.append("%s: %s is computed by merge.py (Pass 5); remove it from the fragment" % (it["id"], k))
    if it["kind"] == "resource" and it.get("update") not in ("in_place", "replace"):
        errors.append("%s resource without update" % it["id"])
    for s in it.get("sources", []):
        fp = s.get("file")
        if fp and not os.path.exists(os.path.join(REF, fp.replace("/", os.sep))):
            errors.append("%s source file missing: %s" % (it["id"], fp))

# swagger coverage
spec = open(os.path.join(REF, "upstream", "engine-api", "api", "swagger.yaml"), encoding="utf-8").read()
all_ops = set(re.findall(r"^\s+operationId:\s*\"?([A-Za-z0-9]+)\"?\s*$", spec, re.M))
seen = collections.Counter()
primary = collections.Counter()
for it in items:
    for s in it.get("sources", []):
        if s.get("type") == "swagger" and s.get("operationId"):
            seen[s["operationId"]] += 1
            if s.get("primary"):
                primary[s["operationId"]] += 1
for o, n in primary.items():
    if n > 1:
        errors.append("operation %s is primary in %d items" % (o, n))
for o in all_ops:
    if seen[o] and not primary[o]:
        errors.append("operation %s referenced but has no primary home" % o)
uncovered = sorted(all_ops - set(seen))
unknown = sorted(set(seen) - all_ops)
for o in unknown:
    errors.append("unknown operationId %s" % o)

# ---- outputs: join outputs/<lane>.json onto items by id
by_id = {i["id"]: i for i in items}
out_owner = {}
out_files = sorted(f for f in os.listdir(OUTPUTS_DIR) if f.endswith(".json")) if os.path.isdir(OUTPUTS_DIR) else []
if ARGS.outputs:
    out_files = [f for f in out_files if f in ARGS.outputs or f[:-5] in ARGS.outputs]
for fn in out_files:
    with open(os.path.join(OUTPUTS_DIR, fn), encoding="utf-8") as f:
        d = json.load(f)
    lane = "outputs:" + d.get("lane", fn[:-5])
    notes[lane] = d.get("notes", [])
    questions += [(lane, q) for q in d.get("naming_questions", [])]
    for iid, entry in d.get("items", {}).items():
        if iid in out_owner:
            errors.append("outputs for %s in both %s and %s" % (iid, out_owner[iid], fn))
            continue
        out_owner[iid] = fn
        it = by_id.get(iid)
        if it is None:
            errors.append("%s: unknown item id %s" % (fn, iid))
            continue
        if it["kind"] == "skip":
            errors.append("%s: outputs for skipped item %s" % (fn, iid))
        outs = entry.get("outputs", [])
        names = collections.Counter(o.get("name") for o in outs)
        for n, c in names.items():
            if c > 1:
                errors.append("%s: output %s defined %d times" % (iid, n, c))
        raws = {o.get("name") for o in outs if o.get("category") == "raw"}
        for o in outs:
            where = "%s.%s" % (iid, o.get("name"))
            for k in ("name", "tf_type", "category", "source", "known_after_apply", "sensitive"):
                if k not in o:
                    errors.append("%s: missing %s" % (where, k))
            if o.get("name") and not re.match(r"^[a-z][a-z0-9_]*$", o["name"]):
                errors.append("%s: name is not snake_case" % where)
            if o.get("category") not in OUT_CATEGORIES:
                errors.append("%s: bad category %r" % (where, o.get("category")))
            if not TF_TYPE.match(str(o.get("tf_type", ""))):
                errors.append("%s: bad tf_type %r" % (where, o.get("tf_type")))
            for k in ("known_after_apply", "sensitive"):
                if k in o and not isinstance(o[k], bool):
                    errors.append("%s: %s is not a bool" % (where, k))
            src = o.get("source") or {}
            if src.get("type") not in OUT_SOURCE_TYPES:
                errors.append("%s: bad source type %r" % (where, src.get("type")))
            if src.get("file") and not os.path.exists(os.path.join(REF, src["file"].replace("/", os.sep))):
                errors.append("%s: source file missing: %s" % (where, src["file"]))
            if o.get("category") == "hash" and o.get("sensitive"):
                errors.append("%s: a hash output is never sensitive" % where)
            if "sensitive_reason" in o and not (o.get("sensitive") and str(o["sensitive_reason"]).strip()):
                errors.append("%s: sensitive_reason needs sensitive: true and a non-empty reason" % where)
            for fp, fv in field_flags(o.get("fields") or [], ""):
                if not isinstance(fv, bool):
                    errors.append("%s: field %s sensitive is not a bool" % (where, fp))
                elif fv and o.get("sensitive"):
                    errors.append("%s: field %s is marked sensitive inside an output that is already sensitive as a whole" % (where, fp))
            if o.get("category") == "hash" and o.get("hash_of") not in raws:
                errors.append("%s: hash_of %r is not a raw output of this item" % (where, o.get("hash_of")))
        for r in raws:
            if not any(o.get("category") == "hash" and o.get("hash_of") == r for o in outs):
                errors.append("%s: raw output %s has no hash output" % (iid, r))
        it["outputs"] = outs
        for k, v in entry.items():
            if k == "outputs":
                continue
            if k not in ITEM_OUT_KEYS:
                errors.append("%s: unknown item-level key %s in %s" % (iid, k, fn))
                continue
            it[k] = v
        if not isinstance(entry.get("uses_cli"), bool):
            errors.append("%s: uses_cli must be true or false (does the item run the docker CLI?)" % iid)
        inputs = entry.get("inputs", [])
        if not isinstance(inputs, list):
            errors.append("%s: inputs is not a list" % iid)
            inputs = []
        for inp in inputs:
            for k in ("name", "tf_type", "default", "source"):
                if not isinstance(inp, dict) or k not in inp:
                    errors.append("%s: input %r missing %s" % (iid, inp.get("name") if isinstance(inp, dict) else inp, k))
            if isinstance(inp, dict) and (inp.get("source") or {}).get("type") not in OUT_SOURCE_TYPES:
                errors.append("%s: input %s bad source type" % (iid, inp.get("name")))
        if entry.get("outputs_status") not in (None, "exists", "partial", "missing"):
            errors.append("%s: bad outputs_status %r" % (iid, entry.get("outputs_status")))
        it["outputs_lane"] = d.get("lane", fn[:-5])

# ---- status: computed from registry_gen.go + stub markers
registered = {}  # (kind, name) -> constructor
if os.path.exists(REGISTRY_GEN):
    with open(REGISTRY_GEN, encoding="utf-8") as f:
        for line in f:
            m = re.match(r"^\s*(New\w+),\s*//\s*(\w+)\s+(\S+)\s*$", line)
            if m:
                registered[(m.group(2), m.group(3))] = m.group(1)
markers = collections.defaultdict(list)  # item id -> [(file, partial note or None)]
for fn in sorted(os.listdir(PROVIDER)) if os.path.isdir(PROVIDER) else []:
    if not fn.endswith(".go") or fn.endswith("_test.go"):
        continue
    with open(os.path.join(PROVIDER, fn), encoding="utf-8") as f:
        for m in MARKER.finditer(f.read()):
            markers[m.group(1)].append((fn, (m.group(2) or "").strip() or None))
for mid in sorted(markers):
    if mid not in by_id:
        errors.append("stub marker for unknown item %s in %s" % (mid, ", ".join(sorted({x[0] for x in markers[mid]}))))
    elif by_id[mid]["kind"] == "skip":
        errors.append("stub marker for skipped item %s" % mid)
for it in items:
    if it["kind"] == "skip":
        continue
    key = ("data_source", "docker_dockerfile") if it["kind"] == "dockerfile_block" else (it["kind"], it["name"])
    mk = markers.get(it["id"], [])
    if key not in registered:
        it["status"], it["status_note"] = "missing", "not registered"
    elif any(n is None for _, n in mk):
        it["status"], it["status_note"] = "missing", "stub: not implemented (Pass 5)"
    elif mk:
        it["status"], it["status_note"] = "partial", "; ".join(n for _, n in mk)
    else:
        it["status"] = "exists"
    if key in registered:
        it["registered"] = ("%s.%s" % (TFKIND[key[0]], key[1])) if key[0] != "resource" else key[1]
    if mk:
        it["stub_files"] = sorted({x[0] for x in mk})

# ---- collisions: same name used by >1 item of the same Terraform kind
by_name = collections.defaultdict(list)
for it in items:
    if it.get("name") and it["kind"] != "skip":
        tfkind = TFKIND[it["kind"]]
        by_name[(tfkind, it["name"])].append(it["id"])
auto_collisions = [(k, v) for k, v in sorted(by_name.items()) if len(v) > 1]
flagged = [(i["id"], i["name"], i["collision"]) for i in items if i.get("collision")]

# ---- totals
tot = collections.OrderedDict()
for L in LAYERS:
    tot[L] = {k: collections.Counter() for k in KINDS}
for it in items:
    tot[it["layer"]][it["kind"]][it.get("status", "n/a") if it["kind"] != "skip" else "n/a"] += 1

# ---- write inventory.json
out_items = []
for it in sorted(items, key=lambda i: (LAYERS.index(i["layer"]), KINDS.index(i["kind"]), i.get("name") or "", i["id"])):
    o = {k: v for k, v in it.items() if not k.startswith("_")}
    o["lane"] = it["_lane"]
    out_items.append(o)
inv = {
    "generated": "2026-11-08",
    "about": "Every Docker object, operation and spec element in reference/upstream (see SOURCES.md) with its decided Terraform name (DECISIONS.md). status is computed from internal/provider/registry_gen.go and the Pass 5 stub markers. Paths in sources are relative to reference/.",
    "pins": {
        "engine_api": "1.56 (moby api/v1.56.1 @ 14ebc602124e280ef4ae3e1a8e688dac1255baa6)",
        "compose_spec": "main @ 3a4a6995e4a971f6173dff9130cb3e38e6738651",
        "compose_go": "v2.16.2 @ ddbe099a5404149d75fe774b30de7bd0befd3bd3",
        "compose_cli": "v5.1.4 @ 4732a2ed20a49c43a7b44bb4928259162c3ae3be",
        "docker_cli": "v29.5.3 @ d1c06ef6b41d88d76866aea43c246cd7c63d04fa",
        "dockerfile": "dockerfile/1.28.0 @ f5b59a1f2fb25a0cb7072c398cab9aed7835da4e",
        "buildx": "v0.38.2 @ 3c4cbd35852fc1a476923627a780a7836c28b654",
    },
    "swagger_coverage": {"operations": len(all_ops), "covered": len(all_ops & set(seen)), "uncovered": uncovered,
                         "primary_homes": len(primary)},
    "totals": {L: {k: dict(c) for k, c in kk.items() if sum(c.values())} for L, kk in tot.items()},
    "naming_questions": [{"lane": l, "question": q} for l, q in questions],
    "collisions": {"same_name_same_kind": [{"kind": k[0], "name": k[1], "items": v} for k, v in auto_collisions],
                   "flagged": [{"id": a, "name": b, "note": c} for a, b, c in flagged]},
    "lane_notes": notes,
    "items": out_items,
}
if cli_map:
    inv["cli_map"] = cli_map
for target in ([] if ARGS.dry_run else [os.path.join(REF, "inventory.json")]) + ([ARGS.emit] if ARGS.emit else []):
    with open(target, "w", encoding="utf-8", newline="\n") as f:
        json.dump(inv, f, indent=2, ensure_ascii=False)
        f.write("\n")

# ---- write INVENTORY.md
def esc(s):
    return (s or "").replace("|", "\\|").replace("\n", " ")

def srcs(it):
    out = []
    for s in it.get("sources", []):
        t = s.get("type")
        if t == "swagger":
            out.append("`%s %s` (%s)" % (s.get("method", ""), s.get("path", ""), s.get("operationId", "")))
        elif t == "schema":
            out.append("`%s`" % s.get("pointer", ""))
        elif t == "cli":
            out.append("`%s`" % s.get("command", ""))
        elif t == "doc":
            out.append("%s%s" % (os.path.basename(s.get("file", "")), s.get("anchor", "") or ""))
    if len(out) > 6:
        out = out[:6] + ["+%d more" % (len(out) - 6)]
    return "<br>".join(esc(x) for x in out)

KN = {"resource": "resource", "data_source": "data source", "action": "action", "ephemeral_resource": "ephemeral",
      "list_resource": "list resource", "function": "function", "dockerfile_block": "block", "skip": "skip"}
md = ["# Inventory", "",
      "The Terraform name of every item in [upstream/](upstream/) ([SOURCES.md](SOURCES.md)). Machine-readable: [inventory.json](inventory.json).", "",
      "Names follow [DECISIONS.md](DECISIONS.md) (Pass 5). Status is computed by `_gen/merge.py`: `missing` = not registered in `internal/provider/registry_gen.go`, or still a Pass 5 stub; `partial` = registered with an open Pass 5 marker (the note says what is left); `exists` = registered with no marker. `cli` = CLI-only (no Engine API endpoint, client-side). Update: `in place` / `replace` for resources.", ""]
TITLES = {"engine": "Engine", "swarm": "Swarm", "compose": "Compose", "dockerfile": "Dockerfile", "buildx": "Buildx", "cli-only": "CLI-only"}
for L in LAYERS:
    rows = [i for i in out_items if i["layer"] == L]
    if not rows:
        continue
    md += ["## %s" % TITLES[L], ""]
    if L == "dockerfile":
        md += ["| kind | name | flags | sources | status |", "|---|---|---|---|---|"]
        for it in rows:
            fl = ", ".join("`%s`" % esc(x["name"]) for x in it.get("flags", []))
            st = it.get("status", "") if it["kind"] != "skip" else "skip: " + it.get("skip_reason", "")
            if it.get("decisions"):
                st += " (" + ", ".join(it["decisions"]) + ")"
            md.append("| %s | `%s` | %s | %s | %s |" % (KN[it["kind"]], it.get("name") or "-", fl, srcs(it), esc(st)))
    else:
        md += ["| kind | name | update | sources | min API | status | note |", "|---|---|---|---|---|---|---|"]
        for it in rows:
            upd = {"in_place": "in place", "replace": "replace"}.get(it.get("update"), "")
            if it["kind"] == "skip":
                st, note = "-", "skip: " + it.get("skip_reason", "")
            else:
                st = it.get("status", "")
                note = it.get("status_note", "") if st == "partial" else ""
                if it.get("decisions"):
                    note = (note + " " if note else "") + "(" + ", ".join(it["decisions"]) + ")"
                if it.get("previous_name"):
                    note = (note + " " if note else "") + "(was `%s`)" % it["previous_name"]
            if it.get("cli_only"):
                st += " · cli"
            if it.get("collision"):
                note += " ⚠ " + it["collision"]
            md.append("| %s | %s | %s | %s | %s | %s | %s |" % (
                KN[it["kind"]], ("`%s`" % it["name"]) if it.get("name") else "-", upd, srcs(it),
                it.get("min_api") or "", esc(st), esc(note)))
    md.append("")

# ---- outputs tables (only when any outputs file was joined)
if any(i.get("outputs") for i in out_items):
    n_items = sum(1 for i in out_items if i.get("outputs"))
    n_outs = sum(len(i.get("outputs") or []) for i in out_items)
    md += ["## Outputs", "",
           "The outputs contract ([CONTRACT.md](CONTRACT.md)): %d items, %d outputs, from [_gen/fragments/outputs/](_gen/fragments/outputs/). "
           "`*` = sensitive, `(a.b*)` = only those nested fields are sensitive, `+` = known after apply. Full detail (types, sources, field maps) is in inventory.json." % (n_items, n_outs), ""]

    def ocell(it, cat):
        names = []
        for o in it.get("outputs") or []:
            if o.get("category") == cat:
                secret = [fp for fp, fv in field_flags(o.get("fields") or [], "") if fv is True]
                sf = "(%s*)" % ", ".join(secret) if secret and not o.get("sensitive") else ""
                names.append("`%s`%s%s%s" % (o["name"], "*" if o.get("sensitive") else "", sf, "+" if o.get("known_after_apply") else ""))
        if not names and cat in ("raw", "structured") and it.get("no_%s_reason" % cat):
            return "none: " + esc(it["no_%s_reason" % cat])
        return ", ".join(names)

    for L in LAYERS:
        rows = [i for i in out_items if i["layer"] == L and i["kind"] != "skip"]
        if not any(i.get("outputs") for i in rows):
            continue
        md += ["### %s" % TITLES[L], "",
               "| kind | name | raw | structured | result | identity | version | hash | outputs status |",
               "|---|---|---|---|---|---|---|---|---|"]
        for it in rows:
            st = it.get("outputs_status", "")
            if it.get("outputs_note"):
                st = (st + ": " if st else "") + it["outputs_note"]
            md.append("| %s | %s | %s | %s | %s | %s | %s | %s | %s |" % (
                KN[it["kind"]], ("`%s`" % it["name"]) if it.get("name") else "-",
                ocell(it, "raw"), ocell(it, "structured"), ocell(it, "result"), ocell(it, "identity"),
                ocell(it, "version"), ocell(it, "hash"), esc(st)))
        md.append("")

md += ["## Totals", "", "Counts as exists / partial / missing.", "",
       "| layer | resources | data sources | actions | ephemeral | list resources | functions | blocks | skipped |",
       "|---|---|---|---|---|---|---|---|---|"]
def cell(c):
    n = sum(c[s] for s in STATUSES)
    return "%d (%d / %d / %d)" % (n, c["exists"], c["partial"], c["missing"]) if n else "0"
grand = {k: collections.Counter() for k in KINDS}
for L in LAYERS:
    t = tot[L]
    for k in KINDS:
        grand[k].update(t[k])
    md.append("| %s | %s | %d |" % (L, " | ".join(cell(t[k]) for k in KINDS[:-1]), sum(t["skip"].values())))
md.append("| **all** | %s | %d |" % (" | ".join(cell(grand[k]) for k in KINDS[:-1]), sum(grand["skip"].values())))
md += ["", "Engine API coverage: %d of %d swagger operations placed%s." % (
    len(all_ops & set(seen)), len(all_ops), (" (uncovered: " + ", ".join(uncovered) + ")") if uncovered else ""), ""]

md += ["## Naming collisions", "",
       "Shared names below are intentional: the items are facets of one existing type, not competing proposals.", ""]
if auto_collisions:
    for k, v in auto_collisions:
        md.append("- Shared %s name `%s`: %s" % (k[0], k[1], ", ".join(v)))
for a, b, c in flagged:
    md.append("- `%s` (%s): %s" % (b or "-", a, esc(c)))
md += ["", "## Naming questions", ""]
for n, (l, q) in enumerate(questions, 1):
    md.append("%d. %s" % (n, esc(q)))
md.append("")
if not ARGS.dry_run:
    with open(os.path.join(REF, "INVENTORY.md"), "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(md))

print("items", len(items), "errors", len(errors))
for e in errors[:80]:
    print("ERR", e)
print("swagger ops", len(all_ops), "covered", len(all_ops & set(seen)), "uncovered", uncovered[:30])
print("auto collisions", auto_collisions)
for L in LAYERS:
    print(L, {k: dict(c) for k, c in tot[L].items() if sum(c.values())})
sys.exit(1 if errors else 0)
