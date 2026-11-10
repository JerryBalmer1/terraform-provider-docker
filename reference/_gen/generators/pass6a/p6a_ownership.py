"""Pass 6a Lane 0: add the `pass6a` section to reference/_gen/pass5/ownership.json (the lane that owns each file during
Pass 6a; exactly one lane per file; lanes edit only their own files), and check a lane's changed paths against it.

    python -I -B reference/_gen/generators/pass6a/p6a_ownership.py                      (from the repo root)
    python -I -B reference/_gen/generators/pass6a/p6a_ownership.py --check <lane> <file of changed paths>
    python -I -B reference/_gen/generators/pass6a/p6a_ownership.py --owner <path>...

Lanes: G release pipeline (goreleaser, release.yml, the Release task, RELEASING.md), P PreRelease and drift (the
DriftCheck / PreRelease tasks, drift.yml, the drift tool, fieldcov and apiwatch when they need a spec-path option),
F preflight and harness (provider Configure diagnostics, every provider source and test, the dind harness, the
hand-maintained acceptance-test deferrals). 0 is Lane 0: the deferred tool and the build script, frozen while the
lanes run (a lane that needs a change there logs a TODO). Z is integration: generated files, fieldmap.d, the
inventory fragments, CI, project docs, BUILD_REPORT and CHANGELOG. `files` assigns every file that exists now; a new
file belongs to the first `rules` entry its path matches; anything unmatched is Z. --check reads ownership.json
only, so the JSON is the single source of truth while lanes run. Earlier sections stay as history.
"""
import fnmatch, json, os, subprocess, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
OWN = os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json")
SECTION = "pass6a"
P = "internal/provider/"
G = "reference/_gen/pass6a/"

RULES = [
    # Z first for paths other lanes' globs would otherwise catch: generated output and integration files.
    ("Z", [P + "registry_gen.go", P + "registry_gen_test.go", P + "contract_gen.go", P + "z_*.go",
           "docs/*", "README_resources.md", ".claude/skills/terraform-provider-docker/*",
           ".claude/skills/docker-cli-map/*", "reference/inventory.json", "reference/INVENTORY.md",
           "reference/_gen/fieldmap.json", "reference/_gen/fieldmap.d/*", "reference/_gen/renames.json",
           "reference/_gen/deferred.json", "BUILD_REPORT.md", "CHANGELOG.md", "CLAUDE.md", "README.md",
           ".github/workflows/ci.yml", G + "commit-groups/*", G + "lane-Z.*", "reference/_gen/pass5/ownership.json",
           "reference/_gen/generators/pass6a/p6a_commit_groups.py", "reference/_gen/fragments/*",
           "reference/_gen/merge.py", "templates/guides/migrating-from-kreuzwerker.md.tmpl"]),
    # 0: Lane 0's deferred tool and the build script (frozen during the lanes).
    ("0", ["tools/deferred/*", "terraform-provider-docker.build.ps1", "reference/_gen/generators/pass6a/p6a_ownership.py",
           G + "lane-0.*"]),
    # G: release pipeline.
    ("G", [".goreleaser.yml", ".github/workflows/release.yml", "build/release.tasks.ps1", "RELEASING.md",
           "terraform-registry-manifest.json", G + "lane-G.*"]),
    # P: PreRelease, upstream drift.
    ("P", ["build/prerelease.tasks.ps1", "tools/drift/*", "tools/fieldcov/*", "tools/apiwatch/*",
           ".github/workflows/drift.yml", "reference/_gen/drift-accepted.json", G + "lane-P.*"]),
    # F: provider Configure preflight, the provider sources and tests, the dind harness, acceptance deferrals.
    ("F", [P + "*", "test/harness/*", "reference/_gen/deferred.d/*", "examples/provider/*", G + "lane-F.*"]),
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
    sec = load()[SECTION]
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
d[SECTION] = {
    "about": "Pass 6a file ownership (lanes G release, P prerelease/drift, F preflight/harness; 0 = Lane 0's deferred "
             "tool and the build script, frozen during the lanes; Z = integration and generated files). Each lane "
             "edits only files it owns; `files` lists every existing file, a new file belongs to the first `rules` "
             "entry it matches, anything unmatched is Z. Build tasks: Lane G writes build/release.tasks.ps1, Lane P "
             "build/prerelease.tasks.ps1; the build script dot-sources both. A cross-lane need is a TODO line with "
             "the exact change in reference/_gen/pass6a/lane-<X>.md, never an edit. Check before handing over: "
             "p6a_ownership.py --check <lane> <listing>.",
    "rules": [{"lane": l, "patterns": p} for l, p in RULES],
    "files": files,
}
with open(OWN, "w", encoding="utf-8", newline="\n") as f:
    json.dump(d, f, indent=2)
    f.write("\n")
cnt = {}
for v in files.values():
    cnt[v] = cnt.get(v, 0) + 1
print("%s files" % SECTION, len(files), dict(sorted(cnt.items())))
