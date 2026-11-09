"""Pass 5 Lane 0, after p5_decisions.py: the Q35 fold made each former result output a field of the single `result`
object, with `upstream` taken from the last segment of its source pointer. Those fields are named after the former
output, not derived from an upstream key, so `upstream` is set to the field's own name (the key the action puts in
its result object).

    python -I -B reference/_gen/generators/pass5/p5_fix_fold.py      (from the repo root, once)
"""
import json, os, re

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
O = os.path.join(ROOT, "reference", "_gen", "fragments", "outputs")
n = 0
for fn in sorted(os.listdir(O)):
    p = os.path.join(O, fn)
    text = open(p, encoding="utf-8").read()
    ind = len(re.match(r"\{\n( +)", text).group(1))
    d = json.loads(text)
    for e in d["items"].values():
        for o in e["outputs"]:
            if o["name"] == "result" and o.get("description", "").startswith("The action's outcome as one object (Q35)"):
                for fd in o["fields"]:
                    if fd["upstream"] != fd["name"]:
                        fd["upstream"] = fd["name"]
                        n += 1
    with open(p, "w", encoding="utf-8", newline="\n") as f:
        f.write(json.dumps(d, indent=ind, ensure_ascii=False) + "\n")
print("fixed", n)
