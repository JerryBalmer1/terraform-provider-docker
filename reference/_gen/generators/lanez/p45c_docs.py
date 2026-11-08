"""Pass 4.5 Lane Z corrections: the prose edits that go with p45c.py (run once, from the repo root).

    python -I -B reference/_gen/generators/lanez/p45c_docs.py

Edits CONTRACT.md, _gen/README.md, _gen/allowed-violations.txt and the InventoryValidate synopsis in
terraform-provider-docker.build.ps1. Each replacement must match exactly once, so a second run fails.
(SCHEMA.md, merge.py and validate.py were edited directly in the same pass; see BUILD_REPORT.md Pass 4.5.)
"""


def edit(p, pairs):
    with open(p, encoding="utf-8") as f:
        s = f.read()
    for a, b in pairs:
        assert s.count(a) == 1, (p, a[:60])
        s = s.replace(a, b)
    with open(p, "w", encoding="utf-8", newline="\n") as f:
        f.write(s)


edit("reference/CONTRACT.md", [
    ("| **result** | The outcome of running an action, or a descriptive value about how the data was produced, not the identity or state of a lasting object. Every structured output of an action is a result. | prune `result` (`space_reclaimed`), container `state` after start/stop, exec/run `result` (`exit_code`, `stdout`), service `update_status`, `created`, `fidelity`, `reconstructed` |",
     "| **result** | The outcome of running an action, or a descriptive value about how the data was produced, not the identity or state of a lasting object. Every structured output of an action is a result, and every action has at least one (V6). | prune `result` (`space_reclaimed`), container `state` after start/stop, compose `container_states` after up/stop, exec/run `result` (`exit_code`, `stdout`), service `update_status`, `archive_size`, commit `changes`, `created`, `fidelity`, `reconstructed` |"),
    ("  - A secret interpolated (`${VAR}`) into a field that is not a secret channel is not hidden; secrets belong in env\n"
     "    or secrets.\n",
     "  - What stays readable on purpose is listed under [Known exposures](#known-exposures).\n"),
    ("- Logs: every logs item has two raw outputs, `logs` (plain text, as the CLI prints it) and `logs_json` (a JSON array\n"
     "  of `{timestamp, stream, line}` entries, plus service/container for compose), hashed as `logs_sha256` and\n"
     "  `logs_json_sha256`.",
     "- Logs: every logs item (`docker_logs`, `docker_service_logs`, `docker_task_logs`, `docker_compose_logs`) has the\n"
     "  structured `log_entries`, `list(object({timestamp=string, stream=string, source=string, message=string}))`, and the\n"
     "  raw `logs` that serializes it, one line per entry `<timestamp> <stream> <source> <message>`, hashed as\n"
     "  `content_sha256`.\n"
     "  - `timestamp` is RFC 3339 nano from the Engine's `timestamps=true`.\n"
     "  - `stream` is `stdout` or `stderr`, or `combined` when the container has a TTY: the Engine merges the two streams\n"
     "    at the source, so they cannot be separated.\n"
     "  - `source` is whatever produced the line: the container name, the service name, the task ID, or\n"
     "    `<compose service>.<container name>`.\n"
     "  - Only `message` is sensitive; `timestamp`, `stream` and `source` stay readable in plans.\n"
     "  - Every logs item takes a `tail` input (lines per container / service / task, default 1000) and a `since` input\n"
     "    (RFC 3339 or a duration such as `42m`), recorded as `inputs` on the item, so state cannot grow without bound\n"
     "    (rule L1)."),
    ("| `docker_cli_version` | The docker CLI release whose client-side behaviour a cli-only type reproduces (stack, manifest) | SOURCES.md pin `docker_cli` (v29.5.3) |\n"
     "\n"
     "Only these eight names may use the version category (rule V3). A swarm object's `Version.Index` is an identity value\n"
     "(`version_index`), not version metadata.",
     "| `docker_cli_version` | The docker CLI binary an item runs; allowed only on items with `uses_cli: true` | `docker version` client `Version` |\n"
     "\n"
     "The authoritative list is the version registry in [_gen/SCHEMA.md](_gen/SCHEMA.md); this table mirrors it. Any\n"
     "`<component>_version` name registered there with its source may use the version category, and an unregistered name\n"
     "fails (rule V3). Every item says whether it runs the docker CLI (`uses_cli`). None does today, since the provider\n"
     "never shells out, so no item carries `docker_cli_version`. The cli-only types (stack, manifest) reproduce CLI\n"
     "behaviour through the Engine and registry APIs instead. A swarm object's `Version.Index` is an identity value\n"
     "(`version_index`), not version metadata."),
    ("| V3 | Version outputs use only the eight names above. |",
     "| V3 | Version outputs use only names in the SCHEMA.md registry; `docker_cli_version` only on items with `uses_cli: true`. |"),
    ("| V5 | A structured or result output is `sensitive` as a whole only with a `sensitive_reason`; otherwise its secret-carrying fields are marked. |",
     "| V5 | A structured or result output is `sensitive` as a whole only with a `sensitive_reason`; otherwise its secret-carrying fields are marked. |\n"
     "| V6 | Every action has at least one `result` output. Resources and data sources need not have one. |\n"
     "| L1 | Every logs item has a `tail` and/or `since` input, and a `tail` input has a positive default limit. |"),
    ("## Handoff",
     "## Known exposures\n"
     "\n"
     "These values stay readable in plans by decision, not oversight:\n"
     "\n"
     "- **Command lines are not secret.** Container and service args / cmd / entrypoint, compose `ps` commands and `top`\n"
     "  process lists stay plain, because hiding them would hide the most useful debugging fields of every container, and\n"
     "  a secret belongs in env or a secret, not on a command line.\n"
     "- **Compose `secrets` and `configs` maps are not hidden as a whole.** Only their `environment` and `content` children\n"
     "  are marked, because a whole-map mark hid every name, file path and target along with the one field that carries\n"
     "  the value.\n"
     "- **Interpolated `${VAR}` values are not hidden.** A secret interpolated into a field that is not a secret channel\n"
     "  shows in plans, because the provider sees only the interpolated result and cannot tell which plain field held a\n"
     "  secret; secrets belong in environment, env_file or secrets.\n"
     "\n"
     "## Handoff"),
])

