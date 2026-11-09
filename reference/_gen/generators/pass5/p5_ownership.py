"""Pass 5 Lane 0: write reference/_gen/pass5/ownership.json, the lane that owns each file under internal/provider,
examples and test during Pass 5 (exactly one lane per file; lanes edit only their own files).

    python -I -B reference/_gen/generators/pass5/p5_ownership.py      (from the repo root)
    python -I -B reference/_gen/generators/pass5/p5_ownership.py --check <lane> <file of changed paths>

`rules` are the naming-prefix rules for files a lane creates (first match wins); `files` assigns every file that
exists now (tracked or not). The script fails when a file matches no rule, so nothing is unowned.
Lane 0 = shared helpers and generated files: lanes call them and never edit them.
"""
import fnmatch, json, os, subprocess

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
lanes = json.load(open(os.path.join(ROOT, "reference", "_gen", "pass5", "lanes.json"), encoding="utf-8"))
inv = json.load(open(os.path.join(ROOT, "reference", "inventory.json"), encoding="utf-8"))

P = "internal/provider/"
RULES = [
    # Lane 0: shared helpers, generated registration, provider plumbing, shared test scaffolding.
    ("0", [P + "registry_gen.go", P + "registry_gen_test.go", P + "contract_gen.go", P + "contract*.go", P + "action_result*.go",
           P + "log_entries*.go", P + "seams*.go", P + "stub_pass5.go", P + "api_version*.go", P + "api_gate*.go", P + "action_common*.go",
           P + "provider.go", P + "provider_*.go", P + "client.go", P + "convert.go", P + "moby_convert.go",
           P + "moby_helpers_test.go", P + "list_common.go", P + "harness_acc_p3_test.go", P + "e2e_test.go",
           P + "datasource_p3_fake_test.go", "examples/provider/*"]),
    # Lane F: migration (MoveState), one file per type.
    ("F", [P + "move_*.go"]),
    # Lane E: buildx + the BuildKit build seam implementation.
    ("E", [P + "buildx_*.go", P + "buildkit_*.go", P + "image_build_buildkit*.go", P + "action_buildx_*.go",
           P + "datasource_buildx_*.go", P + "e_*.go", P + "*_e_test.go", P + "stubs_pass5_e.go", P + "pass5_todo_e.go"]),
    # Lane D: data.docker_dockerfile.
    ("D", [P + "dockerfile_*.go", P + "datasource_dockerfile*.go", P + "d_*.go", P + "*_d_test.go", P + "testdata/dockerfile*",
           P + "stubs_pass5_d.go", P + "pass5_todo_d.go"]),
    # Lane C: compose + stack.
    ("C", [P + "compose_*.go", P + "stack_*.go", P + "action_compose_*.go", P + "datasource_compose_*.go",
           P + "datasource_stack*.go", P + "function_compose_env*.go", P + "c_*.go", P + "*_c_test.go", P + "testdata/compose*",
           P + "stubs_pass5_c.go", P + "pass5_todo_c.go", "test/*"]),
    # Lane B: swarm.
    ("B", [P + "swarm_*.go", P + "node_*.go", P + "nodes_*.go", P + "service_*.go", P + "secret_*.go", P + "docker_config_*.go",
           P + "config_*.go", P + "task_*.go", P + "tasks_*.go", P + "action_service*.go", P + "action_swarm*.go", P + "action_node*.go",
           P + "datasource_p3_swarm_lists*.go", P + "datasource_swarm*.go", P + "datasource_service*.go", P + "datasource_task*.go",
           P + "datasource_node*.go", P + "datasource_secret*.go", P + "datasource_config*.go", P + "ephemeral_swarm_*.go",
           P + "b_*.go", P + "*_b_test.go", P + "stubs_pass5_b.go", P + "pass5_todo_b.go"]),
    # Lane A: engine (containers, images, networks, volumes, plugins, exec, system, prune, registry, logs, tag).
    ("A", [P + "*.go", P + "testdata/*"]),
]

TF = {"resource": "resources", "data_source": "data-sources", "action": "actions", "ephemeral_resource": "ephemeral-resources",
      "list_resource": "list-resources", "function": "functions"}
example_lane = {}
for it in inv["items"]:
    if it["kind"] in TF:
        key = "examples/%s/%s/" % (TF[it["kind"]], it["name"])
        # The type's lane is the lane of its first item (sorted by id), the same item that owns the stub.
        example_lane.setdefault(key, []).append((it["id"], lanes[it["id"]]))
example_lane = {k: sorted(v)[0][1] for k, v in example_lane.items()}
# A type shared by several items belongs to the lane of the item that IS the object (docker_image -> engine:image).
example_lane.update({"examples/resources/docker_image/": "A", "examples/resources/docker_service/": "B",
                     "examples/resources/docker_compose/": "C", "examples/data-sources/docker_registry_image_manifests/": "A"})


def owner(path):
    for prefix, lane in sorted(example_lane.items(), key=lambda x: -len(x[0])):
        if path.startswith(prefix):
            return lane
    for lane, pats in RULES:
        if any(fnmatch.fnmatchcase(path, p) for p in pats):
            return lane
    return None


import sys
if len(sys.argv) == 4 and sys.argv[1] == "--check":
    # --check <lane> <file listing changed paths, one per line>: exit 1 when a path is owned by another lane.
    lane, listing = sys.argv[2], sys.argv[3]
    wrong = []
    for f in open(listing, encoding="utf-8").read().split():
        f = f.replace("\\", "/")
        if f.startswith(("internal/provider/", "examples/", "test/")):
            o = owner(f)
            if o != lane:
                wrong.append("%s (owner %s)" % (f, o))
        elif lane not in ("F",) or not f.startswith(("tools/parity/", "templates/")):
            wrong.append("%s (outside the lane's area)" % f)
    print("\n".join(wrong) if wrong else "ownership ok: lane %s" % lane)
    raise SystemExit(1 if wrong else 0)

tracked = subprocess.run(["git", "-C", ROOT, "ls-files", "-co", "--exclude-standard", "--", "internal/provider", "examples", "test"],
                         capture_output=True, text=True, check=True).stdout.split()
files, bad = {}, []
for f in sorted(set(tracked)):
    if not os.path.exists(os.path.join(ROOT, f)):
        continue
    o = owner(f)
    if o is None:
        bad.append(f)
    files[f] = o
if bad:
    raise SystemExit("unowned: " + ", ".join(bad))
out = {
    "about": "Pass 5 file ownership. Each lane edits only files it owns; Lane 0 files are shared helpers and generated "
             "files that lanes call but never edit. A new file belongs to the first rule its path matches (lanes: prefer "
             "your lane's prefixes, e.g. a lane-specific test helper file <lane>_*.go). examples/<kind>/<type>/ belongs to "
             "the lane of the type. A cross-lane need is a TODO line in the lane report, never an edit.",
    "rules": [{"lane": l, "patterns": p} for l, p in RULES],
    "examples": dict(sorted(example_lane.items())),
    "files": files,
}
os.makedirs(os.path.join(ROOT, "reference", "_gen", "pass5"), exist_ok=True)
with open(os.path.join(ROOT, "reference", "_gen", "pass5", "ownership.json"), "w", encoding="utf-8", newline="\n") as f:
    json.dump(out, f, indent=2)
    f.write("\n")
cnt = {}
for v in files.values():
    cnt[v] = cnt.get(v, 0) + 1
print("files", len(files), dict(sorted(cnt.items())))
