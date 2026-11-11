# Inventory fragment schema

Every file in `reference/_gen/fragments/` (except `z3-cli-map.json`, the CLI coverage map) follows this schema.
`merge.py` validates it; see README.md in this folder.

Harvested sources: `reference/upstream/` (see `reference/SOURCES.md`).
Existing provider types: generated from this inventory (internal/provider/registry_gen.go, tools/registrygen) since Pass 5. The harvest-era
  `existing` / hand-set `status` fields are gone; merge.py computes status (below).

A fragment is ONE JSON file (UTF-8, LF), shaped:

{
  "lane": "Z1",
  "notes": ["free-text caveats"],
  "naming_questions": ["one-line question for the user, with the options"],
  "items": [ ITEM, ... ]
}

ITEM:
{
  "id": "<layer>:<stable-slug>",            // unique, e.g. "engine:container", "swarm:service-rollback", "dockerfile:RUN"
  "layer": "engine|swarm|compose|dockerfile|buildx|cli-only",
  "kind": "resource|data_source|action|ephemeral_resource|list_resource|function|dockerfile_block|skip",
  "name": "docker_...",                     // proposed Terraform type name; null only for skip
  "update": "in_place|replace",             // resources only: in_place if an update endpoint/mechanism exists, else replace. Omit otherwise.
  "summary": "one line",
  "sources": [                              // every source reference this item came from
    {"type": "swagger", "operationId": "ContainerCreate", "method": "POST", "path": "/containers/create"},
    {"type": "schema", "file": "upstream/compose-spec/schema/compose-spec.json", "pointer": "#/properties/services"},
    {"type": "cli", "command": "docker container create", "file": "upstream/docker-cli/docs/reference/commandline/container_create.md"},
    {"type": "doc", "file": "upstream/dockerfile/frontend/dockerfile/docs/reference.md", "anchor": "#run---mount"}
  ],
  "min_api": "1.41",                        // only where a source states it (swagger text, api/docs/CHANGELOG.md); else null
  // status / status_note / registered / stub_files are COMPUTED by merge.py (Pass 5) and must not appear in a
  // fragment: missing = not registered in internal/provider/registry_gen.go or a STUB(pass5) stub; partial =
  // registered with a `// PARTIAL(pass5) <id> <note>` marker in internal/provider/*.go (a documented sub-feature absent or a no-op; the note is required); exists = registered, no marker. validate.py S1 fails on missing, never on partial.
  "decisions": ["Q1"],                      // optional: the DECISIONS.md questions that shaped the item
  "previous_name": "docker_logs",           // optional: the name before Pass 5 (skip_previous_name on items that became skips)
  "cli_only": false,                        // true when it appears in a CLI but has NO Engine API endpoint (client-side: compose, stack deploy, context, trust, manifest, buildx bake...)
  "skip_reason": "one line",                // required for skip
  "collision": "text if the proposed name collides or is confusable with another name (existing or proposed)",  // optional
  "flags": [ {"name": "--mount=type=cache", "options": ["id","target","sharing","..."], "anchor": "#run---mounttypecache"} ]  // dockerfile_block only (optional elsewhere)
}

Path strings in sources are relative to reference/ (start with "upstream/").

Rules:
- resource = has a create AND a delete/remove path. Group all ops of one object (create, inspect, update, delete, plus sub-ops that edit the same object) into ONE resource item; list sources for each op.
- data_source = read-only inspect/list with no lifecycle. A list endpoint becomes a plural data source (docker_containers); a single-object inspect without lifecycle becomes a singular data source. If an object has a resource, its inspect op still usually deserves a singular data source item (looking up objects not managed by TF) — make it a separate data_source item.
- action = imperative op outside CRUD (restart, kill, pause/unpause, exec, prune, push, pull, tag, commit, swarm init/join/leave/unlock, node promote/demote, service rollback/scale, compose up/down/restart, build). Terraform plugin framework "actions".
- skip = deprecated, session/streaming-only, attach/websocket, interactive, pure-client UI etc. One-line reason.
- Naming prefixes: docker_*, docker_swarm_*, docker_compose_*, docker_dockerfile_*, docker_buildx_*. Prefer existing names where the type already exists. Don't invent a new name for something that exists.
- status is computed (see above). The registration itself is generated from the inventory (tools/registrygen ->
  internal/provider/registry_gen.go), so adding an item with a new (kind, name) registers a type: run
  `Invoke-Build Inventory, RegistryGen`, then implement the constructor (`New<CamelName><Kind>`, see
  tools/registrygen) or let `go run ./tools/registrygen -stubs` write a stub.
