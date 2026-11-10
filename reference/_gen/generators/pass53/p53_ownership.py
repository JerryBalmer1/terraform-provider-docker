"""Pass 5.3 Lane 0: add the `pass53` section to reference/_gen/pass5/ownership.json (the lane that owns each file during
Pass 5.3; exactly one lane per file; lanes edit only their own files), and check a lane's changed paths against it.

    python -I -B reference/_gen/generators/pass53/p53_ownership.py                      (from the repo root)
    python -I -B reference/_gen/generators/pass53/p53_ownership.py --check <lane> <file of changed paths>
    python -I -B reference/_gen/generators/pass53/p53_ownership.py --owner <path>...

Lanes: A engine/container (D1 state, D2 docker_container_run, D3, engine field gaps), R registry/auth (D4, D9, registry
and image field gaps, the harness), S swarm (D6, swarm field gaps), K compose (D5, compose field gaps and partials),
B buildx (build.platforms, buildx partials, bake gaps), I CI/docs (D7, D8, D10). 0 is Lane 0's shared helpers and the
field-coverage tool, frozen while the lanes run (a lane that needs a change there logs a TODO). Z is integration: the
generated files, the existing inventory fragments, BUILD_REPORT and CHANGELOG. `files` assigns every file that exists
now; a new file belongs to the first `rules` entry its path matches; anything unmatched is Z. --check reads
ownership.json only, so the JSON is the single source of truth while lanes run. Earlier sections stay as history.
"""
import fnmatch, json, os, subprocess, sys

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
OWN = os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json")
P = "internal/provider/"
G = "reference/_gen/pass53/"
F = "reference/_gen/fragments/"
EX = "examples/"


def ex(kinds, names):
    return [EX + k + "/" + n + "/*" for k in kinds for n in names]


