"""Prints counts for reference/_gen/renames.json (Pass 5 rename table).

Usage: python -I reference/_gen/generators/pass5/renames_summary.py [renames.json]

Categories per entry: dropped (our_attr null); renamed (our_attr != kreuzwerker_attr, or previous_attr !=
our_attr); identity (everything else). Python 3 stdlib only.
"""
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT = os.path.join(HERE, "..", "..", "renames.json")


def category(e):
    our, kw, prev = e["our_attr"], e["kreuzwerker_attr"], e["previous_attr"]
    if our is None:
        return "dropped"
    if (kw is not None and our != kw) or (prev is not None and prev != our):
        return "renamed"
    return "identity"


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else DEFAULT
    with open(path, encoding="utf-8") as f:
        doc = json.load(f)
    entries = doc["renames"]
    total = {"identity": 0, "renamed": 0, "dropped": 0}
    per = {}
    for e in entries:
        c = category(e)
        total[c] += 1
        t = per.setdefault(e["type"], {"identity": 0, "renamed": 0, "dropped": 0, "ours_changed": 0})
        t[c] += 1
        if e["previous_attr"] is not None and e["previous_attr"] != e["our_attr"]:
            t["ours_changed"] += 1
    print("entries: %d" % len(entries))
    print("identity: %d" % total["identity"])
    print("renamed: %d" % total["renamed"])
    print("dropped: %d" % total["dropped"])
    print("type renames: %d" % len(doc.get("type_renames", [])))
    print("from kreuzwerker: %d, ours only (kreuzwerker_attr null): %d"
          % (sum(1 for e in entries if e["kreuzwerker_attr"] is not None),
             sum(1 for e in entries if e["kreuzwerker_attr"] is None)))
    print()
    print("%-42s %8s %8s %8s %13s" % ("type", "identity", "renamed", "dropped", "ours_changed"))
    for t in sorted(per):
        c = per[t]
        print("%-42s %8d %8d %8d %13d" % (t, c["identity"], c["renamed"], c["dropped"], c["ours_changed"]))


if __name__ == "__main__":
    main()
