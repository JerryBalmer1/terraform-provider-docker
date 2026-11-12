"""Pass 7 Lane 0: add the `pass7` section to reference/_gen/pass5/ownership.json (the lane that owns each file during
Pass 7, the Docker Hub build pass; exactly one lane per file; lanes edit only their own files), and check a lane's
changed paths against it.

    python -I -B reference/_gen/generators/pass7/p7_ownership.py                      (from the repo root)
    python -I -B reference/_gen/generators/pass7/p7_ownership.py --check <lane> <file of changed paths>
    python -I -B reference/_gen/generators/pass7/p7_ownership.py --owner <path>...

Lanes (reference/_gen/pass7/lane-0.md): H Hub client + auth + repositories/tags; T tokens (PAT, OAT) + the ephemeral
docker_hub_token; O org-scoped items (orgs, members, invites, groups, org settings, SCIM, audit logs), built and tested
against a fake Hub only; D docs, examples, skill, climap; Z inventory and gates (fragments, generated files,
BUILD_REPORT, CHANGELOG). 0 is Lane 0, frozen while the lanes run. `files` assigns every file that exists now; a new
file belongs to the first `rules` entry its path matches; anything unmatched is Z.
"""
import fnmatch, json, os, subprocess, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
OWN = os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json")
SECTION = "pass7"
P = "internal/provider/"
G = "reference/_gen/pass7/"
E = "examples/"

RULES = [
    # Z first: generated output and integration files other lanes' globs would otherwise catch.
    ("Z", [P + "registry_gen.go", P + "registry_gen_test.go", P + "contract_gen.go", "docs/*", "README_resources.md",
           "reference/inventory.json", "reference/INVENTORY.md", "reference/_gen/fieldmap.json",
           "reference/_gen/deferred.json", "reference/_gen/fragments/*", "reference/_gen/fieldmap.d/*",
           "reference/_gen/allowed-violations.txt", "BUILD_REPORT.md", "CHANGELOG.md", "CLAUDE.md", "README.md",
           ".github/*", ".gitignore", G + "commit-groups/*", G + "lane-Z.*", "reference/_gen/pass5/ownership.json",
           "reference/_gen/generators/pass7/p7_commit_groups.py", "reference/_gen/merge.py",
           "reference/_gen/validate.py", "reference/_gen/pins.py", "reference/_gen/SCHEMA.md", "reference/CONTRACT.md",
           "reference/SOURCES.md", "reference/upstream/*", "tools/registrygen/*", "tools/contractgen/*",
           "tools/fieldcov/*", "tools/drift/*", "tools/doccounts/*", "tools/deferred/*", "reference/_gen/deferred.d/*",
           "terraform-provider-docker.build.ps1", "build/*"]),
    # 0: Lane 0 (frozen during the lanes).
    ("0", ["reference/_gen/generators/pass7/p7_hub.py", "reference/_gen/generators/pass7/p7_ownership.py",
           G + "lane-0.*", G + "hub-probe.md"]),
    # T: tokens and the token exchange (before H, whose hub_* glob would catch them).
    ("T", [P + "hub_access_token*", P + "hub_org_access_token*", P + "ephemeral_hub_token*", P + "*_p7_t*",
           G + "lane-T.*"]),
    # O: org-scoped items, fake Hub only.
    ("O", [P + "hub_org_*", P + "hub_group*", P + "hub_invite*", P + "hub_scim*", P + "hub_audit*",
           P + "hub_repository_group*", P + "hub_fake*", P + "*_p7_o*", G + "lane-O.*"]),
    # H: the Hub client, provider arguments hub_username / hub_token, repositories and tags.
    ("H", [P + "hub_*", P + "provider.go", P + "provider_hub*", P + "*_p7_h*", G + "lane-H.*"]),
    # D: docs, examples, skills, CLI map.
    ("D", [E + "resources/docker_hub_*", E + "data-sources/docker_hub_*", E + "ephemeral-resources/docker_hub_*",
           E + "actions/docker_hub_*", E + "provider/*", "templates/*", ".claude/skills/*", "tools/genskill/*",
           "tools/climap/*", "tools/parity/*", G + "lane-D.*"]),
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
    "about": "Pass 7 file ownership (Docker Hub; lanes H Hub client + auth + repositories/tags, T tokens + "
             "ephemeral docker_hub_token, O org-scoped items against a fake Hub only, D docs/examples/skill, Z "
             "inventory and gates; 0 = Lane 0, frozen during the lanes). Each lane edits only files it owns; `files` "
             "lists every existing file, a new file belongs to the first `rules` entry it matches, anything unmatched "
             "is Z. A cross-lane need is a TODO line with the exact change in reference/_gen/pass7/lane-<X>.md, never "
             "an edit. Check before handing over: p7_ownership.py --check <lane> <listing>.",
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
