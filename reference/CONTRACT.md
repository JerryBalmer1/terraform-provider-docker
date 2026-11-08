# Outputs contract

Every resource and data source of the provider exposes its full result as computed attributes, the same as any
normal Terraform resource, so a downstream provider (Ansible, a config-management module, anything) can consume
either the whole thing or a single field. Actions list the values they return or change.

This file is the rule set. The per-item outputs are proposals that live in
[_gen/fragments/outputs/](_gen/fragments/outputs/) (one file per lane). [INVENTORY.md](INVENTORY.md#outputs) has
them per layer, [inventory.json](inventory.json) has them in full (types, sources, field maps), and
[_gen/SCHEMA.md](_gen/SCHEMA.md#outputs-schema-fragmentsoutputslanejson) defines the JSON shape. To change an output,
edit the lane's file and run `Invoke-Build Inventory`. `Invoke-Build InventoryValidate` (part of the default task)
checks the rules below.

## The five categories

Each output has `name`, `tf_type`, `category`, `source`, `known_after_apply` and `sensitive`.

| category | what it is | examples |
|---|---|---|
| **raw** | The whole result as one string, serialized from the structured output where possible. | `dockerfile` (rendered), `compose_yaml` (normalized, as `docker compose config` prints it), `stack_yaml`, `inspect_json` (the exact Engine API inspect response), `bake_hcl` / `bake_json`, `manifest_json` |
| **structured** | A nested object mirroring the upstream schema, so every field is addressable. | `stages[0].base_image`, `project.services["web"].ports`, `inspect.spec.task_template.container_spec.image` |
| **identity** | Stable handoff values. | `id`, `name`, `image_ref` (repo:tag), `image_digest_ref` (repo@sha256:…), `digest`, `platforms`, `labels` |
| **version** | Which versions produced or describe the result. | see [Version metadata](#version-metadata) |
| **hash** | `sha256` of a raw output, so a consumer can detect change without diffing the text. | `content_sha256`, or `<raw>_sha256` when an item has more than one raw output |

Rules:
- Raw and structured describe the same data. The raw output names the structured one it serializes (`serializes`).
- Every raw output has exactly one hash output (`hash_of`).
- Anything a secret value could leak through is `sensitive`: container/service env, build args, ENV/ARG values,
  secret data, join tokens, unlock keys, registry auth, logs and exec output, whole compose projects (interpolated
  variables). Hashes are never sensitive.
- `known_after_apply` is true for values computed by create/update or by the action run, and false for inputs
  echoed back and for data sources that read existing objects.

## Naming

- Attribute names are snake_case, derived from the upstream name: `HostConfig` → `host_config`,
  `IPAddress` → `ip_address`, `Id`/`ID` → `id`, `EnableIPv6` → `enable_ipv6`. Acronym runs stay whole
  (`LinkLocalIPs` → `link_local_ips`, `NanoCPUs` → `nano_cpus`).
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

Only these six names may use the version category (rule V3). A swarm object's `Version.Index` is an identity value
(`version_index`), not version metadata.

## Validation rules

| rule | check |
|---|---|
| V1 | Every non-skipped item has at least one identity and one version output. |
| V2 | Every resource and data source has a raw and a structured output, or a written `no_raw_reason` / `no_structured_reason`. |
| V3 | Version outputs use only the six names above. |
| V4 | Every structured output has a non-empty `fields` mapping. |

A violation fails `Invoke-Build InventoryValidate` unless [_gen/allowed-violations.txt](_gen/allowed-violations.txt)
lists it with a reason. merge.py separately fails on structural errors: unknown or duplicate ids, bad categories or
types, a raw output without a hash.

## Handoff

These are the provider-neutral values a downstream provider should consume. They are plain strings or plain objects,
with no Terraform- or Docker-provider-specific types:

| value | from | use downstream |
|---|---|---|
| `dockerfile` (text) + `content_sha256` | `data.docker_dockerfile`, `docker_image` | Audit or re-build elsewhere; the hash detects drift. |
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
    dockerfile_sha256 = data.docker_dockerfile.app.content_sha256
  }
}

# Placeholder downstream module (e.g. one wrapping an Ansible or other config-management provider).
module "deploy" {
  source = "./modules/downstream-placeholder"

  image_digest_ref = docker_image.app.image_digest_ref   # repo@sha256:..., pin by digest
  dockerfile       = data.docker_dockerfile.app.dockerfile # sensitive: may carry ARG/ENV values
  dockerfile_sha256 = data.docker_dockerfile.app.content_sha256
  base_images      = [for s in data.docker_dockerfile.app.stages : s.base_image]
  versions = {
    dockerfile_syntax     = data.docker_dockerfile.app.dockerfile_syntax
    engine_api_version    = docker_image.app.engine_api_version
    docker_server_version = docker_image.app.docker_server_version
  }
}
```
