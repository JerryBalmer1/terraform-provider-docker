"""Pass 5 Lane 0: write internal/provider/pass5_todo_<lane>.go, the open-work markers for items whose type is
already implemented (partial) and for the Dockerfile blocks of the stub data.docker_dockerfile (missing).

    python -I -B reference/_gen/generators/pass5/p5_markers.py <inventory.json of 97ec6bf>

Run once, from the repo root, after `go run ./tools/registrygen -stubs` and `Invoke-Build Inventory`. Each marker
line is `// STUB(pass5) <item id> partial: <what is left>`; merge.py turns it into status partial with that note.
Lanes delete a line when the item is done, and the file when it is empty.
"""
import json, os, re, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
PROV = os.path.join(ROOT, "internal", "provider")
inv = json.load(open(os.path.join(ROOT, "reference", "inventory.json"), encoding="utf-8"))
old = {i["id"]: i for i in json.load(open(sys.argv[1], encoding="utf-8"))["items"]}
lanes = json.load(open(os.path.join(ROOT, "reference", "_gen", "pass5", "lanes.json"), encoding="utf-8"))

stubbed = set()
for fn in os.listdir(PROV):
    if fn.startswith("stubs_pass5_") and fn.endswith(".go"):
        stubbed |= set(re.findall(r"//\s*STUB\(pass5\)\s+(\S+)", open(os.path.join(PROV, fn), encoding="utf-8").read()))

TF = {"resource": "", "data_source": "data.", "action": "action.", "ephemeral_resource": "ephemeral.", "list_resource": "list.",
      "function": "provider::docker::"}
by_lane = {}
for it in inv["items"]:
    iid = it["id"]
    if it["kind"] == "skip" or iid in stubbed:
        continue
    lane = lanes[iid]
    if it["kind"] == "dockerfile_block":
        by_lane.setdefault(lane, []).append("// STUB(pass5) %s" % iid)
        continue
    parts = []
    o = old.get(iid, {})
    if o.get("status") == "partial" and o.get("status_note"):
        parts.append(o["status_note"].rstrip("."))
    if it.get("decisions"):
        parts.append("apply " + ", ".join(it["decisions"]))
    if it["kind"] not in ("list_resource", "function"):
        if it["kind"] == "action":
            parts.append("one `result` object + `result_path` (Q35) via action_result.go")
        else:
            names = [x["name"] for x in it.get("outputs") or [] if x["category"] != "identity"]
            if names:
                parts.append("contract outputs via contract.go: " + ", ".join(names))
        parts.append("renames.json rows for %s%s" % (TF[it["kind"]], it["name"]))
    else:
        parts.append("follow renames.json for the resource it lists" if it["kind"] == "list_resource" else "check against the decisions; nothing known open")
    note = "; ".join(parts)
    note = re.sub(r"\s+", " ", note)
    by_lane.setdefault(lane, []).append("// STUB(pass5) %s partial: %s" % (iid, note))

for lane, lines in sorted(by_lane.items()):
    path = os.path.join(PROV, "pass5_todo_%s.go" % lane.lower())
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        f.write("package provider\n\n// Pass 5 Lane %s owns this file: one marker line per inventory item of yours that is registered\n"
                "// but not finished (reference/_gen/merge.py reads them; Invoke-Build StubReport lists them). Delete a\n"
                "// line when its item is done, and the file when it is empty.\n\n" % lane)
        f.write("\n".join(sorted(lines)) + "\n")
    print(path, len(lines))
