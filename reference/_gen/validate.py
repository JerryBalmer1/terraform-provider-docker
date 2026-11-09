"""Check the outputs contract (rules V1-V6 and L1 in SCHEMA.md) on a merged inventory.json.

Run through `Invoke-Build InventoryValidate` from the repo root. Stdlib only.
Violations listed in allowed-violations.txt (`<id><TAB><rule><TAB><reason>`) are reported as allowed;
any other violation makes the exit code 1. A lane can check its own items on a dry-run merge:

    python -I reference/_gen/merge.py --dry-run --outputs <lane> --emit <tmp>/inv.json
    python -I reference/_gen/validate.py <tmp>/inv.json --lane <lane>

The version names V3 accepts are read from the registry table in SCHEMA.md (between the version-registry markers).
"""
import argparse, json, os, re, sys

HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.normpath(os.path.join(HERE, ".."))
# Kinds outside the contract: skipped items, and list resources / provider functions (Pass 5), which return the
# managed resource's own state or a computed value rather than reading or changing a Docker object themselves.
NO_CONTRACT = ("skip", "list_resource", "function")
OBJECTISH = ("object(", "list(object(", "set(object(", "map(object(")

ap = argparse.ArgumentParser()
ap.add_argument("inventory", nargs="?", default=os.path.join(REF, "inventory.json"))
ap.add_argument("--allowed", default=os.path.join(HERE, "allowed-violations.txt"))
ap.add_argument("--lane", help="only items whose outputs come from this lane")
ap.add_argument("--schema", default=os.path.join(HERE, "SCHEMA.md"), help="SCHEMA.md holding the version registry")
args = ap.parse_args()


def read_registry(path):
    """{version name: required item flag or None} from the rows between the version-registry markers."""
    with open(path, encoding="utf-8") as f:
        text = f.read()
    m = re.search(r"<!-- version-registry -->(.*?)<!-- /version-registry -->", text, re.S)
    if not m:
        sys.exit("validate.py: no <!-- version-registry --> block in %s" % path)
    reg = {}
    for line in m.group(1).splitlines():
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 3 or not cells[0].startswith("`"):
            continue
        name, source, only = cells[0].strip("`"), cells[1], cells[2].strip("`")
        if not source:
            sys.exit("validate.py: version %s is registered without a source" % name)
        reg[name] = None if only in ("", "-") else only
    return reg


VERSIONS = read_registry(args.schema)

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
    if it["kind"] in NO_CONTRACT:
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
    if it["kind"] == "action" and "result" not in cats:
        violations.append((iid, "V6", "action has no result output"))
    names = {o["name"] for o in outs}
    if names & {"logs", "log_entries"}:
        inputs = {i["name"]: i for i in it.get("inputs") or []}
        if not inputs.keys() & {"tail", "since"}:
            violations.append((iid, "L1", "logs item has no tail or since input, so its state is unbounded"))
        elif "tail" in inputs and not (isinstance(inputs["tail"].get("default"), int) and inputs["tail"]["default"] > 0):
            violations.append((iid, "L1", "logs item's tail input has no positive default limit"))
    if it["kind"] in ("resource", "data_source"):
        if "raw" not in cats and not it.get("no_raw_reason"):
            violations.append((iid, "V2", "no raw output and no no_raw_reason"))
        if "structured" not in cats and not it.get("no_structured_reason"):
            violations.append((iid, "V2", "no structured output and no no_structured_reason"))
    for o in outs:
        if o["category"] == "version":
            if o["name"] not in VERSIONS:
                violations.append((iid, "V3", "version output %s is not registered in SCHEMA.md (registered: %s)"
                                   % (o["name"], ", ".join(sorted(VERSIONS)))))
            elif VERSIONS[o["name"]] and it.get(VERSIONS[o["name"]]) is not True:
                violations.append((iid, "V3", "version output %s needs %s: true on the item (it is %r)"
                                   % (o["name"], VERSIONS[o["name"]], it.get(VERSIONS[o["name"]]))))
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
