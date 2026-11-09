"""Pass 5.1 Lane Z: add the `pass51` section to reference/_gen/pass5/ownership.json (the lane that owns each file during
Pass 5.1; exactly one lane per file; lanes edit only their own files), and check a lane's changed paths against it.

    python -I -B reference/_gen/generators/pass51/p51_ownership.py                      (from the repo root)
    python -I -B reference/_gen/generators/pass51/p51_ownership.py --check <lane> <file of changed paths>
    python -I -B reference/_gen/generators/pass51/p51_ownership.py --owner <path>...

Lanes: H harness coverage, P ComposeParity coverage, D Dockerfile round-trips, R raw-output fidelity + records,
S honest status, Z integration (generated files, docs, examples, BUILD_REPORT, the rest).
`files` assigns every file that exists now (tracked or untracked); a new file belongs to the first `rules` entry its
path matches. The Pass 5 `rules` / `examples` / `files` sections are left as they were (history).
--check reads ownership.json only (never these rules), so the JSON is the single source of truth while lanes run.
"""
import fnmatch, json, os, subprocess, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
OWN = os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json")
P = "internal/provider/"

# Acceptance test files Lane H may retarget (local registry images, worker / plugin / CSI fixtures). Every
# internal/provider *_test.go holding a `func TestAcc`, except the two owned by Lanes P (ComposeParity) and R's
# contract tests; computed below, listed in `files`.
H_EXTRA = [P + "harness_acc_p3_test.go", P + "e2e_test.go"]

RULES = [
    # Z: generated files and integration-only paths.
    ("Z", [P + "registry_gen.go", P + "registry_gen_test.go", P + "contract_gen.go", "docs/*", ".claude/*",
           "README_resources.md", "BUILD_REPORT.md", "CLAUDE.md", "examples/*", "tools/genskill/*", "tools/climap/*",
           "tools/compose2stack/*", "tools/registrygen/*", "tools/contractgen/*", "reference/_gen/pass51/commit-groups/*",
           "reference/_gen/pass5/ownership.json", "reference/_gen/generators/pass51/p51_*.py",
           "reference/_gen/pass51/lane-Z.*"]),
    # P: ComposeParity test, its fixtures, the harness request file.
    ("P", [P + "compose_parity_acc_c_test.go", P + "compose_hash_compat_test.go", P + "testdata/compose_hash/*",
           P + "p_*.go", "test/fixtures/compose/*", "reference/_gen/pass51/harness-requests.md",
           "reference/_gen/pass51/lane-P.*"]),
    # H: the harness, the build file's acceptance wiring, acceptance test files (listed in `files`), new h_* files.
    ("H", ["test/harness/*", "terraform-provider-docker.build.ps1", "Makefile", P + "h_*.go",
           "reference/_gen/pass51/lane-H.*"] + H_EXTRA),
    # D: data.docker_dockerfile (Pass 5 Lane D files) and the round-trip allowlist under testdata/dockerfile.
    ("D", [P + "dockerfile_*.go", P + "datasource_dockerfile*.go", P + "d_*.go", P + "*_d_test.go",
           P + "testdata/dockerfile*", "reference/_gen/pass51/lane-D.*"]),
    # S: Pass 5 Lane E files, the PARTIAL markers file, merge.py status logic and its outputs.
    ("S", [P + "buildx_*.go", P + "buildkit_*.go", P + "image_build_buildkit*.go", P + "action_buildx_*.go",
           P + "datasource_buildx_*.go", P + "e_*.go", P + "*_e_test.go", P + "s_*.go", P + "pass51_partial*.go",
           "reference/_gen/merge.py", "reference/_gen/validate.py", "reference/inventory.json", "reference/INVENTORY.md",
           "reference/_gen/pass51/lane-S.*"]),
    # R: contract helpers, renames + overrides, CONTRACT.md, parity tool, migration guide, and every other
    # internal/provider file (the inspect code paths of Pass 5 Lanes 0/A/B/C/F and their unit tests).
    ("R", ["reference/CONTRACT.md", "reference/_gen/renames.json", "reference/_gen/pass5/renames_overrides.json",
           "reference/_gen/generators/pass5/renames_post.py", "reference/_gen/generators/pass5/build_renames.py",
           "tools/parity/*", "templates/guides/*", "reference/_gen/fragments/*", P + "r_*.go", P + "*.go", "reference/_gen/pass51/lane-R.*"]),
]


def rule_owner(path):
    for lane, pats in RULES:
        if any(fnmatch.fnmatchcase(path, p) for p in pats):
            return lane
    return "Z"


def load():
    return json.load(open(OWN, encoding="utf-8"))


def owner_from_json(sec, path):
    path = path.replace("\\", "/")
    if path in sec["files"]:
        return sec["files"][path]
    for r in sec["rules"]:
        if any(fnmatch.fnmatchcase(path, p) for p in r["patterns"]):
            return r["lane"]
    return "Z"


if len(sys.argv) >= 2 and sys.argv[1] in ("--check", "--owner"):
    sec = load()["pass51"]
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
acc = set()
for f in listed:
    if f.startswith(P) and f.endswith("_test.go") and "/" not in f[len(P):] and os.path.exists(os.path.join(ROOT, f)):
        if "func TestAcc" in open(os.path.join(ROOT, f), encoding="utf-8").read():
            acc.add(f)
acc.discard(P + "compose_parity_acc_c_test.go")
files = {}
for f in sorted(set(listed)):
    if not f or not os.path.exists(os.path.join(ROOT, f)):
        continue
    files[f] = "H" if f in acc else rule_owner(f)
d = load()
d["pass51"] = {
    "about": "Pass 5.1 file ownership (lanes H, P, D, R, S; Z = integration and generated files). Each lane edits only "
             "files it owns; `files` lists every existing file, a new file belongs to the first `rules` entry it "
             "matches (prefer your lane's prefix: h_/p_/d_/r_/s_*.go), anything unmatched is Z. A cross-lane need is a "
             "TODO line with the exact change in reference/_gen/pass51/lane-<X>.md, never an edit. Check before "
             "staging: p51_ownership.py --check <lane> <listing>.",
    "rules": [{"lane": l, "patterns": p} for l, p in RULES],
    "files": files,
}
with open(OWN, "w", encoding="utf-8", newline="\n") as f:
    json.dump(d, f, indent=2)
    f.write("\n")
cnt = {}
for v in files.values():
    cnt[v] = cnt.get(v, 0) + 1
print("pass51 files", len(files), dict(sorted(cnt.items())), "acceptance files -> H:", len(acc))
