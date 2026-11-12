"""Upstream pins, read from reference/SOURCES.md (the single source), and the pin consistency gate.

merge.py imports sources_pins() for the inventory "pins" block, so no generator carries its own copy.
Run directly (Invoke-Build PinCheck, part of InventoryCheck) it checks every pinned version string in the
generators, the build scripts, tools/apiwatch/pinned.json, go.mod and the dind harness against SOURCES.md, and the
generated copies (reference/inventory.json, internal/provider/contract_gen.go) against what merge.py would write.
Stdlib only. Exit 0 clean, 1 on any mismatch (one MISMATCH line each), 2 when SOURCES.md cannot be read."""
import glob, json, os, re, sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))
SOURCES_MD = os.path.join(ROOT, "reference", "SOURCES.md")

# Inventory pin key -> the SOURCES.md row(s) it comes from, by upstream URL. Rows that share a key must agree.
URL_KEYS = [
    (re.compile(r"github\.com/moby/moby/"), "engine_api"),
    (re.compile(r"github\.com/compose-spec/compose-spec/"), "compose_spec"),
    (re.compile(r"github\.com/compose-spec/compose-go/"), "compose_go"),
    (re.compile(r"github\.com/docker/compose/"), "compose_cli"),
    (re.compile(r"github\.com/docker/cli/"), "docker_cli"),
    (re.compile(r"github\.com/moby/buildkit/"), "dockerfile"),
    (re.compile(r"github\.com/docker/buildx/"), "buildx"),
]
PIN_CELL = re.compile(r"^(\S+) @ ([0-9a-f]{40})\b")
# Specs published only as a URL (Pass 7): pinned by version and the sha256 of the downloaded bytes. Kept apart from
# the git pins above: inventory.json carries them as "spec_pins", and contract_gen.go's contractPins does not (yet).
SPEC_KEYS = [
    (re.compile(r"docs\.docker\.com/reference/api/hub/"), "hub_api"),
]
SPEC_CELL = re.compile(r"^(\S+) @ sha256 ([0-9a-f]{64})\b.*?(?:downloaded (\d{4}-\d{2}-\d{2}))?\)?$")


class PinError(Exception):
    pass


def _rows(text):
    for line in text.splitlines():
        if not line.startswith("|") or line.startswith("|---"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) >= 4 and cells[0] != "layer":
            yield cells


def sources_pins(path=SOURCES_MD):
    """{key: (ref, sha)} from the SOURCES.md table, e.g. docker_cli -> ("v29.9.3", "5457...")."""
    with open(path, encoding="utf-8") as f:
        text = f.read()
    pins = {}
    for cells in _rows(text):
        url, cell = cells[2], cells[3]
        key = next((k for rx, k in URL_KEYS if rx.search(url)), None)
        if key is None:
            continue
        m = PIN_CELL.match(cell)
        if not m:
            raise PinError("SOURCES.md row %r: tag/SHA cell %r is not '<ref> @ <40-hex sha>'" % (cells[1][:40], cell))
        got = (m.group(1), m.group(2))
        if key in pins and pins[key] != got:
            raise PinError("SOURCES.md: rows for %s disagree: %s @ %s vs %s @ %s" % (key, pins[key][0], pins[key][1], got[0], got[1]))
        pins[key] = got
    missing = sorted({k for _, k in URL_KEYS} - set(pins))
    if missing:
        raise PinError("SOURCES.md: no row for %s" % ", ".join(missing))
    return pins


def spec_pins(path=SOURCES_MD):
    """{key: (version, sha256, download date)} for the URL-published specs, e.g. hub_api -> ("2-beta", "7c28...", "2026-11-11")."""
    with open(path, encoding="utf-8") as f:
        text = f.read()
    out = {}
    for cells in _rows(text):
        key = next((k for rx, k in SPEC_KEYS if rx.search(cells[2])), None)
        if key is None:
            continue
        m = SPEC_CELL.match(cells[3])
        if not m:
            raise PinError("SOURCES.md row %r: tag/SHA cell %r is not '<version> @ sha256 <64-hex> (... downloaded YYYY-MM-DD)'" % (cells[1][:40], cells[3]))
        out[key] = (m.group(1), m.group(2), m.group(3) or "")
    missing = sorted({k for _, k in SPEC_KEYS} - set(out))
    if missing:
        raise PinError("SOURCES.md: no row for %s" % ", ".join(missing))
    return out


