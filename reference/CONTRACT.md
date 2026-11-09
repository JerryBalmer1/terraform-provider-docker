# Outputs contract

Every resource and data source of the provider exposes its full result as computed attributes, the same as any
normal Terraform resource, so a downstream provider (Ansible, a config-management module, anything) can consume
either the whole thing or a single field. Actions report one result object (see [Action results](#action-results)).

This file is the rule set. The per-item outputs are proposals that live in
[_gen/fragments/outputs/](_gen/fragments/outputs/) (one file per lane). [INVENTORY.md](INVENTORY.md#outputs) has
them per layer, [inventory.json](inventory.json) has them in full (types, sources, field maps), and
[_gen/SCHEMA.md](_gen/SCHEMA.md#outputs-schema-fragmentsoutputslanejson) defines the JSON shape. To change an output,
edit the lane's file and run `Invoke-Build Inventory`. `Invoke-Build InventoryValidate` (part of the default task)
checks the rules below.

## The six categories

Each output has `name`, `tf_type`, `category`, `source`, `known_after_apply` and `sensitive`.

| category | what it is | examples |
|---|---|---|
| **raw** | The whole result as one string, serialized from the structured output where possible. | `dockerfile` (rendered), `compose_yaml` (normalized, as `docker compose config` prints it), `stack_yaml`, `inspect_json` (the exact Engine API inspect response), `bake_hcl` / `bake_json`, `manifest_json` |
| **structured** | A nested object mirroring the upstream schema, so every field is addressable. | `stages[0].base_image`, `project.services["web"].ports`, `inspect.spec.task_template.container_spec.image` |
| **result** | The outcome of running an action, or a descriptive value about how the data was produced, not the identity or state of a lasting object. Every structured output of an action is a result, and every action has at least one (V6). | an action's single `result` object (prune `space_reclaimed`, exec/run `exit_code`, container `state`, service `update_status`, `archive_size`, commit `changes`); on data sources `created`, `fidelity` |
| **identity** | Stable handoff values. | `id`, `name`, `image_ref` (repo:tag), `image_digest_ref` (repo@sha256:…), `digest`, `platforms`, `labels` |
| **version** | Which versions produced or describe the result. | see [Version metadata](#version-metadata) |
| **hash** | `sha256` of a raw output, so a consumer can detect change without diffing the text. | `content_sha256` when the item has one raw output; `<raw>_sha256` for each raw output when it has two or more (Q34) |

Rules:
- Raw and structured describe the same data. The raw output names the structured one it serializes (`serializes`).
- Every raw output has exactly one hash output (`hash_of`).
- Sensitivity is per field. The secret channels are container/service/task/plugin env, image config env, volume
  driver options, build args / secrets / ssh / inline Dockerfiles, ENV values and ARG defaults (and image history
  `created_by`, which records them), compose `environment` / `env_file` / environment-sourced secrets and configs /
  config `content`, build provenance and attestations, join tokens, unlock keys, registry auth, logs and exec/run
  output. Command lines (args, cmd, entrypoint, process lists) are not a secret channel.
  - A raw output (one string) is `sensitive` when any secret channel can reach it.
  - A structured or result output is not sensitive as a whole: each FIELD that is a secret channel carries
    `sensitive: true`, so harmless fields (image, ports, names) stay readable in plans. A `dynamic` field cannot hide
    nested values, so the whole field is marked. Only an output whose every field is a secret (join tokens) is
    `sensitive` as a whole, and it says why in `sensitive_reason` (rule V5).
  - What stays readable on purpose is listed under [Known exposures](#known-exposures).
  - Hashes are never sensitive.
- `known_after_apply` is true for values computed by create/update or by the action run, and false for inputs
  echoed back and for data sources that read existing objects.

## Naming

- Attribute names are snake_case, derived from the upstream name: `HostConfig` → `host_config`,
  `IPAddress` → `ip_address`, `Id`/`ID` → `id`, `EnableIPv6` → `enable_ipv6`. Acronym runs stay whole
  (`LinkLocalIPs` → `link_local_ips`, `NanoCPUs` → `nano_cpus`).
- Logs: every logs item (`docker_container_logs`, `docker_service_logs`, `docker_task_logs`, `docker_compose_logs`) has the
  structured `log_entries`, `list(object({timestamp=string, stream=string, source=string, message=string}))`, and the
  raw `logs` that serializes it, one line per entry `<timestamp> <stream> <source> <message>`, hashed as
  `content_sha256`.
  - `timestamp` is RFC 3339 nano from the Engine's `timestamps=true`.
  - `stream` is `stdout` or `stderr`, or `combined` when the container has a TTY: the Engine merges the two streams
    at the source, so they cannot be separated.
  - `source` is whatever produced the line: the container name, the service name, the task ID, or
    `<compose service>.<container name>`.
  - Only `message` is sensitive; `timestamp`, `stream` and `source` stay readable in plans.
  - Every logs item takes a `tail` input (lines per container / service / task, default 1000) and a `since` input
    (RFC 3339 or a duration such as `42m`), recorded as `inputs` on the item, so state cannot grow without bound
    (rule L1).
- Compose keys are already snake_case (hyphens become `_`). Dockerfile flags drop the dashes
  (`--keep-git-dir` → `keep_git_dir`).
- Each structured output records the full upstream → snake_case mapping in its `fields`, so no mapping is
  implicit. Where `fields_complete` is false, the levels below follow the rule above from the cited pointer.
- Sources are pinned: swagger pointers into `upstream/engine-api/api/swagger.yaml` (API 1.56), compose-spec
  pointers (`#/$defs/...`) into `upstream/compose-spec/schema/compose-spec.json` (or `compose-go-embedded.json` where the
  two differ, e.g. `mem_limit` / `memswap_limit` / `shm_size` types, `jobs.*.extends`), Dockerfile reference anchors,
  and CLI `--format` fields.

## Version metadata

| output | meaning | source |
|---|---|---|
| `engine_api_version` | The Engine API version negotiated with the daemon | `GET /version` → `#/definitions/SystemVersion` (`ApiVersion`) |
| `docker_server_version` | The daemon's version | `GET /version` → `SystemVersion.Version` |
| `compose_spec_version` | The compose-spec commit the provider follows | SOURCES.md pin `compose_spec` (main @ 3a4a699) |
| `compose_go_version` | The compose-go library that parses and validates | SOURCES.md pin `compose_go` (v2.16.2) |
| `dockerfile_syntax` | The `# syntax=` value, or the default frontend when there is none | Dockerfile reference `#syntax`; pin `dockerfile` (1.28.0) |
| `buildx_version` | The buildx release the bake/imagetools model follows | SOURCES.md pin `buildx` (v0.38.2) |
| `buildkit_version` | The BuildKit version a builder node runs | `docker buildx inspect` node `BuildkitVersion` |
| `distribution_api_version` | The registry's API version, for items that talk to a registry directly | `Docker-Distribution-API-Version` response header |
| `docker_cli_version` | The docker CLI binary an item runs; allowed only on items with `uses_cli: true` | `docker version` client `Version` |

The authoritative list is the version registry in [_gen/SCHEMA.md](_gen/SCHEMA.md); this table mirrors it. Any
`<component>_version` name registered there with its source may use the version category, and an unregistered name
fails (rule V3). Every item says whether it runs the docker CLI (`uses_cli`). None does today, since the provider
never shells out, so no item carries `docker_cli_version`. The cli-only types (stack, manifest) reproduce CLI
behaviour through the Engine and registry APIs instead. A swarm object's `Version.Index` is an identity value
(`version_index`), not version metadata.

## Action results

Terraform 1.14 actions have no computed attributes, so an action cannot hand values back into configuration or state.
From Pass 5 on (DECISIONS.md Q35, Q43):

- Every action has exactly one `result` output: the structured API response body (for composite actions, the merged
  responses), field by field like any structured output.
- The **result object** of an action is all of its outputs keyed by output name: `result`, the identity and version
  outputs, raw outputs such as exec `stdout` / `stderr` (Q33), and their hashes. Compose exec / run report
  `{exit_code, stdout, stderr}` this way: `result.exit_code` beside the raw `stdout` and `stderr`.
- Every progress message the action sends at the end carries the result object as JSON, with sensitive fields shown as
  `(sensitive)`.
- Every action takes an optional `result_path` input. After a successful run the action writes the result object to
  that local file as JSON: UTF-8, keys sorted at every level, a trailing newline, sensitive fields written in full
  (the file is the user's own). Nothing is written when the run fails.
- A later Terraform run reads it with `jsondecode(data.local_file.x.content)` (hashicorp/local), e.g.
  `jsondecode(data.local_file.prune.content).result.space_reclaimed`.
- The shared code is `internal/provider/action_result.go`. Lanes do not format results themselves.

## Kinds outside the contract

List resources and provider functions have no outputs in the inventory and are not checked by V1-V6. A list resource
returns the state of the managed resource it lists, which carries that resource's contract outputs. A function
returns a computed value and touches no Docker object.

## Validation rules

| rule | check |
|---|---|
| V1 | Every non-skipped item has at least one identity and one version output. |
| V2 | Every resource and data source has a raw and a structured output, or a written `no_raw_reason` / `no_structured_reason`. |
| V3 | Version outputs use only names in the SCHEMA.md registry; `docker_cli_version` only on items with `uses_cli: true`. |
| V4 | Every structured output, and every object-typed result output, has a non-empty `fields` mapping. |
| V5 | A structured or result output is `sensitive` as a whole only with a `sensitive_reason`; otherwise its secret-carrying fields are marked. |
| V6 | Every action has at least one `result` output (Pass 5: exactly one, named `result`). Resources and data sources need not have one. |
| L1 | Every logs item has a `tail` and/or `since` input, and a `tail` input has a positive default limit. |

A violation fails `Invoke-Build InventoryValidate` unless [_gen/allowed-violations.txt](_gen/allowed-violations.txt)
lists it with a reason. merge.py separately fails on structural errors: unknown or duplicate ids, bad categories or
types, a raw output without a hash, a sensitive hash, a field marked sensitive inside an output that is already
sensitive as a whole.

## Known exposures

These values stay readable in plans by decision, not oversight:

- **Command lines are not secret.** Container and service args / cmd / entrypoint, compose `ps` commands and `top`
  process lists stay plain, because hiding them would hide the most useful debugging fields of every container, and
  a secret belongs in env or a secret, not on a command line.
- **Compose `secrets` and `configs` maps are not hidden as a whole.** Only their `environment` and `content` children
  are marked, because a whole-map mark hid every name, file path and target along with the one field that carries
  the value.
- **Interpolated `${VAR}` values are not hidden.** A secret interpolated into a field that is not a secret channel
  shows in plans, because the provider sees only the interpolated result and cannot tell which plain field held a
  secret; secrets belong in environment, env_file or secrets.

## Handoff

These are the provider-neutral values a downstream provider should consume. They are plain strings or plain objects,
with no Terraform- or Docker-provider-specific types:

| value | from | use downstream |
|---|---|---|
| `dockerfile` (text) + `dockerfile_sha256` | `data.docker_dockerfile`, `docker_image` | Audit or re-build elsewhere; the hash detects drift. |
| `image_digest_ref` (repo@sha256:…) | `docker_image`, `docker_registry_image`, `data.docker_image`, `data.docker_registry_image`, `docker_service` | Pin deployments by digest, never by tag. |
| `image_ref` (repo:tag), `platforms`, `labels` | the same image types | Human-readable reference, platform selection, metadata. |
| `compose_yaml` + `content_sha256` | `docker_compose`, `data.docker_compose_config` | Hand the normalized project to another runner. |
| `stack_yaml` + `content_sha256` | `docker_stack` | Hand the rendered stack to `docker stack deploy` elsewhere. |
| structured objects (`stages`, `project`, `inspect`) | every resource / data source | Read one field without parsing text. |
| version outputs | every item | Record which engine/spec/frontend produced the value. |

`data.docker_image` and `data.docker_registry_image` share one identity/version/structured shape (Lane D), so a
consumer can swap the local lookup for the registry lookup. Fields only one side can fill (`labels`, `created`,
`config` are local-only) are null on the other.

## Worked example (illustrative: not compiled; the attributes are proposals)

```hcl
# Illustrative only. data.docker_dockerfile and most attributes below are contract proposals, not shipped schema.

data "docker_dockerfile" "app" {
  git = {
    url     = "https://github.com/example/app.git"
    ref     = "v1.4.2"
    subpath = "Dockerfile"
  }
}

resource "docker_image" "app" {
  name = "registry.example.com/app:1.4.2"
  build {
    context    = "${path.module}/app"
    dockerfile = "Dockerfile"
  }
  # Rebuild when the parsed Dockerfile changes.
  triggers = {
    dockerfile_sha256 = data.docker_dockerfile.app.dockerfile_sha256
  }
}

# Placeholder downstream module (e.g. one wrapping an Ansible or other config-management provider).
module "deploy" {
  source = "./modules/downstream-placeholder"

  image_digest_ref = docker_image.app.image_digest_ref   # repo@sha256:..., pin by digest
  dockerfile       = data.docker_dockerfile.app.dockerfile # sensitive: may carry ARG/ENV values
  dockerfile_sha256 = data.docker_dockerfile.app.dockerfile_sha256
  base_images      = [for s in data.docker_dockerfile.app.stages : s.base_image]
  versions = {
    dockerfile_syntax     = data.docker_dockerfile.app.dockerfile_syntax
    engine_api_version    = docker_image.app.engine_api_version
    docker_server_version = docker_image.app.docker_server_version
  }
}
```