RULES = [
    # Z first for the paths other lanes' globs would otherwise catch: generated output and integration files.
    ("Z", [P + "registry_gen.go", P + "registry_gen_test.go", P + "contract_gen.go", P + "z_*.go",
           "docs/*", "README_resources.md", ".claude/skills/terraform-provider-docker/*",
           ".claude/skills/docker-cli-map/*", "reference/inventory.json", "reference/INVENTORY.md",
           "reference/_gen/fieldmap.json", "reference/_gen/renames.json", "BUILD_REPORT.md", "CHANGELOG.md",
           G + "commit-groups/*", G + "lane-Z.*", "reference/_gen/pass5/ownership.json",
           "reference/_gen/generators/pass53/*", "templates/guides/migrating-from-kreuzwerker.md.tmpl",
           F + "z1-engine.json", F + "z2-compose.json", F + "z3-cli.json", F + "z3-cli-map.json",
           F + "z4-dockerfile.json", F + "pass5.json", F + "dockerfile-sources.json", F + "outputs/[A-EPZ].json",
           F + "outputs/P5.json", "reference/_gen/merge.py"]),
    # 0: Lane 0's helpers and the coverage tool, frozen during the lanes.
    ("0", ["tools/fieldcov/*", P + "move_common.go", P + "move_common_test.go", "tools/parity/guide.go", P + "owner_label.go", P + "owner_label_test.go", P + "api_log.go",
           P + "api_log_test.go", G + "field-gaps/*", G + "fieldcov-before.json", G + "lane-0.*",
           "reference/_gen/fieldmap.d/0.json"]),
    # I: CI, build entry point, project docs, the npipe smoke test, the count check.
    ("I", [".github/*", "GNUmakefile", "CLAUDE.md", "README.md", "terraform-provider-docker.build.ps1",
           "tools/doccounts/*", P + "i_*.go", P + "*_i_test.go", ".claude/skills/engine-api-watch/*",
           G + "lane-I.*", F + "pass53-I.json", "reference/_gen/fieldmap.d/I.json", "templates/index.md.tmpl",
           "CONTRIBUTING.md"]),
    # R: registry, image pull/push/tag, auth and credential helpers, CONTRACT.md, the dind harness.
    ("R", [P + "image_resource.go", P + "image_data_source.go", P + "img_common.go", P + "img_datasources.go",
           P + "img_action_commit.go", P + "action_image.go", P + "reg_*.go", P + "registry_image_*.go",
           P + "tag_resource.go", P + "move_image.go", P + "move_registry_image.go", P + "move_tag.go",
           P + "provider_auth*.go", P + "provider.go", P + "provider_test.go", P + "client.go", P + "client_test.go",
           P + "ephemeral_registry_*.go", P + "datasource_p3_registry*.go", P + "function_image_ref*.go",
           P + "pass51_partial_s.go", P + "r_*.go", P + "*_r_test.go", P + "testdata/credhelper*",
           "test/harness/*", "reference/CONTRACT.md", G + "lane-R.*", F + "pass53-R.json", F + "outputs/P53R.json",
           "reference/_gen/fieldmap.d/R.json"]
          + ex(["resources", "data-sources", "ephemeral-resources", "actions"],
               ["docker_image", "docker_tag", "docker_registry_image", "docker_registry_manifest_list",
                "docker_registry_token", "docker_registry_login", "docker_image_commit", "docker_image_import",
                "docker_image_load", "docker_image_save", "docker_image_prune", "docker_registry_manifest"])),
    # B: buildx and the BuildKit seam, docker_image's build path.
    ("B", [P + "buildkit_*.go", P + "buildx_*.go", P + "image_build*.go", P + "img_build_opts*.go",
           P + "img_buildx_outputs*.go", P + "action_buildx_bake*.go", P + "datasource_buildx_*.go",
           P + "move_buildx_builder.go", P + "b_*.go", P + "*_b_test.go", P + "testdata/bake*",
           G + "lane-B.*", F + "pass53-B.json", F + "outputs/P53B.json", "reference/_gen/fieldmap.d/B.json"]
          + ex(["resources", "data-sources", "actions"],
               ["docker_buildx_builder", "docker_buildx_bake", "docker_buildx_builders", "docker_buildx_disk_usage",
                "docker_buildx_history", "docker_builder_prune"])),
    # K: compose (local + swarm mode), ComposeParity, the compose partial markers.
    ("K", [P + "compose_*.go", P + "c_compose_refs*.go", P + "action_compose_*.go", P + "datasource_compose_*.go",
           P + "function_compose_env*.go", P + "move_compose.go", P + "k_*.go", P + "*_k_test.go", P + "c_*.go",
           P + "*_c_test.go", P + "p_compose_parity*.go", P + "pass53_partial_k.go", P + "testdata/compose*",
           "test/fixtures/compose/*", "tools/compose2stack/*", G + "lane-K.*", F + "pass53-K.json",
           F + "outputs/P53K.json", "reference/_gen/fieldmap.d/K.json"]
          + ex(["resources", "data-sources", "actions", "functions"],
               ["docker_compose", "docker_compose_*", "compose_env"])),
    # S: swarm topology and workload.
    ("S", [P + "service_*.go", P + "swarm_*.go", P + "secret_resource*.go", P + "docker_config_resource*.go",
           P + "node_resource*.go", P + "stack_*.go", P + "action_service*.go", P + "action_swarm*.go",
           P + "ephemeral_swarm_tokens*.go", P + "datasource_swarm_*.go", P + "datasource_p3_swarm_lists*.go",
           P + "datasource_stack*.go", P + "move_service.go", P + "move_secret.go", P + "move_config.go",
           P + "s_*.go", P + "*_s_test.go", G + "lane-S.*", F + "pass53-S.json", F + "outputs/P53S.json",
           "reference/_gen/fieldmap.d/S.json"]
          + ex(["resources", "data-sources", "ephemeral-resources", "actions", "list-resources"],
               ["docker_service", "docker_service_*", "docker_swarm_*", "docker_secret", "docker_config",
                "docker_node", "docker_node_*", "docker_stack", "docker_stack_*", "docker_task*"])),
    # A: engine single-host objects: container (state, run action, D3), network, volume, plugin, exec, prunes.
    ("A", [P + "container_*.go", P + "ctr_*.go", P + "cop_*.go", P + "action_container_*.go", P + "action_exec*.go",
           P + "ephemeral_exec_output*.go", P + "network_*.go", P + "volume_*.go", P + "obj_cluster_volume*.go",
           P + "plugin_*.go", P + "obj_plugin_actions*.go", P + "move_container.go", P + "move_network.go",
           P + "move_volume.go", P + "move_plugin.go", P + "action_prune*.go", P + "datasource_p3_containers*.go",
           P + "datasource_p3_logs*.go", P + "log_entries*.go", P + "moby_convert*.go", P + "a_*.go",
           P + "*_a_test.go", "templates/guides/container-state.md.tmpl", "reference/_gen/pass5/renames_overrides.json",
           G + "lane-A.*", F + "pass53-A.json", F + "outputs/P53A.json", "reference/_gen/fieldmap.d/A.json"]
          + ex(["resources", "data-sources", "actions", "ephemeral-resources"],
               ["docker_container", "docker_container_*", "docker_network", "docker_network_*", "docker_volume",
                "docker_volume_*", "docker_plugin", "docker_plugin_*", "docker_exec", "docker_exec_output",
                "docker_system_prune", "docker_containers"])),
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
    sec = load()["pass53"]
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
d["pass53"] = {
    "about": "Pass 5.3 file ownership (lanes A, R, S, K, B, I; 0 = Lane 0 helpers and fieldcov, frozen during the lanes; "
             "Z = integration and generated files). Each lane edits only files it owns; `files` lists every existing "
             "file, a new file belongs to the first `rules` entry it matches (prefer your lane's prefix: a_/r_/s_/k_/b_/"
             "i_*.go), anything unmatched is Z. Inventory: new items go in reference/_gen/fragments/pass53-<lane>.json, "
             "new outputs in fragments/outputs/P53<lane>.json; a change to an existing fragment entry is a TODO for Z. "
             "Field coverage: manual entries in reference/_gen/fieldmap.d/<lane>.json. A cross-lane need is a TODO line "
             "with the exact change in reference/_gen/pass53/lane-<X>.md, never an edit. Check before handing over: "
             "p53_ownership.py --check <lane> <listing>.",
    "rules": [{"lane": l, "patterns": p} for l, p in RULES],
    "files": files,
}
with open(OWN, "w", encoding="utf-8", newline="\n") as f:
    json.dump(d, f, indent=2)
    f.write("\n")
cnt = {}
for v in files.values():
    cnt[v] = cnt.get(v, 0) + 1
print("pass53 files", len(files), dict(sorted(cnt.items())))
