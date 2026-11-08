"""Pass 4.5 Lane Z corrections: break each new validate.py rule once, show it fails, restore, show it passes.

    python -I -B reference/_gen/generators/lanez/p45c_negcheck.py      (from the repo root)

Each case edits one real outputs fragment, runs merge.py + validate.py, then writes the original bytes back and runs
both again. The fragment is restored even if a step raises.
"""
import json, os, subprocess, sys

OUT = os.path.join("reference", "_gen", "fragments", "outputs")
PY = [sys.executable, "-I", "-B"]


def run():
    m = subprocess.run(PY + ["reference/_gen/merge.py"], capture_output=True, text=True)
    v = subprocess.run(PY + ["reference/_gen/validate.py"], capture_output=True, text=True)
    lines = [l for l in v.stdout.splitlines() if l.startswith(("VIOLATION", "checked"))]
    return m.returncode, v.returncode, lines


def no_tail_since(d):
    d["items"]["engine:container-logs"].pop("inputs")


def action_without_result(d):
    it = d["items"]["engine:container-export"]
    it["outputs"] = [o for o in it["outputs"] if o["category"] != "result"]


def unregistered_version(d):
    d["items"]["engine:container-export"]["outputs"].append({
        "name": "containerd_version", "tf_type": "string", "category": "version",
        "source": {"type": "swagger", "file": "upstream/engine-api/api/swagger.yaml",
                   "pointer": "#/definitions/SystemVersion/properties/Components"},
        "known_after_apply": True, "sensitive": False})


def cli_version_without_cli(d):
    assert d["items"]["cli-only:stack"]["uses_cli"] is False
    d["items"]["cli-only:stack"]["outputs"].append({
        "name": "docker_cli_version", "tf_type": "string", "category": "version",
        "source": {"type": "sources_md", "pin": "docker_cli"}, "known_after_apply": False, "sensitive": False})


CASES = [("L1  logs item without tail/since", "A", no_tail_since),
         ("V6  action without result", "A", action_without_result),
         ("V3  unregistered version name", "A", unregistered_version),
         ("V3  docker_cli_version on uses_cli=false", "E", cli_version_without_cli)]

ok = True
for label, lane, mutate in CASES:
    path = os.path.join(OUT, lane + ".json")
    with open(path, "rb") as f:
        orig = f.read()
    try:
        d = json.loads(orig.decode("utf-8"))
        mutate(d)
        with open(path, "w", encoding="utf-8", newline="\n") as f:
            json.dump(d, f, indent=1 if lane in "AB" else 2, ensure_ascii=False)
            f.write("\n")
        mb, vb, lb = run()
    finally:
        with open(path, "wb") as f:
            f.write(orig)
    ma, va, la = run()
    print("== %s (%s.json)" % (label, lane))
    print("   broken:   merge exit %d, validate exit %d" % (mb, vb))
    for l in lb:
        print("     " + l)
    print("   restored: merge exit %d, validate exit %d" % (ma, va))
    for l in la:
        print("     " + l)
    ok &= vb == 1 and va == 0 and ma == 0
print("ALL AS EXPECTED" if ok else "UNEXPECTED RESULT")
sys.exit(0 if ok else 1)
