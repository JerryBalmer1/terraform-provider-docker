"""Pass 5 Lane 0: assign every non-skipped inventory item to its implementation lane.

    python -I -B reference/_gen/generators/pass5/p5_lanes.py      (from the repo root, after Invoke-Build Inventory)

Writes reference/_gen/pass5/lanes.json ({item id: lane}). Lanes: A engine (+ registry / manifests), B swarm,
C compose + stack, D dockerfile, E buildx (+ the BuildKit build seam behind docker_image), F migration (no items).
"""
import json, os

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
inv = json.load(open(os.path.join(ROOT, "reference", "inventory.json"), encoding="utf-8"))

OVERRIDE = {
    "buildx:imagetools-inspect": "A",  # same data source as cli-only:manifest-inspect (docker_registry_image_manifests)
    "buildx:build": "E",               # the BuildKit build seam docker_image calls
    "cli-only:manifest-list": "A",
    "cli-only:manifest-inspect": "A",
    "cli-only:stack": "C",
    "cli-only:stacks": "C",
    "cli-only:stack-services": "C",
}
BY_LAYER = {"engine": "A", "swarm": "B", "compose": "C", "dockerfile": "D", "buildx": "E", "cli-only": "E"}
out = {}
for it in inv["items"]:
    if it["kind"] == "skip":
        continue
    out[it["id"]] = OVERRIDE.get(it["id"], BY_LAYER[it["layer"]])
os.makedirs(os.path.join(ROOT, "reference", "_gen", "pass5"), exist_ok=True)
with open(os.path.join(ROOT, "reference", "_gen", "pass5", "lanes.json"), "w", encoding="utf-8", newline="\n") as f:
    json.dump(dict(sorted(out.items())), f, indent=2)
    f.write("\n")
counts = {}
for v in out.values():
    counts[v] = counts.get(v, 0) + 1
print("lanes", dict(sorted(counts.items())))