edit("reference/_gen/README.md", [
    ("- `Invoke-Build InventoryValidate` runs `validate.py` (rules V1-V5) on the merged inventory.json.",
     "- `Invoke-Build InventoryValidate` runs `validate.py` (rules V1-V6 and L1; V3 reads the version registry in\n"
     "  SCHEMA.md) on the merged inventory.json."),
    ("Port the edit into the generator first if you want to keep using it.",
     "Port the edit into the generator first if you want to keep using it.\n"
     "\n"
     "`generators/lanez/` holds the Lane Z scripts that edit `fragments/outputs/*.json` in place. Each runs from\n"
     "`fragments/outputs/` with the script folder as its first argument, in this order, starting from the outputs files\n"
     "of commit 9df9acf (Lanes A-E, before Lane Z):\n"
     "\n"
     "| script | pass | what it does |\n"
     "|---|---|---|\n"
     "| `frag.py` | - | load/save helper (keeps each file's indent) |\n"
     "| `mech.py <dir> <inventory.json of 9df9acf>` | 4.5 Z | logs outputs, `result` category, version names |\n"
     "| `sens.py <dir>` | 4.5 Z | per-field sensitivity (V5) |\n"
     "| `desc.py <dir>` | 4.5 Z | description and note text for V5 |\n"
     "| `qs.py <dir>` | 4.5 Z | closes the decided questions, Z notes |\n"
     "| `p45c.py <dir>` | 4.5 Z corrections | `log_entries` + tail/since inputs, V6 results for 26 actions, `uses_cli`, docker_cli_version removed |\n"
     "\n"
     "e.g. `cd reference/_gen/fragments/outputs; python -I -B ../../generators/lanez/mech.py ../../generators/lanez <inv>`.\n"
     "Replayed in that order on 9df9acf's files, the first four reproduce the Lane Z commit (df79456) byte for byte, and\n"
     "`p45c.py` then gives the current files. `p45c_docs.py` (run from the repo root) made the matching prose edits.\n"
     "They are provenance: re-running one on the current files fails or doubles its edits."),
])

edit("reference/_gen/allowed-violations.txt", [
    ("# Outputs-contract violations that Invoke-Build InventoryValidate accepts (rules V1-V5, see SCHEMA.md / ../CONTRACT.md).",
     "# Outputs-contract violations that Invoke-Build InventoryValidate accepts (rules V1-V6 and L1, see SCHEMA.md / ../CONTRACT.md)."),
    ("# Pass 4.5 Lane Z: V5 added (per-field sensitivity); every item meets V1-V5 with no entries.",
     "# Pass 4.5 Lane Z: V5 added (per-field sensitivity); every item meets V1-V5 with no entries.\n"
     "# Pass 4.5 Lane Z corrections: V6 (action results), L1 (bounded logs) and the V3 version registry added; still no entries."),
])

edit("terraform-provider-docker.build.ps1", [
    ("# Synopsis: Inventory, then check the outputs contract (V1-V4, reference/_gen/SCHEMA.md); violations fail unless listed in reference/_gen/allowed-violations.txt.",
     "# Synopsis: Inventory, then check the outputs contract (V1 identity+version, V2 raw+structured, V3 registered version names + uses_cli, V4 fields, V5 per-field sensitivity, V6 action result, L1 bounded logs; reference/_gen/SCHEMA.md); violations fail unless listed in reference/_gen/allowed-violations.txt."),
])
print("ok")
