"""Pass 5.2 Lane 0: add the `pass52` section to reference/_gen/pass5/ownership.json (the lane that owns each file during
Pass 5.2; exactly one lane per file; lanes edit only their own files), and check a lane's changed paths against it.

    python -I -B reference/_gen/generators/pass52/p52_ownership.py                      (from the repo root)
    python -I -B reference/_gen/generators/pass52/p52_ownership.py --check <lane> <file of changed paths>
    python -I -B reference/_gen/generators/pass52/p52_ownership.py --owner <path>...

Lanes: C compose (the four ComposeParity findings), D Dockerfile round-trip honesty, W Windows verification of the
docker_plugin_create tar fix (new w_* test files only; obj_plugin_actions.go is Z's, W writes a patch instead),
M docs (CLAUDE.md, CHANGELOG.md, release-notes templates), Z integration (generated files, harness, BUILD_REPORT, the
rest). `files` assigns every file that exists now; a new file belongs to the first `rules` entry its path matches;
anything unmatched is Z. --check reads ownership.json only, so the JSON is the single source of truth while lanes run.
The Pass 5 and Pass 5.1 sections are left as they were (history).
"""
import fnmatch, json, os, subprocess, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
OWN = os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json")
P = "internal/provider/"
G = "reference/_gen/pass52/"

RULES = [
    # Z first for the paths other lanes' globs would otherwise catch.
    ("Z", [P + "registry_gen.go", P + "registry_gen_test.go", P + "contract_gen.go", P + "obj_plugin_actions.go",
           G + "commit-groups/*", G + "lane-Z.*", "reference/_gen/pass5/ownership.json",
           "reference/_gen/generators/pass52/*"]),
    # W: new w_* test files and the patch it may hand to Z.
    ("W", [P + "w_*.go", G + "plugin-fix.patch", G + "lane-W.*"]),
    # C: compose provider files, the ComposeParity test + fixtures + allowlist, the hash-compat test, CONTRACT.md.
    ("C", [P + "compose_*.go", P + "action_compose_*.go", P + "datasource_compose_*.go", P + "function_compose_env*.go",
           P + "c_*.go", P + "*_c_test.go", P + "p_compose_parity*.go", P + "testdata/compose*",
           "test/fixtures/compose/*", "reference/CONTRACT.md", G + "lane-C.*"]),
    # D: data.docker_dockerfile and the round-trip allowlist.
    ("D", [P + "dockerfile_*.go", P + "datasource_dockerfile*.go", P + "d_*.go", P + "*_d_test.go",
           P + "testdata/dockerfile*", G + "lane-D.*"]),
    # M: project docs and release notes.
    ("M", ["CLAUDE.md", "CHANGELOG.md", "templates/release*", "RELEASE_NOTES*", ".github/release*", G + "lane-M.*"]),
]


def rule_owner(path):
    for lane, pats in RULES:
        if any(fnmatch.fnmatchcase(path, p) for p in pats):
            return lane
    return "Z"


def owner_from_json(sec, path):
    path = path.replace("\\", "/")
    if path in sec["files"]:
        return sec["files"][path]
    for r in sec["rules"]:
        if any(fnmatch.fnmatchcase(path, p) for p in r["patterns"]):
            return r["lane"]
    return "Z"


def load():
    return json.load(open(OWN, encoding="utf-8"))


if len(sys.argv) >= 2 and sys.argv[1] in ("--check", "--owner"):
    sec = load()["pass52"]
    if sys.argv[1] == "--owner":
        for p in sys.argv[2:]:
            print(owner_from_json(sec, p), p)
        raise SystemExit(0)
    lane, listing = sys.argv[2], sys.argv[3]
    wrong = ["%s (owner %s)" % (f, o) for f in open(listing, encoding="utf-8").read().split()
             for o in [owner_from_json(sec, f)] if o != lane]
    print("\n".join(wrong) if wrong else "ownership ok: lane %s" % lane)
    raise SystemExit(1 if wrong else 0)

listed = subprocess.run(["git", "-C", ROOT, "ls-files", "-co", "--exclude-standard"], capture_output=True, text=True,
                        check=True).stdout.split("\n")
files = {f: rule_owner(f) for f in sorted(set(listed)) if f and os.path.exists(os.path.join(ROOT, f))}
d = load()
d["pass52"] = {
    "about": "Pass 5.2 file ownership (lanes C, D, W, M; Z = integration and generated files). Each lane edits only "
             "files it owns; `files` lists every existing file, a new file belongs to the first `rules` entry it "
             "matches (prefer your lane's prefix: c_/d_/w_*.go), anything unmatched is Z. A cross-lane need is a "
             "TODO line with the exact change in reference/_gen/pass52/lane-<X>.md, never an edit. Check before "
             "staging: p52_ownership.py --check <lane> <listing>.",
    "rules": [{"lane": l, "patterns": p} for l, p in RULES],
    "files": files,
}
with open(OWN, "w", encoding="utf-8", newline="\n") as f:
    json.dump(d, f, indent=2)
    f.write("\n")
cnt = {}
for v in files.values():
    cnt[v] = cnt.get(v, 0) + 1
print("pass52 files", len(files), dict(sorted(cnt.items())))