def inventory_spec_pins(specs):
    """The inventory.json "spec_pins" block: "<version> @ sha256 <hex> (downloaded <date>)"."""
    return {k: "%s @ sha256 %s (downloaded %s)" % v for k, v in sorted(specs.items())}


def version(pins, key):
    """The bare version of a pin: v29.9.3 -> 29.9.3, api/v1.56.1 -> 1.56 (Engine API), dockerfile/1.29.0 -> 1.29.0."""
    ref = pins[key][0]
    if key == "engine_api":
        m = re.match(r"api/v(\d+\.\d+)\.\d+$", ref)
        if not m:
            raise PinError("SOURCES.md engine_api ref %r is not api/vX.Y.Z" % ref)
        return m.group(1)
    return ref.split("/")[-1].lstrip("v")


def inventory_pins(pins):
    """The inventory.json "pins" block (also contractPins in internal/provider/contract_gen.go)."""
    out = {}
    for _, key in URL_KEYS:
        ref, sha = pins[key]
        out[key] = ("%s (moby %s @ %s)" % (version(pins, key), ref, sha)) if key == "engine_api" else ("%s @ %s" % (ref, sha))
    return out


# ---- the gate

# Files that may carry a pinned version string (paths relative to the repo root; globs).
SCAN = [
    "reference/_gen/*.py",
    "terraform-provider-docker.build.ps1",
    "build/*.ps1",
    "tools/*/*.go",
    "tools/apiwatch/pinned.json",
    "go.mod",
    "test/harness/docker-compose.yml",
    "test/harness/Invoke-E2E.ps1",
    "test/harness/scripts/*",
]
# Hex tokens in scanned files that are not SOURCES.md pins, with the reason.
NOT_PINS = {
    "1e48613cd36acab30e9146aae6f036ea8e40a288": "kreuzwerker/docker v4.6.0 commit (tools/parity; a parity baseline, not a reference source)",
}
# (pin key, regex with one group = the version as written). The group must equal version(pins, key).
VERSION_RULES = [
    ("docker_cli", re.compile(r"\bdocker:(\d+\.\d+\.\d+)-dind\b")),
    ("docker_cli", re.compile(r"\bEngine (\d+\.\d+\.\d+)\b")),
    ("docker_cli", re.compile(r"\bdocker_cli\"?\s*[:=]\s*\"v(\d+\.\d+\.\d+)")),
    ("docker_cli", re.compile(r"github\.com/docker/cli/tree/v(\d+\.\d+\.\d+)")),
    ("engine_api", re.compile(r"\bEngine API v?(\d+\.\d+)\b(?!\.)")),
    ("engine_api", re.compile(r"\"api_version\":\s*\"(\d+\.\d+)\"")),
    ("engine_api", re.compile(r"/engine/version/v(\d+\.\d+)\.yaml")),
    ("engine_api", re.compile(r"\bmoby api/v(\d+\.\d+)\.\d+")),
    ("compose_cli", re.compile(r"github\.com/docker/compose/(?:tree|releases/download)/v(\d+\.\d+\.\d+)")),
    ("compose_cli", re.compile(r"\bcompose_cli\"?\s*[:=]\s*\"v(\d+\.\d+\.\d+)")),
    ("compose_go", re.compile(r"github\.com/compose-spec/compose-go/v2 v(\d+\.\d+\.\d+)")),
    ("compose_go", re.compile(r"\bcompose_go\"?\s*[:=]\s*\"v(\d+\.\d+\.\d+)")),
    ("buildx", re.compile(r"github\.com/docker/buildx/tree/v(\d+\.\d+\.\d+)")),
    ("buildx", re.compile(r"\bbuildx\"?\s*[:=]\s*\"v(\d+\.\d+\.\d+)")),
    ("dockerfile", re.compile(r"\bdockerfile/(\d+\.\d+\.\d+)\b")),
]
HEX = re.compile(r"(?<![0-9A-Za-z])([0-9a-f]{40}|[0-9a-f]{64})(?![0-9A-Za-z])")


