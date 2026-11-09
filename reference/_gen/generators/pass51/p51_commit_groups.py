"""Pass 5.1 Lane Z: split every staged path into commit groups H, P, D, R, S, Z (one listing per group in
reference/_gen/pass51/commit-groups/), by the `pass51` section of reference/_gen/pass5/ownership.json.

    python -I -B reference/_gen/generators/pass51/p51_commit_groups.py      (from the repo root, after `git add -A`)

Every staged path lands in exactly one group (a path with no rule is Z); the script fails otherwise, and prints the
coverage proof: staged count == sum of the group counts, no path in two groups. A group is the final content of its
files. The groups are meant to be committed in order H, P, D, R, S, Z; only the final state (all groups) is
guaranteed to build, because a lane's tests can depend on another lane's code from the same pass.
"""
import fnmatch, json, os, subprocess

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
sec = json.load(open(os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json"), encoding="utf-8"))["pass51"]
staged = [s for s in subprocess.run(["git", "-C", ROOT, "diff", "--cached", "--name-only", "HEAD"], capture_output=True,
                                    text=True, check=True).stdout.split("\n") if s]


def owner(path):
    if path.startswith("reference/_gen/pass51/commit-groups/"):
        return "Z"
    if path in sec["files"]:
        return sec["files"][path]
    for r in sec["rules"]:
        if any(fnmatch.fnmatchcase(path, p) for p in r["patterns"]):
            return r["lane"]
    return "Z"


ORDER = ["H", "P", "D", "R", "S", "Z"]
groups = {g: [] for g in ORDER}
for p in staged:
    groups[owner(p)].append(p)
out = os.path.join(ROOT, "reference", "_gen", "pass51", "commit-groups")
os.makedirs(out, exist_ok=True)
for f in os.listdir(out):
    os.remove(os.path.join(out, f))
for g in ORDER:
    if groups[g]:
        with open(os.path.join(out, g + ".txt"), "w", encoding="utf-8", newline="\n") as f:
            f.write("\n".join(sorted(groups[g])) + "\n")
flat = [p for g in ORDER for p in groups[g]]
assert len(flat) == len(set(flat)) == len(staged), "coverage: a path is missing or in two groups"
print("staged", len(staged), "=", " + ".join("%s %d" % (g, len(groups[g])) for g in ORDER if groups[g]))
