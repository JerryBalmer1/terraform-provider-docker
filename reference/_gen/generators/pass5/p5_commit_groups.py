"""Pass 5 Lane Z: split every staged path into commit groups (one file per group in reference/_gen/pass5/commit-groups/).

    python -I -B reference/_gen/generators/pass5/p5_commit_groups.py      (from the repo root, after `git add -A`)

Groups: 0-decisions-scaffold (Lane 0: decisions, inventory sources, generators, shared helpers, registration, build
file), A..F (each lane's owned files per reference/_gen/pass5/ownership.json; F also tools/parity and the migration
guide template), Z-docs-inventory (generated docs / skills / inventory, BUILD_REPORT, and the Lane Z integration fixes
in tools/climap, tools/compose2stack, tools/genskill). Every staged path lands in exactly one group; the script fails
otherwise. A group is the final content of its files: Lane 0 edits a lane later changed belong to that lane's group, so
the groups are meant to be committed in order 0, A..F, Z and only the last commit is guaranteed to build.
"""
import fnmatch, json, os, subprocess

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
own = json.load(open(os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json"), encoding="utf-8"))
staged = subprocess.run(["git", "-C", ROOT, "diff", "--cached", "--name-only", "HEAD"], capture_output=True, text=True,
                        check=True).stdout.split("\n")
staged = [s for s in staged if s]

Z_PREFIX = ("docs/", ".claude/", "tools/climap/", "tools/compose2stack/", "tools/genskill/")
Z_FILES = {"README_resources.md", "reference/inventory.json", "reference/INVENTORY.md", "BUILD_REPORT.md",
           "reference/_gen/pass5/renames_overrides.json", "reference/_gen/generators/pass5/p5_gap_flips.py",
           "reference/_gen/generators/pass5/p5_lanez.py", "reference/_gen/generators/pass5/p5_commit_groups.py"}


def owner(path):
    # Lane 0 removed these (registration became generated; data.docker_logs was renamed).
    if fnmatch.fnmatchcase(path, "internal/provider/register_*.go") or path.startswith("examples/data-sources/docker_logs/"):
        return "0"
    if path.startswith(("tools/parity/", "templates/guides/")):
        return "F"
    if path in Z_FILES or path.startswith(Z_PREFIX) or path.startswith("reference/_gen/pass5/commit-groups/"):
        return "Z"
    if path.startswith(("internal/provider/", "examples/", "test/")):
        for prefix, lane in sorted(own["examples"].items(), key=lambda x: -len(x[0])):
            if path.startswith(prefix):
                return lane
        for r in own["rules"]:
            if any(fnmatch.fnmatchcase(path, p) for p in r["patterns"]):
                return r["lane"]
        raise SystemExit("unowned: " + path)
    return "0"


NAMES = {"0": "0-decisions-scaffold.txt", "Z": "Z-docs-inventory.txt"}
groups = {}
for p in staged:
    groups.setdefault(owner(p), []).append(p)
out = os.path.join(ROOT, "reference", "_gen", "pass5", "commit-groups")
os.makedirs(out, exist_ok=True)
for f in os.listdir(out):
    os.remove(os.path.join(out, f))
for g, paths in groups.items():
    with open(os.path.join(out, NAMES.get(g, g + ".txt")), "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(sorted(paths)) + "\n")
print("staged", len(staged), {NAMES.get(g, g + ".txt"): len(v) for g, v in sorted(groups.items())})
