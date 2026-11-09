"""Pass 5: apply reference/_gen/pass5/renames_overrides.json to reference/_gen/renames.json.

    python -I -B reference/_gen/generators/pass5/build_renames.py <kreuzwerker clone>   # base table
    python -I -B reference/_gen/generators/pass5/renames_post.py                        # decisions + lane results

renames_overrides.json holds what came after the audit: user decisions on shapes (shape_mappings, per-row
`set` patches) and the gap rows the lanes implemented (Lane Z). Each row patch is matched by
(type, kreuzwerker_attr, previous_attr); every patch must match exactly one row, or the script fails.
Idempotent: re-running on its own output changes nothing.
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
    if len(hits) != 1:
        bad.append("%s: %d rows match" % (key, len(hits)))
        continue
    hits[0].update(p["set"])
if bad:
    raise SystemExit("renames_post: " + "; ".join(bad))
with open(RJ, "w", encoding="utf-8", newline="\n") as f:
    json.dump(r, f, indent=2, ensure_ascii=False)
    f.write("\n")
print("renames_post: %d row patches, %d shape mappings" % (len(ov.get("rows", [])), len(r["shape_mappings"])))