def scan_files(root):
    seen = []
    for pattern in SCAN:
        for p in sorted(glob.glob(os.path.join(root, pattern))):
            if os.path.isfile(p) and not p.endswith("_test.go") and p not in seen:
                seen.append(p)
    return seen


def check(root=ROOT, sources=None):
    """Every mismatch as 'path:line: text'. An empty list means every pin agrees with SOURCES.md."""
    sources = sources or os.path.join(root, "reference", "SOURCES.md")
    with open(sources, encoding="utf-8") as f:
        src_text = f.read()
    pins = sources_pins(sources)
    problems = []
    for path in scan_files(root):
        rel = os.path.relpath(path, root).replace(os.sep, "/")
        with open(path, encoding="utf-8", errors="replace") as f:
            lines = f.read().splitlines()
        for n, line in enumerate(lines, 1):
            for m in HEX.finditer(line):
                tok = m.group(1)
                if tok not in src_text and tok not in NOT_PINS:
                    problems.append("%s:%d: hash %s is not in SOURCES.md" % (rel, n, tok))
            for key, rx in VERSION_RULES:
                for m in rx.finditer(line):
                    want = version(pins, key)
                    if m.group(1) != want:
                        problems.append("%s:%d: %s %s, SOURCES.md pins %s (%s)" % (rel, n, key, m.group(1), want, m.group(0)))
    # Generated copies of the pins block.
    want = inventory_pins(pins)
    want_specs = inventory_spec_pins(spec_pins(sources))
    inv_path = os.path.join(root, "reference", "inventory.json")
    if os.path.exists(inv_path):
        with open(inv_path, encoding="utf-8") as f:
            inv = json.load(f)
        got = inv.get("pins", {})
        for k in sorted(set(want) | set(got)):
            if got.get(k) != want.get(k):
                problems.append("reference/inventory.json: pins.%s %r, SOURCES.md gives %r (run Invoke-Build Inventory)" % (k, got.get(k), want.get(k)))
        got_specs = inv.get("spec_pins", {})
        for k in sorted(set(want_specs) | set(got_specs)):
            if got_specs.get(k) != want_specs.get(k):
                problems.append("reference/inventory.json: spec_pins.%s %r, SOURCES.md gives %r (run Invoke-Build Inventory)" % (k, got_specs.get(k), want_specs.get(k)))
        # The pinned spec file itself must hash to the pin.
        import hashlib
        for k, (ver, sha, _) in spec_pins(sources).items():
            local = os.path.join(root, "reference", "upstream", "hub", "docs.docker.com-hub-latest.yaml") if k == "hub_api" else None
            if local and os.path.exists(local):
                with open(local, "rb") as f:
                    got_sha = hashlib.sha256(f.read()).hexdigest()
                if got_sha != sha:
                    problems.append("%s: sha256 %s, SOURCES.md pins %s @ sha256 %s" % (os.path.relpath(local, root).replace(os.sep, "/"), got_sha, ver, sha))
    cg_path = os.path.join(root, "internal", "provider", "contract_gen.go")
    if os.path.exists(cg_path):
        with open(cg_path, encoding="utf-8") as f:
            body = f.read()
        m = re.search(r"var contractPins = map\[string\]string\{(.*?)\n\}", body, re.S)
        got = dict(re.findall(r"\"(\w+)\":\s*\"([^\"]*)\"", m.group(1))) if m else {}
        for k in sorted(set(want) | set(got)):
            if got.get(k) != want.get(k):
                problems.append("internal/provider/contract_gen.go: contractPins[%s] %r, SOURCES.md gives %r (run Invoke-Build Inventory, ContractGen)" % (k, got.get(k), want.get(k)))
    return problems


def main(argv):
    root = argv[1] if len(argv) > 1 else ROOT
    try:
        problems = check(root)
        pins = sources_pins(os.path.join(root, "reference", "SOURCES.md"))
        pins.update(spec_pins(os.path.join(root, "reference", "SOURCES.md")))
    except (PinError, OSError) as e:
        print("ERR %s" % e)
        return 2
    for p in problems:
        print("MISMATCH " + p)
    if problems:
        print("PinCheck: %d pin(s) differ from reference/SOURCES.md" % len(problems))
        return 1
    print("PinCheck: %d pins, %d files scanned, every pinned version and hash agrees with reference/SOURCES.md"
          % (len(pins), len(scan_files(root))))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
