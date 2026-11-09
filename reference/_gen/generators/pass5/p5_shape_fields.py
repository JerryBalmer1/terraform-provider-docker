"""Pass 5 (user decision on shapes): record, in the structured-output field maps, which Terraform input attribute each
upstream logging / networks field corresponds to. Adds `input_attr` to the FIELDs (the upstream name and pointer stay
exactly as they are, so inspect_json and the structured output still mirror each API).

    python -I -B reference/_gen/generators/pass5/p5_shape_fields.py      (from the repo root; idempotent)
"""
import json, os, re

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
O = os.path.join(ROOT, "reference", "_gen", "fragments", "outputs")

# (item id prefix set, dotted field path inside the output) -> {field or subfield name: input_attr}
MAP = [
    (("engine:container", "engine:container-data"), "host_config.log_config", {"": "logging", "type": "logging.driver", "config": "logging.options"}),
    (("engine:container", "engine:container-data"), "network_settings.networks", {"": "networks (map key = network name)"}),
    (("swarm:service", "swarm:service-data"), "spec.task_template.log_driver", {"": "logging", "name": "logging.driver", "options": "logging.options"}),
    (("swarm:service", "swarm:service-data"), "spec.task_template.networks", {"": "networks (map key = network name -> target)", "aliases": "networks.aliases", "driver_opts": "networks.driver_opts"}),
    (("compose:services",), "services.logging", {"": "services.logging (compose shape)"}),
]


def find(fields, path):
    head, _, rest = path.partition(".")
    for f in fields:
        if f["name"] == head:
            return f if not rest else find(f.get("fields") or [], rest)
    return None


n = 0
for fn in sorted(os.listdir(O)):
    p = os.path.join(O, fn)
    text = open(p, encoding="utf-8").read()
    ind = len(re.match(r"\{\n( +)", text).group(1))
    d = json.loads(text)
    for iid, e in d["items"].items():
        for ids, path, m in MAP:
            if iid not in ids:
                continue
            for o in e["outputs"]:
                f = find(o.get("fields") or [], path)
                if not f:
                    continue
                for sub, attr in m.items():
                    t = f if sub == "" else next((x for x in f.get("fields") or [] if x["name"] == sub), None)
                    if t is not None and t.get("input_attr") != attr:
                        t["input_attr"] = attr
                        n += 1
    with open(p, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(json.dumps(d, indent=ind, ensure_ascii=False) + "\n")
print("input_attr set on", n, "fields")
