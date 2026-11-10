"""Pass 6a Lane Z: write reference/_gen/pass6a/commit-groups/<group>.txt, one path per line, from the staged changes.

    python -I -B reference/_gen/generators/pass6a/p6a_commit_groups.py <lane0.patch>

Groups: 0 (Lane 0), G, P, F (the lanes), Z (integration). Every staged path is in exactly one group: the lane whose
reference/_gen/pass6a/lane-<X>.files lists it; else 0 when the Lane 0 patch touched it; else Z. A path Lane Z edited
after a lane (or Lane 0) handed it over stays in that first group, so the groups taken in the order 0, G, P, F, Z
together give the staged tree; only that final state is required to build. The script fails when a path would land
in two groups or the groups do not cover the staged changes exactly.
"""
import os, subprocess, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
OUT = os.path.join(ROOT, "reference", "_gen", "pass6a", "commit-groups")
LANES = ["G", "P", "F"]


def git(*a):
    return subprocess.run(["git", "-C", ROOT] + list(a), capture_output=True, text=True, check=True).stdout


staged = [p for p in git("diff", "--cached", "--name-only", "HEAD").split("\n") if p]
lane0 = set()
for line in git("apply", "--numstat", sys.argv[1]).split("\n"):
    if line:
        lane0.add(line.split("\t")[2])
owner = {}
for l in LANES:
    lst = os.path.join(ROOT, "reference", "_gen", "pass6a", "lane-%s.files" % l)
    for p in open(lst, encoding="utf-8").read().split():
        if p in owner:
            raise SystemExit("%s is listed by lanes %s and %s" % (p, owner[p], l))
        owner[p] = l
groups = {g: [] for g in ["0"] + LANES + ["Z"]}
for p in staged:
    g = owner.get(p) or ("0" if p in lane0 else "Z")
    groups[g].append(p)
missing = [p for p in owner if p not in staged]
if missing:
    print("note: listed by a lane but not staged (no net change):", ", ".join(missing))
os.makedirs(OUT, exist_ok=True)
for g, paths in groups.items():
    with open(os.path.join(OUT, g + ".txt"), "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(sorted(paths)) + ("\n" if paths else ""))
flat = [p for ps in groups.values() for p in ps]
assert len(flat) == len(set(flat)) == len(staged), "groups do not partition the staged paths"
print("commit groups:", {g: len(ps) for g, ps in groups.items()}, "total", len(flat))
