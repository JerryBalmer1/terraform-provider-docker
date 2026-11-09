"""Attribute extractors for the Pass 5 rename audit (provenance for renames.json).

kreuzwerker_attrs(docs_dir): every attribute of every kreuzwerker v4.6.0 resource,
data source and action, read from the tfplugindocs output at that tag (docs/), with
full dotted paths ("ports.internal", "task_spec.container_spec.mounts.target").
Each item: (type, path, kind_text) where type is kind-prefixed (data./action.).

our_attrs(ref_dir): every attribute of our types, read from the genskill reference
(.claude/skills/terraform-provider-docker/reference/*.md, generated from the live schema).
Each item: (type, path, type_text, status).

Python 3 stdlib only; run with python -I.
"""
import os
import re
import sys

KW_KIND = {"resources": "", "data-sources": "data.", "actions": "action."}
_ITEM = re.compile(r"^- `([A-Za-z0-9_]+)` \(([^)]*)\)")
_NEST = re.compile(r"^### Nested Schema for `([^`]+)`")


def kreuzwerker_attrs(docs_dir):
    out = []
    for sub, prefix in KW_KIND.items():
        d = os.path.join(docs_dir, sub)
        for fn in sorted(os.listdir(d)):
            if not fn.endswith(".md"):
                continue
            typ = prefix + "docker_" + fn[:-3]
            in_schema = False
            parent = ""
            with open(os.path.join(d, fn), encoding="utf-8") as f:
                for line in f:
                    line = line.rstrip("\n")
                    if line.startswith("## Schema"):
                        in_schema = True
                        continue
                    if line.startswith("## ") and in_schema:
                        in_schema = False
                    if not in_schema:
                        continue
                    m = _NEST.match(line)
                    if m:
                        parent = m.group(1)
                        continue
                    m = _ITEM.match(line)
                    if m:
                        path = (parent + "." if parent else "") + m.group(1)
                        out.append((typ, path, m.group(2)))
    return out


_HEAD = re.compile(r"^### `([^`]+)`")
_BLK = re.compile(r"`([A-Za-z0-9_]+)` \(([^)]*)\)")
_ROW = re.compile(r"^\| `([A-Za-z0-9_]+)` \| `([^`]*)` \| ([^|]*) \|")


def our_attrs(ref_dir):
    out = []
    for fn in sorted(os.listdir(ref_dir)):
        if not fn.endswith(".md") or fn.startswith(("function.", "provider")):
            continue
        typ = fn[:-3]
        parent = ""
        in_args = False
        with open(os.path.join(ref_dir, fn), encoding="utf-8") as f:
            for line in f:
                line = line.rstrip("\n")
                if line.startswith("## "):
                    in_args = line.startswith("## Arguments")
                    parent = ""
                    continue
                if not in_args:
                    continue
                m = _HEAD.match(line)
                if m:
                    parent = m.group(1)
                    continue
                if line.startswith("Nested blocks: "):
                    for name, desc in _BLK.findall(line):
                        path = (parent + "." if parent else "") + name
                        out.append((typ, path, "block: " + desc, "block"))
                    continue
                m = _ROW.match(line)
                if m and m.group(1) != "Name":
                    path = (parent + "." if parent else "") + m.group(1)
                    out.append((typ, path, m.group(2), m.group(3).strip()))
    return out


if __name__ == "__main__":
    which = sys.argv[1]
    rows = kreuzwerker_attrs(sys.argv[2]) if which == "kw" else our_attrs(sys.argv[2])
    for r in rows:
        print("\t".join(r))
