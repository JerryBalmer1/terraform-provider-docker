"""Pass 6b Lane 0: add the `pass6b` section to reference/_gen/pass5/ownership.json (the lane that owns each file during
Pass 6b; exactly one lane per file; lanes edit only their own files), and check a lane's changed paths against it.

    python -I -B reference/_gen/generators/pass6b/p6b_ownership.py                      (from the repo root)
    python -I -B reference/_gen/generators/pass6b/p6b_ownership.py --check <lane> <file of changed paths>
    python -I -B reference/_gen/generators/pass6b/p6b_ownership.py --owner <path>...

Lanes: E example tests and manual tasks (test/TfDockerTest, examples/ incl. <name>.tests.ps1, test/__manual, the root
build script, build/examples.tasks.ps1, tools/deferred and reference/_gen/deferred.d for the example allowlist);
W pairwise combination tests (tools/pairwise, reference/_gen/pairwise, build/pairwise.tasks.ps1, and the
docker_container / docker_service / docker_network / docker_volume provider files so findings can be fixed in place);
V skip audit and exec stdin (reference/_gen/fieldmap.d, the exec action and ephemeral resource, preflight / client /
provider.go, the compose push and run actions for the re-pin gaps). 0 is Lane 0, frozen while the lanes run: the
re-pinned reference/upstream and SOURCES.md, the dind harness (Invoke-E2E.ps1 -Script, the pinned compose binary),
the compose parity / reference files whose citations Lane 0 updated. Z is integration: generated files, every other
provider file, the inventory fragments, CI, prerelease/release task files, project docs, BUILD_REPORT and CHANGELOG.
`files` assigns every file that exists now; a new file belongs to the first `rules` entry its path matches; anything
unmatched is Z. --check reads ownership.json only, so the JSON is the single source of truth while lanes run.
Earlier sections stay as history.
"""
import fnmatch, json, os, subprocess, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
OWN = os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json")
SECTION = "pass6b"
P = "internal/provider/"
G = "reference/_gen/pass6b/"

RULES = [
    # Z first for paths other lanes' globs would otherwise catch: generated output and integration files.
    ("Z", [P + "registry_gen.go", P + "registry_gen_test.go", P + "contract_gen.go", P + "z_*.go",
           "docs/*", "README_resources.md", ".claude/skills/*", "reference/inventory.json", "reference/INVENTORY.md",
           "reference/_gen/fieldmap.json", "reference/_gen/renames.json", "reference/_gen/deferred.json",
           "BUILD_REPORT.md", "CHANGELOG.md", "CLAUDE.md", "README.md", ".github/*", ".gitignore",
           G + "commit-groups/*", G + "lane-Z.*", G + "open-findings.md", "reference/_gen/pass5/ownership.json",
           "reference/_gen/generators/pass6b/p6b_commit_groups.py", "reference/_gen/fragments/*",
           "reference/_gen/merge.py", "templates/*", "build/prerelease.tasks.ps1", "build/release.tasks.ps1",
           "tools/climap/*", "tools/parity/*"]),
    # 0: Lane 0's re-pin, harness and parity-citation files (frozen during the lanes).
    ("0", ["reference/upstream/*", "reference/SOURCES.md", "reference/CONTRACT.md", "test/harness/*",
           P + "compose_parity_acc_c_test.go", P + "c_compose_refs.go", P + "compose_refs_c_test.go",
           P + "compose_local_apply.go", "reference/_gen/generators/pass6b/p6b_ownership.py", G + "lane-0.*"]),
    # E: example tests, manual tasks, the root build script, the example allowlist in deferred.
    ("E", ["test/TfDockerTest/*", "test/__manual/*", "examples/*", "terraform-provider-docker.build.ps1",
           "build/examples.tasks.ps1", "tools/deferred/*", "reference/_gen/deferred.d/*", G + "lane-E.*"]),
    # W: pairwise generator and models, its task, and the four resource families it tests.
    ("W", ["tools/pairwise/*", "reference/_gen/pairwise/*", "build/pairwise.tasks.ps1", P + "container_*",
           P + "ctr_*", P + "service_*", P + "network_*", P + "volume_*", P + "*_p6b_w*", G + "lane-W.*"]),
    # V: skip audit (fieldmap.d), exec stdin, preflight follow-up, the compose push / run re-pin gaps.
    ("V", ["reference/_gen/fieldmap.d/*", P + "action_exec*", P + "cop_exec*", P + "ephemeral_exec_output*",
           P + "preflight*", P + "client.go", P + "provider.go", P + "api_log*", P + "action_compose_images*",
           P + "action_compose_run*", P + "*_p6b_v*", G + "lane-V.*"]),
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
    "about": "Pass 6b file ownership (lanes E example tests / manual tasks, W pairwise tests, V skip audit / exec "
             "stdin; 0 = Lane 0's re-pin, harness and parity-citation files, frozen during the lanes; Z = integration "
             "and generated files). Each lane edits only files it owns; `files` lists every existing file, a new file "
             "belongs to the first `rules` entry it matches, anything unmatched is Z. A cross-lane need is a TODO line "
             "with the exact change in reference/_gen/pass6b/lane-<X>.md, never an edit. Check before handing over: "
             "p6b_ownership.py --check <lane> <listing>.",
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