- dockerfile_block items name a nested block or attribute of data.docker_dockerfile (`docker_dockerfile.run`); they are
  not registered types. Their status follows data.docker_dockerfile plus their own markers.

# Outputs schema (`fragments/outputs/<lane>.json`)

The outputs contract ([../CONTRACT.md](../CONTRACT.md)) gives every non-skipped item an `outputs` array.
Each lane owns one file in `fragments/outputs/`. merge.py joins it onto the items by id and exits 1 when:
an id is unknown or skipped; one id appears in two outputs files; an output is missing a required field, has a bad
category, `tf_type` or source type, or is not snake_case; a hash output is sensitive; `sensitive_reason` comes without
`sensitive: true`; a FIELD `sensitive` is not a bool or sits inside an output that is already sensitive as a whole; two outputs of one item share a name; a raw output has no
hash output; a `hash_of` does not name a raw output of the same item; an item-level key is not in the list below.

```
{
  "lane": "A",
  "notes": ["free-text caveats"],
  "naming_questions": ["one-line question, with the options"],   // appended to INVENTORY.md after the existing ones
  "allowed_violations": [ {"id": "<item id>", "rule": "V1..V6|L1", "reason": "why"} ],  // proposals; Lane Z copies accepted ones into allowed-violations.txt
  "items": {
    "<item id>": {
      "outputs": [ OUTPUT, ... ],
      "no_raw_reason": "why there is no raw output",               // optional; satisfies V2 for raw
      "no_structured_reason": "why there is no structured output", // optional; satisfies V2 for structured
      "outputs_status": "exists|partial|missing",                  // optional: do today's provider attributes cover these outputs?
      "outputs_note": "what exists today / what is missing",       // optional
      "uses_cli": false,                                           // required: true only if the item runs the docker CLI
                                                                   //   binary (the provider never does today); gates docker_cli_version
      "inputs": [ INPUT, ... ]                                     // optional; logs items must record tail/since here (rule L1)
    }
  }
}

OUTPUT:
{
  "name": "inspect_json",                 // snake_case; the Terraform attribute name
  "tf_type": "string",                    // string | number | bool | dynamic | list(T) | set(T) | map(T) | object({...})
  "category": "raw|structured|result|identity|version|hash",
  "source": SOURCE,
  "known_after_apply": true,              // unknown at plan time (computed by create/update or by a read that depends on unknown inputs)
  "sensitive": false,                     // raw: true when a secret value could leak through it. structured/result: false, with the
                                          //   secret-carrying FIELDs marked instead; true only with sensitive_reason (rule V5)
  "sensitive_reason": "why",              // structured/result only, with sensitive: true: every field is a secret (e.g. join tokens)
  "description": "one line",              // optional
  "serializes": "inspect",                // raw only, optional: the structured output this raw output is the serialization of
  "hash_of": "inspect_json",              // hash only, required: the raw output it hashes
  "fields": [ FIELD, ... ],               // structured (and object-typed result) only, required (V4): the upstream -> snake_case mapping
  "fields_complete": true,                // structured/result only: false when nested levels below `fields` are left to the mechanical rule
  "provided": false                       // optional: true when the provider exposes this attribute today
}

FIELD:
{ "name": "host_config", "upstream": "HostConfig", "tf_type": "object({...})", "pointer": "#/definitions/HostConfig",
  "sensitive": true,                      // optional: this field (and everything under it) is a secret channel; only in an output that is not sensitive
  "fields": [ FIELD, ... ] }              // nested fields optional; tf_type "object({...})" / "list(object({...}))" / "map(object({...}))" means "see fields"
                                          // tf_type "object({})" is an empty object (swagger `enum: [{}]`, e.g. ExposedPorts values)

Swagger-derived shapes (Pass 6c): for every "<item id>/<output>" listed in tools/contractgen/swagger_map.json,
contractgen replaces the output's tf_type and field tree with the shape of the named swagger definition when it
generates contract_gen.go (object with additionalProperties -> map(T), array -> list(T), enum -> string, free-form ->
dynamic). The fragment tree still supplies names, descriptions and `sensitive` flags of the fields it maps (matched by
`upstream`); swagger_map.json "sensitive" adds "Definition.Property" secret channels. inventory.json keeps the
fragment trees; contract_gen.go and the provider schema carry the derived ones.

INPUT (an argument the outputs depend on; today only the logs bounds):
{ "name": "tail", "tf_type": "number", "default": 1000, "upstream": "tail",
  "source": SOURCE,                       // the Engine query parameter or CLI flag it maps to
  "description": "one line" }             // "default": null means no default

SOURCE (paths relative to reference/):
  {"type": "swagger", "file": "upstream/engine-api/api/swagger.yaml", "pointer": "#/definitions/ContainerInspectResponse"}
  {"type": "schema",  "file": "upstream/compose-spec/schema/compose-spec.json", "pointer": "#/definitions/service"}
  {"type": "doc",     "file": "upstream/dockerfile/frontend/dockerfile/docs/reference.md", "anchor": "#from"}
  {"type": "cli",     "command": "docker buildx inspect", "format_field": "{{.Name}}", "file": "upstream/..."}  // a CLI --format / inspect field
  {"type": "sources_md", "pin": "compose_spec"}       // a pin from SOURCES.md / inventory.json "pins"
  {"type": "derived", "from": "inspect_json", "how": "sha256 of the UTF-8 bytes"}
```

