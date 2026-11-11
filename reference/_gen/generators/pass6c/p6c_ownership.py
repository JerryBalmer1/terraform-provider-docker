"""Pass 6c Lane 0: add the `pass6c` section to reference/_gen/pass5/ownership.json (the lane that owns each file during
Pass 6c; exactly one lane per file; lanes edit only their own files), and check a lane's changed paths against it.

    python -I -B reference/_gen/generators/pass6c/p6c_ownership.py                      (from the repo root)
    python -I -B reference/_gen/generators/pass6c/p6c_ownership.py --check <lane> <file of changed paths>
    python -I -B reference/_gen/generators/pass6c/p6c_ownership.py --owner <path>...

Lanes: S structured output fidelity (the contract generator, contract*.go and every *_contract* file, r_rawjson, the
outputs fragments, CONTRACT.md); C Docker Build Cloud (buildx builder, BuildKit seam, image build, provider auth, the
buildx_builder example, the climap list, deferred.d acceptance/examples allowlists); F open findings and loose ends
(test harness, TfDockerTest, manual scenarios, the root build script and task files, tools/pairwise, tools/drift,
tools/deferred, docker_service, docker_exec, preflight / client / ssh, the compose files citing v5.1.4, every other
example); R README and registry docs (README.md, templates, tools/doccounts, examples/provider). 0 is Lane 0, frozen
while the lanes run. Z is integration: generated files, every other file, BUILD_REPORT and CHANGELOG.
`files` assigns every file that exists now; a new file belongs to the first `rules` entry its path matches; anything
unmatched is Z. --check reads ownership.json only. Earlier sections stay as history.
"""
import fnmatch, json, os, subprocess, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
OWN = os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json")
SECTION = "pass6c"
P = "internal/provider/"
G = "reference/_gen/pass6c/"

RULES = [
    # Z first: generated output and integration files other lanes' globs would otherwise catch.
    ("Z", [P + "registry_gen.go", P + "registry_gen_test.go", "docs/*", "README_resources.md", ".claude/skills/*",
           "reference/inventory.json", "reference/INVENTORY.md", "reference/_gen/fieldmap.json",
           "reference/_gen/renames.json", "reference/_gen/deferred.json", "BUILD_REPORT.md", "CHANGELOG.md",
           "CLAUDE.md", ".github/*", ".gitignore", G + "commit-groups/*", G + "lane-Z.*", G + "open-findings.md",
           "reference/_gen/pass6b/open-findings.md", "reference/_gen/pass5/ownership.json",
           "reference/_gen/generators/pass6c/p6c_commit_groups.py", "reference/_gen/merge.py"]),
    # 0: Lane 0 (frozen during the lanes).
    ("0", ["reference/_gen/generators/pass6c/p6c_ownership.py", G + "lane-0.*", "test/harness/.gitignore"]),
    # S: structured output fidelity.
    ("S", ["tools/contractgen/*", P + "contract*", P + "*_contract*", P + "r_rawjson*", P + "*_p6c_s*",
           "reference/_gen/fragments/outputs/*", "reference/CONTRACT.md", "reference/_gen/SCHEMA.md", G + "lane-S.*"]),
    # C: Docker Build Cloud.
    ("C", [P + "buildx_builder*", P + "buildkit_*", P + "image_build*", P + "img_build*", P + "*cloud*",
           P + "provider_auth*", P + "*_p6c_c*", "examples/resources/docker_buildx_builder/*",
           "reference/_gen/deferred.d/acceptance.json", "reference/_gen/deferred.d/examples.json",
           "tools/climap/*", G + "lane-C.*"]),
    # F: open findings and loose ends.
    ("F", ["test/harness/*", "test/TfDockerTest/*", "test/__manual/*", "terraform-provider-docker.build.ps1",
           "build/*", "tools/pairwise/*", "reference/_gen/pairwise/*", "tools/drift/*", "tools/deferred/*",
           "reference/_gen/deferred.d/*", P + "service_*", P + "action_exec*", P + "cop_exec*", P + "exec_stdin*",
           P + "ephemeral_exec_output*", P + "preflight*", P + "client.go", P + "provider_ssh*",
           P + "compose_hooks.go", P + "compose_local.go", P + "compose_local_k.go", P + "compose_build.go",
           P + "compose_publish_k.go", P + "pass53_partial_k.go", P + "action_compose_images.go",
           P + "z_compose_endpoint_test.go", P + "*_p6c_f*", G + "lane-F.*"]),
    # R: README and registry docs.
    ("R", ["README.md", "templates/*", "tools/doccounts/*", "examples/provider/*", G + "lane-R.*"]),
    # F also owns every example not claimed above (docker_exec stdin_path, the global-service example).
    ("F", ["examples/*"]),
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
    "about": "Pass 6c file ownership (lanes S structured output fidelity, C Docker Build Cloud, F open findings and "
             "loose ends, R README and registry docs; 0 = Lane 0, frozen during the lanes; Z = integration and "
             "generated files). Each lane edits only files it owns; `files` lists every existing file, a new file "
             "belongs to the first `rules` entry it matches, anything unmatched is Z. A cross-lane need is a TODO line "
             "with the exact change in reference/_gen/pass6c/lane-<X>.md, never an edit. Check before handing over: "
             "p6c_ownership.py --check <lane> <listing>.",
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
