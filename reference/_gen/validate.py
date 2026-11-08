"""Check the outputs contract (rules V1-V5 in SCHEMA.md) on a merged inventory.json.

Run through `Invoke-Build InventoryValidate` from the repo root. Stdlib only.
Violations listed in allowed-violations.txt (`<id><TAB><rule><TAB><reason>`) are reported as allowed;
any other violation makes the exit code 1. A lane can check its own items on a dry-run merge:

    python -I reference/_gen/merge.py --dry-run --outputs <lane> --emit <tmp>/inv.json
    python -I reference/_gen/validate.py <tmp>/inv.json --lane <lane>
"""
import argparse, json, os, sys

HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.normpath(os.path.join(HERE, ".."))
VERSION_NAMES = {"engine_api_version", "docker_server_version", "compose_spec_version",
                 "compose_go_version", "dockerfile_syntax", "buildx_version",
                 "buildkit_version", "docker_cli_version"}
OBJECTISH = ("object(", "list(object(", "set(object(", "map(object(")

ap = argparse.ArgumentParser()
ap.add_argument("inventory", nargs="?", default=os.path.join(REF, "inventory.json"))
ap.add_argument("--allowed", default=os.path.join(HERE, "allowed-violations.txt"))
ap.add_argument("--lane", help="only items whose outputs come from this lane")
args = ap.parse_args()

with open(args.inventory, encoding="utf-8") as f:
    inv = json.load(f)

allowed, bad_allowed = {}, []
if os.path.exists(args.allowed):
    with open(args.allowed, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.rstrip("\r\n")
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) != 3 or not parts[2].strip():
                bad_allowed.append("allowed-violations.txt:%d: want <id><TAB><rule><TAB><reason>" % n)
                continue
            allowed[(parts[0], parts[1])] = parts[2]

violations = []  # (id, rule, text)
checked = 0
for it in inv["items"]:
    if it["kind"] == "skip":
        continue
    if args.lane and it.get("outputs_lane") != args.lane:
        continue
    checked += 1
    iid, outs = it["id"], it.get("outputs") or []
    cats = {o["category"] for o in outs}
    if not outs:
        violations.append((iid, "V1", "no outputs at all"))
    else:
        if "identity" not in cats:
            violations.append((iid, "V1", "no identity output"))
        if "version" not in cats:
            violations.append((iid, "V1", "no version output"))
    if it["kind"] in ("resource", "data_source"):
        if "raw" not in cats and not it.get("no_raw_reason"):
            violations.append((iid, "V2", "no raw output and no no_raw_reason"))
        if "structured" not in cats and not it.get("no_structured_reason"):
            violations.append((iid, "V2", "no structured output and no no_structured_reason"))
    for o in outs:
        if o["category"] == "version" and o["name"] not in VERSION_NAMES:
            violations.append((iid, "V3", "version output %s is not one of %s" % (o["name"], ", ".join(sorted(VERSION_NAMES)))))
        if o["category"] == "structured" and not o.get("fields"):
            violations.append((iid, "V4", "structured output %s has no fields" % o["name"]))
        if o["category"] == "result" and o["tf_type"].startswith(OBJECTISH) and not o.get("fields"):
            violations.append((iid, "V4", "result output %s is an object but has no fields" % o["name"]))
        if o["category"] in ("structured", "result") and o.get("sensitive") and not o.get("sensitive_reason"):
            violations.append((iid, "V5", "%s output %s is sensitive as a whole with no sensitive_reason; mark the secret-carrying fields instead" % (o["category"], o["name"])))

failing = [v for v in violations if (v[0], v[1]) not in allowed]
used = {(v[0], v[1]) for v in violations}
stale = [k for k in allowed if k not in used and not args.lane]

for iid, rule, text in violations:
    tag = "ALLOWED" if (iid, rule) in allowed else "VIOLATION"
    print("%-9s %s %s: %s%s" % (tag, rule, iid, text, (" (" + allowed[(iid, rule)] + ")") if tag == "ALLOWED" else ""))
for k in stale:
    print("STALE     allowed-violations.txt entry matches nothing: %s %s" % k)
for b in bad_allowed:
    print("ERROR     " + b)
n_out = sum(len(i.get("outputs") or []) for i in inv["items"] if not args.lane or i.get("outputs_lane") == args.lane)
print("checked %d items, %d outputs: %d violations (%d allowed, %d failing), %d stale allowed entries"
      % (checked, n_out, len(violations), len(violations) - len(failing), len(failing), len(stale)))
sys.exit(1 if failing or bad_allowed or stale else 0)