Naming rules:
- snake_case from the upstream name: `HostConfig` -> `host_config`, `IPAddress` -> `ip_address`, `Id`/`ID` -> `id`,
  `EnableIPv6` -> `enable_ipv6`; compose keys are already snake_case (hyphens become `_`); Dockerfile flags drop the
  dashes (`--keep-git-dir` -> `keep_git_dir`). `fields` always records both names, so no mapping is implicit.
- raw outputs: `dockerfile`, `compose_yaml`, `stack_yaml`, `inspect_json`, `bake_hcl`, `bake_json`, or `<thing>_json`.
  Logs items have raw `logs` (text, one line per entry: `<timestamp> <stream> <source> <message>`) serializing the
  structured `log_entries`, `list(object({timestamp=string, stream=string, source=string, message=string}))`:
  timestamp RFC 3339 nano (timestamps=true); stream `stdout` | `stderr`, or `combined` for a TTY (the Engine merges
  the streams at the source); source the container name, service name, task ID or `<compose service>.<container>`
  that produced the line; only `message` is sensitive. There is no `logs_json`.
- hash outputs: `content_sha256` when the item has one raw output, else `<raw name>_sha256`.
- version outputs use only names registered below (rule V3). A new `<component>_version` name is added by a row
  here, with its source; an unregistered name fails. "only on" names an item flag that must be true.

<!-- version-registry -->
| name | source | only on |
|---|---|---|
| `engine_api_version` | `GET /version` `ApiVersion`, negotiated (min of the client maximum and the daemon's) | - |
| `docker_server_version` | `GET /version` `Version` | - |
| `compose_spec_version` | SOURCES.md pin `compose_spec` (the compose-spec commit followed) | - |
| `compose_go_version` | SOURCES.md pin `compose_go` (the library that parses and validates) | - |
| `dockerfile_syntax` | the `# syntax=` value, else the default frontend (SOURCES.md pin `dockerfile`) | - |
| `buildx_version` | SOURCES.md pin `buildx` (the release the bake / imagetools model follows) | - |
| `buildkit_version` | builder inspect: `docker buildx inspect` node `BuildkitVersion` (BuildKit's ListWorkers / Info) | - |
| `distribution_api_version` | the registry's `Docker-Distribution-API-Version` response header (e.g. `registry/2.0`), for items that talk to a registry directly | - |
| `docker_cli_version` | `docker version` client `Version` of the docker CLI binary the item runs | `uses_cli` |
<!-- /version-registry -->

- result outputs: the outcome of an action (every structured output of an action) or a descriptive value about how
  the data was produced (`created`, `fidelity`, `reconstructed`), not the identity or state of a lasting object.
- identity outputs: `id`, `name`, `image_ref` (repo:tag), `image_digest_ref` (repo@sha256:...), `digest`, `platforms`,
  `labels`, or another stable handoff value.

Contract rules V1-V6 and L1 (checked by `Invoke-Build InventoryValidate`, reference/_gen/validate.py; exceptions only through
`reference/_gen/allowed-violations.txt`, one `<id><TAB><rule><TAB><reason>` per line):
- V1: every non-skipped item has at least one identity output and at least one version output.
- V2: every resource and data source has a raw and a structured output, or `no_raw_reason` / `no_structured_reason`.
- V3: every version output name is registered in the version registry above; a name with an "only on" flag
  (`docker_cli_version`: `uses_cli`) appears only on items where that flag is true.
- V4: every structured output, and every object-typed result output, has a non-empty `fields` list.
- V5: a structured or result output with `sensitive: true` has a `sensitive_reason`; otherwise mark the secret-carrying fields.
- V6: every action has at least one `result` output. Resources and data sources need not have one.
- L1: every logs item (an item with a `logs` or `log_entries` output) has a `tail` and/or `since` input; a `tail`
  input has a positive integer default, so state cannot grow without bound.
