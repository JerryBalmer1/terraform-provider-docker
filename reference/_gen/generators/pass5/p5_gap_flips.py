"""Pass 5 Lane Z: turn the lane reports' "Gap rows implemented" sections into row patches in
reference/_gen/pass5/renames_overrides.json (then run renames_post.py).

    python -I -B reference/_gen/generators/pass5/p5_gap_flips.py <dir with lane-<X>-report.md>

A report line is `[- ][<type> | ]<kreuzwerker_attr> -> <our_attr>[ (note)]`; a `<type> | kreuzwerker -> ours:` header sets
the type for the lines below it. Each line is matched to exactly one open gap row (our_attr null, reason "gap:");
lines that match nothing and gap rows nobody reported are printed. Existing gap patches are replaced, so the script can
be re-run.
"""
import json, os, re, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
RJ = os.path.join(ROOT, "reference", "_gen", "renames.json")
OV = os.path.join(ROOT, "reference", "_gen", "pass5", "renames_overrides.json")
rows = json.load(open(RJ, encoding="utf-8"))["renames"]
gaps = [r for r in rows if r["our_attr"] is None and (r["reason"] or "").startswith("gap")]
types = {r["type"] for r in gaps}
ov = json.load(open(OV, encoding="utf-8"))
ov["rows"] = [p for p in ov["rows"] if not p.get("gap_flip")]

LINE = re.compile(r"^\s*[-*]?\s*`?(?:(?P<type>[a-z.]*docker_[a-z_]+)\s*\|\s*)?(?P<kw>[a-z0-9_.*]+)\s*->\s*(?P<ours>[a-z0-9_.*]+)`?(?P<note>.*)$")
HEAD = re.compile(r"^\s*`?(?P<type>[a-z.]*docker_[a-z_]+)\s*\|\s*kreuzwerker\s*->\s*ours")
found, unmatched = {}, []
for lane in "ABCE":
    p = os.path.join(sys.argv[1], "lane-%s-report.md" % lane)
    cur_type, on = None, False
    for line in open(p, encoding="utf-8"):
        if line.startswith("#"):
            on = "gap rows implemented" in line.lower() or (line.lower().strip() == "## gap rows")
            cur_type = None
            continue
        if not on:
            continue
        h = HEAD.match(line)
        if h:
            cur_type = h.group("type")
            continue
        line = line.replace("Implemented: ", "")
        # Shorthand "a.x / y / z -> b.x / y / z": expand into one line per leaf.
        sm = re.match(r"^(\s*[-*]?\s*)([a-z0-9_.]+)\.([a-z0-9_]+(?:\s*/\s*[a-z0-9_]+)+)\s*->\s*([a-z0-9_.]+)\.([a-z0-9_]+(?:\s*/\s*[a-z0-9_]+)+)\s*$", line)
        if sm:
            for a, b in zip(re.split(r"\s*/\s*", sm.group(3)), re.split(r"\s*/\s*", sm.group(5))):
                kw, ours = sm.group(2) + "." + a, sm.group(4) + "." + b
                cand = [g for g in gaps if g["kreuzwerker_attr"] == kw and (cur_type is None or g["type"] == cur_type)]
                if len(cand) == 1:
                    found[(cand[0]["type"], kw)] = (lane, ours, "")
                else:
                    unmatched.append("%s: %s" % (lane, kw))
            continue
        m = LINE.match(line)
        if not m:
            continue
        t = m.group("type") or cur_type
        cand = [g for g in gaps if g["kreuzwerker_attr"] == m.group("kw") and (t is None or g["type"] == t)]
        if len(cand) != 1:
            unmatched.append("%s: %s" % (lane, line.strip()))
            continue
        g = cand[0]
        found[(g["type"], g["kreuzwerker_attr"])] = (lane, m.group("ours"), m.group("note").strip(" ()"))

for (t, kw), (lane, ours, note) in sorted(found.items()):
    ov["rows"].append({"type": t, "kreuzwerker_attr": kw, "gap_flip": True,
                       "set": {"our_attr": ours, "transform": "added (gap, Pass 5)" + (": " + note if note else ""),
                               "reason": "Pass 5 Lane %s: implemented under the upstream name." % lane}})
with open(OV, "w", encoding="utf-8", newline="\n") as f:
    json.dump(ov, f, indent=2, ensure_ascii=False)
    f.write("\n")
open_gaps = [g for g in gaps if (g["type"], g["kreuzwerker_attr"]) not in found]
print("gap rows: %d, flipped: %d, still open: %d, unmatched report lines: %d" % (len(gaps), len(found), len(open_gaps), len(unmatched)))
for g in open_gaps:
    print("OPEN", g["type"], g["kreuzwerker_attr"], g["reason"])
for u in unmatched:
    print("UNMATCHED", u)
