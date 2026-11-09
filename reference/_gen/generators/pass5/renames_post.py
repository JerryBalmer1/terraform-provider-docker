"""Pass 5: apply reference/_gen/pass5/renames_overrides.json to reference/_gen/renames.json.

    python -I -B reference/_gen/generators/pass5/build_renames.py <kreuzwerker clone>   # base table
    python -I -B reference/_gen/generators/pass5/renames_post.py                        # decisions + lane results

renames_overrides.json holds what came after the audit: user decisions on shapes (shape_mappings, per-row
`set` patches) and the gap rows the lanes implemented (Lane Z). Each row patch is matched by
(type, kreuzwerker_attr, previous_attr); every patch must match exactly one row, or the script fails.
A patch with `"add": true` is a row the audit missed (Pass 5.1: our own schema changes that tools/parity's
previous-schema check reports): it is appended when no row matches and updated in place when one does, then
the table is re-sorted with build_renames.py's key. Its `set` must give our_attr, transform, upstream and
reason. Idempotent: re-running on its own output changes nothing.
"""
import json, os

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
RJ = os.path.join(ROOT, "reference", "_gen", "renames.json")
OV = os.path.join(ROOT, "reference", "_gen", "pass5", "renames_overrides.json")
r = json.load(open(RJ, encoding="utf-8"))
ov = json.load(open(OV, encoding="utf-8"))

r["shape_mappings"] = ov.get("shape_mappings", {})
bad = []
for p in ov.get("rows", []):
    key = (p["type"], p.get("kreuzwerker_attr"), p.get("previous_attr", "*"))
    hits = [x for x in r["renames"] if x["type"] == key[0] and x["kreuzwerker_attr"] == key[1]
            and (key[2] == "*" or x["previous_attr"] == key[2])]
    if p.get("add") and not hits:
        row = {"type": p["type"], "kreuzwerker_attr": p.get("kreuzwerker_attr"), "previous_attr": p.get("previous_attr")}
        missing = [k for k in ("our_attr", "transform", "upstream", "reason") if k not in p["set"]]
        if missing or key[2] == "*":
            bad.append("%s: an added row needs previous_attr and set.%s" % (key, ", set.".join(missing) or "previous_attr"))
            continue
        row.update({k: p["set"][k] for k in ("our_attr", "transform", "upstream", "reason")})
        row.update({k: v for k, v in p["set"].items() if k not in row})
        r["renames"].append(row)
        continue
    if len(hits) != 1:
        bad.append("%s: %d rows match" % (key, len(hits)))
        continue
    hits[0].update(p["set"])
if bad:
    raise SystemExit("renames_post: " + "; ".join(bad))
# build_renames.py's order.
r["renames"].sort(key=lambda e: (e["type"], e["kreuzwerker_attr"] or e["previous_attr"] or "", e["previous_attr"] or "", e["our_attr"] or ""))
with open(RJ, "w", encoding="utf-8", newline="\n") as f:
    json.dump(r, f, indent=2, ensure_ascii=False)
    f.write("\n")
print("renames_post: %d row patches, %d shape mappings" % (len(ov.get("rows", [])), len(r["shape_mappings"])))
