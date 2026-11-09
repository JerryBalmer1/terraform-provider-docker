"""Pass 5 Lane Z: inventory follow-ups from the lane reports (run once from the repo root, after the lane merge).

    python -I -B reference/_gen/generators/pass5/p5_lanez.py

- dockerfile:data-dockerfile (Lane D): the git source became `url` (raw HTTPS fetch, no git) this pass; builder mode
  (`stage` blocks) is a source too; descriptions and notes follow.
- compose:service-build: drop compose_spec_version / compose_go_version (they describe compose parsing, which a
  docker_image build does not do; the item's outputs live on docker_image). engine_api_version stays.
- buildx:bake: `dockerfile_syntax` (map per target) is a result value, not a registered version output (the version
  registry holds one value per item); it moves into the `result` object as field `dockerfile_syntax`.
"""
import json, os, re

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
Z = os.path.join(ROOT, "reference", "_gen", "fragments")


def load(p):
    t = open(p, encoding="utf-8").read()
    return json.loads(t), len(re.match(r"\{\n( +)", t).group(1))


def save(p, d, ind):
    with open(p, "w", encoding="utf-8", newline="\n") as f:
        f.write(json.dumps(d, indent=ind, ensure_ascii=False) + "\n")


# --- dockerfile (item in dockerfile-sources.json, outputs in outputs/D.json)
p = os.path.join(Z, "dockerfile-sources.json")
d, ind = load(p)
it = next(i for i in d["items"] if i["id"] == "dockerfile:data-dockerfile")
it["summary"] = ("Read one Dockerfile from exactly one source (`content` inline, `path` local file, `url` raw HTTPS fetch, `image` "
                 "reference, or ordered `stage` / `instruction` blocks), parse it into the block model (directives, global ARGs, "
                 "stages, every instruction with all flags, exposed ports, base images) and render `dockerfile`; image mode "
                 "reconstructs an approximate instruction list from the image history (fidelity = approximate; exact otherwise).")
d.setdefault("notes", []).append(
    "Pass 5 Lane D/Z: the `git` source became `url` (a raw HTTPS fetch of one file, pure Go, 4 MiB cap, no git); `git_commit` "
    "stays in the schema and is always null. Builder mode (`stage` blocks) is a fifth source (`source_type` = blocks). "
    "Instruction order across keywords is one ordered `instruction` block per stage holding exactly one keyword block. "
    "The per-keyword structured objects stay JSON strings until their fields are mapped completely (fields_complete). "
    "Block-level outputs (instruction, stage_index, instruction_index, dockerfile, content_sha256, dockerfile_syntax) sit on "
    "each instruction / global_arg block; the `stage` block has no computed `instruction` (it would clash with the nested "
    "block), its FROM data is in stages[*].")
save(p, d, ind)

p = os.path.join(Z, "outputs", "D.json")
d, ind = load(p)
outs = d["items"]["dockerfile:data-dockerfile"]["outputs"]
desc = {"id": "`content:<sha256>`, `path:<absolute path>`, `url:<url>`, `image:<reference>` or `blocks:<sha256>`.",
        "source_type": "`content`, `path`, `url`, `image` or `blocks`.",
        "git_commit": "Always null since Pass 5 (the git source became `url`); kept for a later git source.",
        "source_dockerfile": "The Dockerfile text exactly as read from content / path / url, before parsing (Q47); null in image and blocks mode."}
for o in outs:
    if o["name"] in desc:
        o["description"] = desc[o["name"]]
save(p, d, ind)

# --- compose:service-build (outputs/C.json)
p = os.path.join(Z, "outputs", "C.json")
d, ind = load(p)
e = d["items"]["compose:service-build"]
e["outputs"] = [o for o in e["outputs"] if o["name"] not in ("compose_spec_version", "compose_go_version")]
e["outputs_note"] = (e.get("outputs_note", "") + " Pass 5 Lane Z: the compose version outputs were dropped here; they describe "
                     "compose parsing, which docker_image does not do (docker_compose carries them).").strip()
save(p, d, ind)

# --- buildx:bake (outputs/E.json)
p = os.path.join(Z, "outputs", "E.json")
d, ind = load(p)
e = d["items"]["buildx:bake"]
ds = next(o for o in e["outputs"] if o["name"] == "dockerfile_syntax")
e["outputs"].remove(ds)
res = next(o for o in e["outputs"] if o["name"] == "result")
if not any(f["name"] == "dockerfile_syntax" for f in res["fields"]):
    res["fields"].append({"name": "dockerfile_syntax", "upstream": "dockerfile_syntax", "tf_type": "map(string)"})
save(p, d, ind)
print("ok")
