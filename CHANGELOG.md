# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Release workflow: the `sign` job no longer uploads the cosign bundle (`<name>_<version>_SHA256SUMS.sigstore.json`) to the GitHub release, which made the Terraform Registry reject v0.1.0 ("invalid file extension found: json") until the asset was deleted by hand. The bundle is now a workflow artifact (`cosign-bundle-vX.Y.Z`); the keyless signature and the SLSA provenance are unchanged, and the job's `contents` permission dropped to read. RELEASING.md says where the bundle lives and how to verify it.
- `reference/inventory.json` (and the generated `contractPins` copy) labelled the docker CLI pin v29.5.3 while `reference/SOURCES.md` pins v29.9.3.

### Added

- Release asset-name gate (`tools/releaseassets`, in `Invoke-Build ReleaseCheck` and `Release`): the files a release would upload must be exactly one zip per target, the manifest, the SHA256SUMS and its `.sig`, and no `release.yml` step may upload anything else to the release.
- Pin gate `Invoke-Build PinCheck` (part of `InventoryCheck`, so in the default task): `reference/SOURCES.md` is the single source of the upstream pins. `reference/_gen/merge.py` reads the inventory pins from it (`reference/_gen/pins.py`) instead of carrying a copy, and the gate fails when a pinned version or hash in a generator, the build scripts, `tools/apiwatch/pinned.json`, `go.mod`, the dind harness, `inventory.json` or `contract_gen.go` differs from it.
- `Invoke-Build RegistrySmoke`: after publishing, `terraform init` of `jerrybalmer1/docker` at the newest released version from the public registry; fails unless it installs and is signed by key ID `A18B1B39E1DFDD4C`. The last step of RELEASING.md, not part of PreRelease.

## [0.1.0] (2026-11-11)

The first release. This section covers build passes 5, 5.1, 5.2, 5.3, 6a, 6b, 6c, 6d and 6e (the log of each pass is in [BUILD_REPORT.md](BUILD_REPORT.md)). Pass 6e comes first, then 6d, 6c, 6b, 6a and 5.3; the sections after them are passes 5 to 5.2.

### Known issues

Each issue is listed in `reference/_gen/deferred.json` (kind `known_issue`, with a reproduction) and in a "Known issues" section of its docs page.

- `docker_buildx_builder` with `driver = "cloud"` (Docker Build Cloud) is implemented against fakes and not yet verified against the live service: the builder group is dialled at the default proxy, `buildx-version: v0.38.2` is sent, and results are loaded through the build session (buildx's "cloud pull" is not implemented). Affects Build Cloud users only; other drivers are not affected. No workaround needed to try it; `TestAccBuildxBuilderCloud_live` (`TF_ACC_BUILD_CLOUD=1`) verifies an account. [docs/resources/buildx_builder.md](docs/resources/buildx_builder.md), id `buildx-builder-cloud-unverified`.
- `docker_compose` (`mode = "local"`): a `pre_start` step with `per_replica: true` runs once per created replica and its container is always removed, while compose v5.7.0 rejects `per_replica: true` and keeps a failed step container. Affects projects that use `per_replica` and also run under `docker compose`. Workaround: leave `per_replica` unset in such projects; a failed step's last 20 log lines are in the apply error. [docs/resources/compose.md](docs/resources/compose.md), id `compose-pre-start-per-replica`.
- `docker_compose` (`mode = "local"`): for `use_api_socket: true` the `com.docker.compose.config-hash` label differs from the compose CLI's when the host has no registry credentials or the service sets `DOCKER_CONFIG`, so a container created by `docker compose up` is recreated by the next apply, and the other way round. Affects only projects managed by both the CLI and the provider; no workaround other than managing the project from one side. [docs/resources/compose.md](docs/resources/compose.md), id `compose-use-api-socket-config-hash`.
- `docker_compose` and `data.docker_compose_config`: a service whose `build.ssh` has an entry with a key path (`default: /home/me/.ssh/id_ed25519`) makes compose-go v2.16.2 write invalid JSON, so the structured `project` holds only `name` (every other field is null, with no diagnostic). `compose_yaml` and the deployment are not affected. Workaround: read `compose_yaml`, or use the SSH agent (`ssh: [default]`) instead of a key path. [docs/resources/compose.md](docs/resources/compose.md), [docs/data-sources/compose_config.md](docs/data-sources/compose_config.md), id `compose-build-ssh-project-dropped`.
- `data.docker_registry_image`: for a plain-HTTP registry on a host other than `localhost` or a loopback address (a daemon `insecure-registries` entry served over http), `config`, `labels` and `created` stay null with the warning "Image config not read from registry": the provider reads the image config from the registry itself and uses plain HTTP only for loopback registries. `digest`, `platforms` and `inspect_json` are filled. Workaround: serve the registry over TLS, or use `data.docker_image` on a pulled image. [docs/data-sources/registry_image.md](docs/data-sources/registry_image.md), id `registry-image-config-plain-http-remote`.

### Pass 6e

#### Changed

- Reference sources re-pinned: docker/compose v5.7.0 -> v5.7.1 (the compose CLI reference is unchanged; the harness's `docker compose` binary for TestAccComposeParity follows) and the Dockerfile reference dockerfile/1.28.0 -> dockerfile/1.29.0. The computed `dockerfile_syntax` default (`docker_image`, `data.docker_dockerfile`) is now `docker/dockerfile:1.29.0`. Compose source citations whose lines moved in v5.7.1 were updated.
- `data.docker_dockerfile`: `ARG BUILDKIT_DISABLE_ONBUILD` and `ARG BUILDKIT_GIT_DEBUG_COMMANDS` are reported as BuildKit built-in build args (new in Dockerfile 1.29.0).
- DriftCheck diffs the documentation-only sources (docker compose and docker CLI commands and options, buildx commands and options, Dockerfile instructions, flags, parser directives, build checks and build args) instead of requiring a blind acceptance. A release with no surface change passes and is logged in `reference/_gen/drift-log.json`.

#### Known limitations

- The labs-only Dockerfile 1.29.0 flags `FROM --reset`, `ENV --unset`, `LABEL --unset` and `VOLUME --unset` (`# syntax=docker/dockerfile:1-labs`) are not modelled by `data.docker_dockerfile`. A Dockerfile that uses `FROM --reset` or `ENV --unset` fails to parse there. `docker_image` builds are unaffected. Recorded in `reference/_gen/deferred.json` as kind `upstream_surface`.

### Pass 6d

#### Fixed

- `data.docker_registry_image`: `config`, `labels` and `created` were always null. They are now read from the registry's image config blob, on the daemon path too (the provider fetches the blob with `registry_auth`; a registry it cannot reach gives a warning and null values, never an error). For an index the daemon's os/arch is used, else linux/amd64, else the first image entry. `config` has the same shape and values as `data.docker_image`'s `config` (TestAccP6cImageDataRoundTrip compares them).

### Pass 6c

#### Breaking

- Structured outputs now mirror the Engine response shape, derived from the pinned swagger by `tools/contractgen`: objects with `additionalProperties` are maps, arrays of objects are lists of nested objects, enums are plain strings. 141 fields that were JSON strings or dynamic values are now typed attributes (23 outputs on 23 items), 23 fields changed their declared type and 469 nested fields are now addressable, e.g. `inspect.network_settings.networks["n"].ip_address`, `inspect.host_config.port_bindings["80/tcp"][0].host_port`, `inspect.config.exposed_ports` (a map; it was dropped before). Schema versions of `docker_container` (2), `docker_image`, `docker_tag`, `docker_network`, `docker_volume`, `docker_plugin` and `docker_service` (1) were bumped; the state upgrader nulls the old outputs and the next refresh fills them.
- Swagger-cited fields of provider-shaped outputs are typed too (Pass 6c verification): `data.docker_image` and `data.docker_registry_image` `config.exposed_ports` / `config.volumes` are maps (`list(string)` before; the data source dropped the whole `config` with "Outputs contract value dropped"); `docker_stack` `service_status[].spec` and `data.docker_stack` `services[].spec` are the ServiceSpec object (a JSON string before); `docker_registry_image` `distribution.descriptor.platform` and `data.docker_image_attestations` `descriptor.platform` are objects; `data.docker_compose_containers` `containers` `health` / `host_config` / `network_settings` / `mounts` / `image_manifest_descriptor`, `data.docker_compose_volumes` `status` / `cluster_volume` / `usage_data` and the container action results' `state.health.log` are typed nested attributes. Schema versions of `docker_stack` and `docker_registry_image` were bumped to 1 with the same nulling upgrader.
- The structured `project` output of `docker_compose` and `data.docker_compose_config` takes its shape from compose-go v2.16.2's Go types (what `json.Marshal(types.Project)` writes): ports `published` is a string (it may be a range), byte sizes (`mem_limit`, `shm_size`, ...) and durations are strings, `cpus` is a number, `extra_hosts` is host -> addresses, `ulimits` entries are `{single, soft, hard}`. `published` was a number and was dropped.
- Keys of user-data maps (labels, options, driver options, sysctls, annotations, log config) are no longer snake_cased: `log_config.config["max-file"]`, not `max_file`. Only schema field names are renamed.
- `docker_service.replicas` is null in state for every mode except `replicated` (it was 1), so config generated for a global service (`terraform plan -generate-config-out`) is valid.

#### Added

- `config_sha256` on `docker_container`, `docker_service`, `docker_network` and `docker_volume`: the hash of the configuration part of the inspect only (never State, timestamps, PIDs, health logs or daemon-assigned addresses). It is the hash a downstream consumer should watch; `content_sha256` remains the hash of the whole raw output and changes whenever runtime state changes (reference/CONTRACT.md, "Which hash to watch").
- `docker_buildx_builder`: `driver = "cloud"` with `endpoint = "<org>/<builder>"` (Docker Build Cloud): a token from auth.docker.io through the provider's credential chain, then BuildKit over build-cloud.docker.com:443. The token lives in memory for the connection only. Implemented against fakes, not yet verified against the live service (`TestAccBuildxBuilderCloud_live`, `TF_ACC_BUILD_CLOUD=1`).
- `docker_exec`: `stdin_path`, read at invoke time; the way to pass a secret to an action (actions cannot mark `stdin` sensitive). Conflicts with `stdin`, `tty` and `detach`.
- Preflight diagnostics for plain HTTP sent to a TLS port and for an SSH host that cannot be reached, at Configure and on every later operation. The cases still not covered are listed in `deferred.json` (kind `diagnostic`).
- Example tests, manual tasks and PairwiseTest install the provider from a local filesystem mirror (a real `terraform init`, lock file written and checksums verified) instead of `dev_overrides`; Terraform 1.14.9 init works.
- `Invoke-Build Clean` removes every `%TEMP%\tpd-*` folder earlier tasks created and lists them. PairwiseTest and DriftCheck write under `test/harness/.logs`; manual scenario working copies live in `test/harness/.work/manual/<scenario>`; `_Apply` / `_Destroy` run unattended and `-Prompt` asks.
- README.md rewritten; every number in it is generated by `tools/doccounts` and checked by `DocCountsCheck`. The registry landing page (docs/index.md) covers configuration, authentication order, owner labels, `min_api_version` and the structured outputs.

#### Fixed

- Strings inside a JSON fallback were encoded twice (`inspect.mounts[].type` was `"\"tmpfs\""`).
- Health check output (`state.health.log[].output`), env values, plugin env, secret data and image history `created_by` are sensitive wherever a structured output carries them.
- "Outputs contract value dropped" is an error under `TF_ACC` and fails ExampleTests and PairwiseTest, so a structured output cannot lose a value silently.

### Pass 6b

#### Added

- `docker_exec` (action) and `docker_exec_output` (ephemeral resource): `stdin`, written to the command's stdin and then closed (`docker exec -i`). Sensitive on the ephemeral resource; on the action it shows in plans, because actions cannot mark attributes sensitive.
- `docker_compose_push`: `ignore_missing` (`docker compose push --ignore-missing`, compose v5.7.0).
- `docker_compose_run`: `start_dependencies` (default `false`, the previous behaviour) and `no_recreate` (`--no-recreate`).
- Example tests: every folder under `examples/` has a `<name>.tests.ps1` (Pester) built on the new `test/TfDockerTest` module (apply, an empty second plan, destroy against the locally built provider; state vs daemon). Each example has a 5-minute budget plus 3 minutes for teardown; `Invoke-Build ExampleTests -Filter 'docker_tag,actions/*'` re-runs a subset, and a summary by example name goes to `test/harness/.logs/examples`.
- `build/Invoke-Logged.ps1`: runs one long command in the background with a hard cap and writes `<name>.pid`, `<name>.log` and `<name>.exit` under `test/harness/.logs/<pass>/` (git-ignored); at the cap it kills the process tree and can bring the harness down. CLAUDE.md makes it the way to start and wait on long runs. Tasks `ExampleTests` (dind harness), `ExampleValidate`, `ExampleModuleTest`, `ExampleCoverage` (default task: every registered type has an example and a test, or a reasoned entry in `reference/_gen/deferred.d/examples.json`, listed in `deferred.json` as kind `example`).
- Pairwise combination tests (`tools/pairwise`, models in `reference/_gen/pairwise/`): configs covering every value pair of `docker_container`, `docker_service`, `docker_network` and `docker_volume`, applied on the harness and compared with the daemon's inspect (`PairwiseGen`, `PairwiseTest`).
- Manual scenarios in `test/__manual/` (container_full, network_volume_full, dockerfile_image_container, compose_local, swarm_stack, actions_tour, kreuzwerker_migration) with the underscore tasks `_List`, `_Up [-Swarm]`, `_Init`, `_Plan`, `_Apply`, `_Destroy`, `_Down` (`-Scenario <name>`); `ManualTaskCheck` (default task) fails if the default task, PreRelease or CI depends on one.
- `ExampleTests` and `PairwiseTest` are PreRelease gates. `test/harness/Invoke-E2E.ps1 -Script` runs a PowerShell script under the harness lifecycle.

#### Changed

- Reference sources re-pinned: docker/compose v5.1.4 -> v5.7.0, docker/cli v29.5.3 -> v29.9.3. The removed `docker compose alpha publish` is no longer an inventory item. The parity rules (volumes_from `:ro`, config-hash over resolved references, bare `KEY` for null environment, depends_on order) were re-checked in the v5.7.0 source and hold; citations updated.
- TestAccComposeParity runs against the docker compose release reference/SOURCES.md pins, which harness-up downloads into `test/harness/.bin` (SHA256-checked), not the compose that ships with Docker Desktop.
- `docker_service`: `replicas` with `mode` global, global_job or replicated_job is an error (it was silently ignored), as in the docker CLI.
- `docker_swarm_node.role` is optional and computed with no default: left unset it follows the node's live role (so `docker_node_promote` leaves no drift), and a node joined with a manager token is no longer demoted to worker.
- `docker_container_pause` / `docker_container_unpause` do nothing when the container is already in the target state.
- `docker_compose_push` matches compose v5.7.0: services without `build` are skipped, and `build.tags` are pushed too.
- Permission-denied and TLS daemon failures carry the Configure preflight diagnostic on every Engine operation, not only at Configure.
- Build scripts call `Write-Build` instead of the `print` alias, which could resolve to Windows' print.exe ("Unable to initialize device PRN").

#### Fixed

- `docker_network`: `enable_ipv4 = false` with an IPv4 `ipam_config` pool is rejected at validate time (the Engine silently dropped the pool: inconsistent result); config-only networks keep their configured driver and IPAM (the Engine reports driver "null"); `config_from` networks are created without labels and reject the arguments the Engine refuses there.
- `docker_volume`: an unnamed volume no longer fails on the Engine 29 `com.docker.volume.anonymous` label; a plugin driver reported with its tag (`sshfs:latest`) keeps the configured name.
- `docker_buildx_bake`: `target.<name>.<attr>` references see every bake target attribute, as in buildx.
- Examples that did not run: container_unpause, image_history, docker_container, docker_image, docker_stack, docker_service (healthcheck on IPv6 localhost), docker_buildx_bake.

### Pass 6a

#### Added

- Release pipeline: GoReleaser v2 builds for darwin/linux/windows on amd64 and arm64, plus linux arm and 386, as `terraform-provider-docker_<version>_<os>_<arch>.zip`; a GPG-signed `SHA256SUMS` that includes the registry manifest (protocol 6.0); a cosign keyless bundle and SLSA build provenance (`.github/workflows/release.yml`, run on `v*` tags; [RELEASING.md](RELEASING.md)). Tasks `Release` (local snapshot, never signs or publishes, then checks every asset) and `ReleaseCheck` (target matrix and cross-compiles, no goreleaser; also in CI).
- `PreRelease` task: every pre-release gate from one list (`$PreReleaseTasks` in `build/prerelease.tasks.ps1`), run in order without stopping at a failure, then one summary table (task, result, duration).
- `DriftCheck` task and `tools/drift`: the newest upstream release of every source pinned in `reference/SOURCES.md` against its pin; moved specs are fetched into TEMP and diffed through fieldcov (new flags `-swagger`, `-compose-schema`, `-bake-ref`, `-fields-out`). It fails on anything new that is neither mapped nor in `reference/_gen/drift-accepted.json`. A weekly workflow (`.github/workflows/drift.yml`) runs it and opens or updates one "Upstream drift" issue.
- `reference/_gen/deferred.json` (generated by `tools/deferred`; tasks `DeferredReport` / `DeferredCheck`): everything knowingly not done (field skips and partials, PARTIAL markers, inventory skips, acceptance tests that cannot run on the harness), each with its reason and source.

#### Changed

- Provider configure: daemon failures are classified with distinct diagnostics: not reachable (the host and where it came from), permission denied on the socket or named pipe, TLS failure (with the TLS settings in effect), and a daemon below `min_api_version` (both versions shown).
- Acceptance harness on Docker Engine 29.9.3 (API 1.56, was 28.5.2 / 1.51), with a containerd image store node (`cdnode`, `TF_ACC_CONTAINERD_HOST`); the other nodes stay on overlay2. Now run for real: service `swap_bytes` / `memory_swappiness`, loading a multi-platform image, image attestations and multi-platform builds.
- `go.mod` tidied: `github.com/tonistiigi/fsutil` and `google.golang.org/genproto/googleapis/rpc` moved to the direct block (no new module), so GoReleaser's `go mod tidy -diff` check passes.

### Pass 5.3

#### Breaking

- **`docker_container`: `must_run` is replaced by `state`** = `"running"` (default) | `"created"` | `"exited"`, named after Docker's `State.Status` values (see the [container state guide](docs/guides/container-state.md)).
  - `running` starts a stopped container in place on the next apply (no replacement).
  - `created` creates the container and never starts it; a running one only gets a plan warning.
  - `exited` runs the container once to completion (`wait_timeout`, default 5m), records `exit_code`, `finished_at`, `logs` and `log_entries`, fails the apply when the exit code is not in `expected_exit_codes` (default `[0]`), and is never run again by a refresh. `auto_remove` with `state = "exited"` is a validation error: use the new `docker_container_run` action.
  - Existing state upgrades automatically (schema v0 -> v1); there is no exact counterpart for `must_run = false`. Moving from kreuzwerker/docker: `start = false` becomes `created`, `attach = true` becomes `exited`, everything else `running`; `must_run` is dropped (subsumed by `running`).
- `docker_container`: `stop_timeout` is now Optional + Computed. When unset it reports the daemon default (Docker Desktop 29.5.3 fills in 1), which used to fail the apply with "inconsistent result after apply"; only changing a configured value forces replacement.
- `docker_network`: `scope` is now an optional argument (Forces replacement).
- **Registry credentials (D4).** A registry with no `registry_auth` block now gets credentials from the default docker config (`DOCKER_CONFIG`, else `~/.docker/config.json`); it used to be anonymous. Lookup order: `registry_auth` (username/password, then `DOCKER_REGISTRY_USER` / `DOCKER_REGISTRY_PASS`, then its config file), then in a config file `credHelpers[registry]`, then `credsStore`, then `auths`. Credential helpers still run only with `allow_credential_helpers = true`. Nothing found still means anonymous, never an error. BuildKit builds use the same lookup.
- Moving state from kreuzwerker/docker accepts only the source address `registry.terraform.io/kreuzwerker/docker` (Terraform >= 1.8). Any other registry host, or the bare `kreuzwerker/docker`, is no longer treated as kreuzwerker.

#### Added

- **`docker_container_run` action**: create, start, wait, capture `{exit_code, stdout, stderr}`, then remove (`remove`, default true), like `docker run --rm`. Takes `result_path` like every action.
- **Owner labels.** Provider argument `owner`: every object the provider creates gets the label `io.terraform.provider.owner=<owner>`. Resources (12 types) also refuse to create over a same-named object owned by someone else, and need `adopt = true` to manage one (for example after import); actions that create objects (8 types) only add the label. Unset: no labels and no checks. The full lists, and the types deliberately not labelled with their reasons, are in [reference/CONTRACT.md](reference/CONTRACT.md) ("Owner label").
- **Engine API call logging**: one DEBUG entry per Engine API call (subsystem `docker_api`, shown with `TF_LOG_PROVIDER=DEBUG`) with `request_id`, `method`, `path` (secret query values redacted), `status`, `duration_ms` and `api_version`.
- **Field coverage.** New tool `tools/fieldcov` and task `FieldCoverage` (in the default task, enforce mode): every writable upstream field is implemented, or skipped or partial with a reason. 1200 fields: Engine API 756 (692 mapped, 64 skipped), compose-spec 414 (378 mapped, 29 skipped, 7 partial), bake 30 (30 mapped). This pass added, under the upstream names:
  - `docker_container`: `umask` (Engine API 1.56), `annotations`, `blkio_weight`, `blkio_weight_device`, the `blkio_device_*` throttles, `cpu_realtime_*`, `memory_swappiness`, `oom_*`, `pids_limit`, `masked_paths`, `readonly_paths`, `links`, `console_size`, `cgroup`, `uts_mode`, `volume_driver`, `shell`, `volumes`; mount `consistency`, bind and tmpfs options and type `image`; per-network `links`.
  - `docker_network`: `scope`, `config_only`, `config_from`, `enable_ipv4`. `docker_exec`: `console_size`. `docker_container_cp`: `copy_uidgid`.
  - `docker_service` and `docker_stack` services: `tty`, `open_stdin`, `oom_score_adj`, `privileges.{no_new_privileges, seccomp, app_armor}`, tmpfs and bind mount options, `healthcheck.start_interval`, `resources.limits.pids`, `resources.swap_bytes` and `resources.memory_swappiness` (Engine API 1.52), `configs.runtime`.
  - `docker_secret`: `driver` and `templating`; `docker_config`: `templating`; `docker_swarm_init`: `data_path_addr`, `ca_config.external_cas`; `docker_swarm_node`: `data_path_addr`; `docker_node`: `name`.
  - `docker_image_commit`: `config` (all container config fields); `docker_image_load`: `quiet`; `docker_registry_image`: `platform` (Engine API 1.46).
  - `docker_compose` local mode now applies lifecycle hooks (`pre_start`, `post_start`, `pre_stop`), `blkio_config`, `credential_spec`, `use_api_socket`, `links`, `external_links`, `mac_address`, `umask`, volume labels, per-network `gw_priority`, `link_local_ips`, `driver_opts`, `interface_name` and `use_service_name_alias`, and `pull_policy` `daily` / `weekly` / `every_<n>` / `refresh` (from the image's last pull time, like compose). Swarm mode sends secret `driver` / `template_driver` and config `template_driver`. `docker_compose_publish`: `with_env`, `resolve_image_digests`, `oci_version`.
- **Multi-platform builds**: `docker_image` `build.platforms` takes several platforms and builds an image index (needs the daemon's containerd image store, checked before the build). `build.builder` also accepts a BuildKit address.
- buildx: bake `target:` contexts and `target.<name>.<attr>` references, bake `resources`, the `remote` driver for `docker_buildx_builder`, and build-history materials and attachment bodies (`attachment`, `attachment_platform`, `attachment_body`).
- `insecure_skip_verify` on `data.docker_registry_image` now takes effect: when set, the registry is read directly, with TLS verification as configured.

#### Changed

- **docker_compose recreate rule (D5).** Every container is recreated when its `com.docker.compose.config-hash` label no longer matches the service, as `docker compose up` decides it (compose `mustRecreate`). Who gets a one-time recreate after upgrading, and why:
  - Local mode: containers created by an earlier version whose config-hash label is stale while the provider's own spec hash still matched. That is a service changed after creation only in something that hash did not see: a `KEY: null` environment entry added or removed (the gap left in Pass 5.2), or a changed hook, `env_file`, `label_file`, `extends`, `develop`, `attach`, `models` or `provider`. For such projects the first plan shows one in-place update, which recreates only those containers. Containers without the provider's spec-hash label are judged as before, and services without those keys see no plan change.
  - Local mode: services that set `blkio_config`, `links`, `external_links`, `mac_address`, `umask`, `credential_spec`, `use_api_socket` or volume labels, which used to be ignored, are recreated once to apply them.
  - Swarm mode (D6): services with a `KEY: null` environment entry are updated in place once; the bare `KEY` is now sent as `docker stack deploy` does (docker/cli v29.9.3 cli/compose/convert/service.go:614-626). Secrets with `driver` / `template_driver` and configs with `template_driver` are recreated once.
  - Setting or changing `owner` recreates nothing.
- PARTIAL sub-features: 10 before this pass, 4 now. Remaining: `compose:models` (Engine API 1.56 has no model endpoint; compose runs the docker-model CLI plugin), `compose:cli-publish` (`--app`), `buildx:builder` (the kubernetes driver and the buildx instance store need modules this provider does not depend on), `buildx:bake` (`policy` is parsed but not evaluated: Rego needs open-policy-agent).

#### Removed (development)

- `GNUmakefile`. Invoke-Build is the only build entry point: CI installs InvokeBuild and runs the same tasks under pwsh, and acceptance runs go through `test/harness/Invoke-E2E.ps1`. New tasks: `FieldCoverage`, `FieldMap`, `DocCounts` / `DocCountsCheck` (the type counts in CLAUDE.md and README.md are generated and checked), `ParityCheck`, `ApiWatchCheck`.


### Breaking

Keep a Changelog has no Breaking heading. It is listed first here because upgrading requires configuration and state changes.

Pass 5 renamed attributes to their upstream names (Engine API, compose-spec, docker CLI, buildx). Most of the old shapes came from kreuzwerker/docker; several are this provider's own. If you are coming from kreuzwerker/docker, follow the [migration guide](docs/guides/migrating-from-kreuzwerker.md). It covers the `moved` blocks for all 12 kreuzwerker resource types (Terraform >= 1.8) and the full kreuzwerker attribute mapping, including the kreuzwerker attributes that are dropped, each with a reason.

Behaviour that changes without a rename:

- `docker_tag.id` is now `<source image ID>.<target image>`. Existing state is migrated on read.
- Plural data sources such as `docker_configs`, `docker_secrets`, `docker_nodes` and `docker_services` hold the full Engine objects, field by field. For example, `nodes[*].hostname` is now `nodes[*].description.hostname`; every moved field is listed below.

The attributes below changed between this provider's schema before Pass 5 and now. The list is generated from [reference/_gen/renames.json](reference/_gen/renames.json): every row whose previous attribute differs from the current one, was removed, or (for this provider's own attributes) changed shape. Each row reads `old` -> `new`, with the shape change after the colon where there is one. `Q<n>` and "General rule" in a reason refer to [reference/DECISIONS.md](reference/DECISIONS.md).

Type rename: `data.docker_logs` -> `data.docker_container_logs`. Q6: one logs data source per Engine operation (ContainerLogs / ServiceLogs / TaskLogs); the container one is named for its object, beside data.docker_service_logs and data.docker_task_logs.

#### Resources

**`docker_buildx_builder`**

- `buildkit_config` -> `buildkitd_config`
- `buildkit_config_content` -> `buildkitd_config_content`
- `buildkit_flags` -> `buildkitd_flags`

**`docker_compose`**

- `project_name` -> `name`
- `working_dir` -> `project_directory`

**`docker_container`**

- `capabilities` removed; single block {add, drop} -> flattened into top-level cap_add / cap_drop. General rule: HostConfig has CapAdd/CapDrop, no capabilities object (compose cap_add/cap_drop; docker_service already uses them).
- `capabilities.add` -> `cap_add`: rename; block attribute -> top-level set(string)
- `capabilities.drop` -> `cap_drop`: rename; block attribute -> top-level set(string)
- `devices.container_path` -> `devices.path_in_container`
- `devices.host_path` -> `devices.path_on_host`
- `devices.permissions` -> `devices.cgroup_permissions`
- `log_driver` -> `logging.driver`: rename; flat attribute -> single block logging
- `log_opts` -> `logging.options`: rename; flat attribute -> single block logging
- `max_retry_count` -> `maximum_retry_count`
- `mounts.tmpfs_size` -> `mounts.tmpfs_options.size_bytes`
- `networks_advanced` -> `networks`: list of blocks keyed by name -> map keyed by network name; connect/disconnect in place (NetworkConnect/NetworkDisconnect)
- `networks_advanced.aliases` -> `networks.aliases`: rename (parent block -> map value)
- `networks_advanced.ipv4_address` -> `networks.ipv4_address`: rename (parent block -> map value)
- `networks_advanced.ipv6_address` -> `networks.ipv6_address`: rename (parent block -> map value)
- `networks_advanced.name` -> `networks`: block attribute -> map key
- `ports.external` -> `ports.host_port`
- `ports.internal` -> `ports.container_port`
- `ports.ip` -> `ports.host_ip`
- `upload` -> `files`: rename (list of blocks); set of blocks -> list of blocks
- `upload.content` -> `files.content`: rename (parent block)
- `upload.content_base64` -> `files.content_base64`: rename (parent block)
- `upload.executable` -> `files.mode`: bool -> mode: true = "0744" (unless permissions set); removed as a separate attribute
- `upload.file` -> `files.path`
- `upload.group` -> `files.gid`: rename; string -> number
- `upload.owner` -> `files.uid`: rename; string -> number
- `upload.permissions` -> `files.mode`
- `upload.source` -> `files.source`: rename (parent block)
- `upload.source_hash` -> `files.source_hash`: rename (parent block)

**`docker_image`**

- `image_id` -> `id`: rename; image_id removed
- `repo_digest` -> `image_digest_ref`: rename; upstream RepoDigests list goes in the structured output

**`docker_network`**

- `ipv6` -> `enable_ipv6`

**`docker_registry_image`**

- `sha256_digest` -> `digest`: rename (image_digest_ref added beside it)

**`docker_service`**

- `dns` -> `dns_config`
- `dns.nameservers` -> `dns_config.nameservers`
- `dns.options` -> `dns_config.options`
- `dns.search` -> `dns_config.search`
- `mounts.tmpfs_size` -> `mounts.tmpfs_options.size_bytes`
- `mounts.volume_driver` -> `mounts.volume_options.driver_config.name`
- `mounts.volume_driver_opts` -> `mounts.volume_options.driver_config.options`
- `mounts.volume_labels` -> `mounts.volume_options.labels`
- `networks` (same name): set(string) of network names -> map keyed by network name (NetworkAttachmentConfig); old values become the keys
- `networks_advanced` -> `networks`
- `networks_advanced.aliases` -> `networks.aliases`
- `networks_advanced.name` -> `networks`

**`docker_stack`**

- `service.dns` -> `service.dns_config`: rename (single block)
- `service.dns.nameservers` -> `service.dns_config.nameservers`: rename (parent block)
- `service.dns.options` -> `service.dns_config.options`: rename (parent block)
- `service.dns.search` -> `service.dns_config.search`: rename (parent block)
- `service.mounts.tmpfs_size` -> `service.mounts.tmpfs_options.size_bytes`: rename; flat attribute -> nested single block tmpfs_options
- `service.mounts.volume_driver` -> `service.mounts.volume_options.driver_config.name`: rename; flat attribute -> nested blocks volume_options.driver_config
- `service.mounts.volume_driver_opts` -> `service.mounts.volume_options.driver_config.options`: rename; flat attribute -> nested blocks volume_options.driver_config
- `service.mounts.volume_labels` -> `service.mounts.volume_options.labels`: rename; flat attribute -> nested single block volume_options
- `service.networks` (same name): set(string) of network names -> map keyed by network name (NetworkAttachmentConfig); old values become the keys
- `service.networks_advanced` -> `service.networks`: list of blocks keyed by name -> map keyed by network name (merged into networks)
- `service.networks_advanced.aliases` -> `service.networks.aliases`: rename (parent block -> map value)
- `service.networks_advanced.name` -> `service.networks`: block attribute -> map key

#### Data sources

**`data.docker_configs`**

- `configs.data` -> `configs.spec.data`: rename; flat list field -> upstream path in the configs objects
- `configs.labels` -> `configs.spec.labels`: rename; flat list field -> upstream path in the configs objects
- `configs.name` -> `configs.spec.name`: rename; flat list field -> upstream path in the configs objects

**`data.docker_container_logs`**

- `logs_list_string` -> `log_entries.message`: list(string) -> list(object); each line's text is .message (raw `logs` string and timestamp/stream/source added)
- `show_stderr` -> `stderr`
- `show_stdout` -> `stdout`
- `timestamps` removed; timestamps are always requested. Q6 logs contract: log_entries.timestamp always comes from timestamps=true.

**`data.docker_image`**

- `image_id` -> `id`: removed; same value as id (ImageInspect.Id)
- `repo_digest` -> `image_digest_ref`: rename; upstream RepoDigests list goes in the structured output

**`data.docker_network`**

- `ipv6` -> `enable_ipv6`

**`data.docker_nodes`**

- `nodes.addr` -> `nodes.status.addr`: rename; flat list field -> upstream path in the nodes objects
- `nodes.availability` -> `nodes.spec.availability`: rename; flat list field -> upstream path in the nodes objects
- `nodes.engine_version` -> `nodes.description.engine.engine_version`: rename; flat list field -> upstream path in the nodes objects
- `nodes.hostname` -> `nodes.description.hostname`: rename; flat list field -> upstream path in the nodes objects
- `nodes.labels` -> `nodes.spec.labels`: rename; flat list field -> upstream path in the nodes objects
- `nodes.leader` -> `nodes.manager_status.leader`: rename; flat list field -> upstream path in the nodes objects
- `nodes.role` -> `nodes.spec.role`: rename; flat list field -> upstream path in the nodes objects
- `nodes.state` -> `nodes.status.state`: rename; flat list field -> upstream path in the nodes objects

**`data.docker_registry_image`**

- `sha256_digest` -> `digest`: rename (image_digest_ref added beside it)

**`data.docker_secrets`**

- `secrets.labels` -> `secrets.spec.labels`: rename; flat list field -> upstream path in the secrets objects
- `secrets.name` -> `secrets.spec.name`: rename; flat list field -> upstream path in the secrets objects

**`data.docker_services`**

- `services.image` -> `services.spec.task_template.container_spec.image`: rename; flat list field -> upstream path in the services objects
- `services.labels` -> `services.spec.labels`: rename; flat list field -> upstream path in the services objects
- `services.mode` -> `services.spec.mode`: string "replicated" | "global" -> the upstream ServiceMode object (spec.mode.replicated / global / replicated_job / global_job; the one set is the mode)
- `services.name` -> `services.spec.name`: rename; flat list field -> upstream path in the services objects
- `services.replicas` -> `services.spec.mode.replicated.replicas`: rename; flat list field -> upstream path in the services objects

#### Actions

**`action.docker_container_export`**

- `container` -> `container_id`: rename (accepts ID or name as before)
- `path` -> `output`

**`action.docker_exec`**

- `container` -> `container_id`: rename (accepts ID or name as before)
- `service` -> `service_id`: rename (accepts ID or name as before)

**`action.docker_image_load`**

- `path` -> `input`

**`action.docker_image_save`**

- `path` -> `output`

**`action.docker_node_activate`**

- `node` -> `node_id`: rename (accepts ID or hostname as before)

**`action.docker_node_drain`**

- `node` -> `node_id`: rename (accepts ID or hostname as before)

**`action.docker_service_force_update`**

- `service` -> `service_id`: rename (accepts ID or name as before)

**`action.docker_service_rollback`**

- `service` -> `service_id`: rename (accepts ID or name as before)

**`action.docker_service_scale`**

- `service` -> `service_id`: rename (accepts ID or name as before)

**`action.docker_system_prune`**

- `all_images` -> `all`

#### Ephemeral resources

**`ephemeral.docker_exec_output`**

- `container` -> `container_id`: rename (accepts ID or name as before)

**`ephemeral.docker_registry_token`**

- `token` -> `token.token`: string -> object {token, expires_in, issued_at}; the bearer token is now token.token (expires_in / issued_at also stay top-level)

### Added

- 80 new types (Pass 5), for 138 registered in total: 17 resources, 54 data sources, 51 actions, 4 ephemeral resources, 8 list resources and 4 provider functions. [README_resources.md](README_resources.md) lists them by layer.
  - Resources: `docker_node`, `docker_registry_manifest_list`.
  - Ephemeral resource: `docker_registry_login`.
  - Data sources:
    - containers: `docker_container`, `docker_container_changes`, `docker_container_file`, `docker_container_stats`, `docker_container_top`;
    - images: `docker_images`, `docker_image_history`, `docker_image_search`, `docker_image_attestations`;
    - objects and system: `docker_networks`, `docker_volumes`, `docker_plugins`, `docker_info`, `docker_version`, `docker_system_df`;
    - swarm: `docker_swarm`, `docker_secret`, `docker_config`, `docker_service`, `docker_service_logs`, `docker_task`, `docker_tasks`, `docker_task_logs`;
    - compose and stacks: `docker_compose_config`, `docker_compose_containers`, `docker_compose_images`, `docker_compose_logs`, `docker_compose_port`, `docker_compose_projects`, `docker_compose_top`, `docker_compose_volumes`, `docker_stack`, `docker_stacks`;
    - Dockerfile: `docker_dockerfile`;
    - buildx: `docker_buildx_builder`, `docker_buildx_builders`, `docker_buildx_history`, `docker_buildx_history_records`, `docker_buildx_disk_usage`.
  - Actions:
    - containers: `docker_container_start` / `_stop` / `_restart` / `_kill` / `_pause` / `_unpause` / `_wait` / `_cp`;
    - images: `docker_image_commit`;
    - prune: `docker_container_prune`, `docker_network_prune`, `docker_volume_prune`, `docker_builder_prune`;
    - plugins: `docker_plugin_create`, `docker_plugin_push`;
    - swarm: `docker_node_promote`, `docker_node_demote`, `docker_swarm_unlock`, `docker_swarm_rotate_unlock_key`, `docker_swarm_rotate_ca`;
    - compose: `docker_compose_build` / `_commit` / `_cp` / `_exec` / `_export` / `_kill` / `_pause` / `_publish` / `_pull` / `_push` / `_restart` / `_run` / `_scale` / `_start` / `_stop` / `_unpause` / `_wait`;
    - buildx: `docker_buildx_bake`.
- Migration from kreuzwerker/docker: the 12 kreuzwerker resource types can move their state into this provider with `moved` blocks (`ResourceWithMoveState`, Terraform >= 1.8). See the [migration guide](docs/guides/migrating-from-kreuzwerker.md).
- The kreuzwerker/docker v4.6.0 gap attributes (143 of them) are implemented, across `docker_container`, `docker_image`, `docker_service`, `docker_buildx_builder` and others. The parity check now maps all 685 kreuzwerker attributes or drops each one with a reason.
- Outputs contract: managed types and data sources expose a raw `*_json` output of the Engine response next to the structured attributes, plus identity and version outputs. Re-encoded outputs are listed in [reference/CONTRACT.md](reference/CONTRACT.md).
- `docker_volume`: Swarm CSI cluster volumes (`cluster_volume_spec`, `cluster_volume_id`; availability changes in place).
- `docker_container`: one `networks` map, connected and disconnected in place, and a `files` block for copying files in.
- `docker_image`: builds can run on named buildx builders, and `build` gains `ssh`, `no_cache_filter`, outputs, attestations and build metadata outputs. `build.additional_contexts` now works (it used to return "not supported yet").
- `docker_swarm_init`: spec updates (orchestration, raft, dispatcher, CA config, labels).
- `docker_secret` / `docker_config`: labels update in place.
- The swarm actions report one result object and take `result_path`, as do image import, load and save.
- Project: the dind acceptance harness gained seeded registry images, a standby worker (worker4) joined in the `-SwarmInit` pass, and a test plugin fixture (no-op volume plugin and CSI driver). `TestAccComposeParity` runs all 13 sub-cases against the docker compose CLI. Every Dockerfile doc example round-trips through `data.docker_dockerfile`, or is allowlisted with a reason (204 round-trip, 9 allowlisted fragments).
- Project: inventory status is computed honestly. A registered type whose documented sub-feature is absent or a no-op carries a `// PARTIAL(pass5)` marker and shows as `partial` in [reference/INVENTORY.md](reference/INVENTORY.md). Ten items are partial: buildx bake / build / builder / history, compose publish / services / models / secrets / configs, and the registry image data source.

### Changed

- `com.docker.compose.config-hash` on `docker_compose` (local mode) is the docker compose CLI's value. The provider's own digest moved to `io.terraform.provider.compose-spec-hash`. Existing containers are not recreated.
- `docker_compose` local mode hashes `service:` and `volumes_from` references after resolving them to container IDs, as docker compose does, and recreates a dependant container when its target container is recreated (also when that happens outside Terraform; refresh shows it as drift). Containers of services with such references that were created before this change are recreated once on the first apply (Pass 5.2).
- `docker_compose` local mode writes the `com.docker.compose.depends_on` label with its entries in sorted order (Pass 5.2).
- `docker_compose` local mode keeps a `volumes_from` `:ro` suffix read-only (docker compose drops it and mounts read-write). This differs from docker compose and is documented in [reference/CONTRACT.md](reference/CONTRACT.md) (Pass 5.2).
- `docker_compose.remove_orphans` defaults to `true`, while the CLI's `--remove-orphans` is opt-in. See "Known differences from upstream defaults" in [reference/CONTRACT.md](reference/CONTRACT.md).
- Removing `docker_network.ipam_config` is now an in-place change.
- `ephemeral.docker_registry_token`: `token` is an object (`token.token`, `expires_in`, `issued_at`).
- `action.docker_swarm_unlock` `unlock_key` and `action.docker_swarm_rotate_ca` `signing_ca_key` are plain required arguments, not write-only (framework v1.19 rejects write-only arguments on actions). They are kept out of progress and result output.
- Raw `*_json` outputs are deterministic. Byte-exact outputs no longer vary by a trailing newline. Outputs the Engine client cannot return raw are re-encoded canonically: sorted keys, original number text, no HTML escaping.

### Removed

- `data.docker_logs` (now `data.docker_container_logs`), `docker_container.capabilities`, `data.docker_container_logs.timestamps`, `data.docker_image.image_id` and the other attributes listed under Breaking.

### Fixed

- `docker_compose` local mode: an `environment` entry with a null value is passed to the Engine as a bare `KEY`, following compose-spec unset semantics (Pass 5.2). Adding or removing only a null entry does not recreate an existing container. Swarm mode still drops null entries.
- `docker_compose` local mode create matches `docker compose up`: `com.docker.compose.image` is the image ID, `depends_on` uses `svc:condition:restart`, the container name is the first alias, and named volumes go through binds.
- `action.docker_compose_scale` starts kept containers that had stopped, as the CLI does.
- `docker_network.ipam_config`: a daemon-assigned pool in migrated state no longer forces replacement.
- `docker_container`: when `must_run` plans a start, the computed outputs are unknown. This fixes an inconsistent result after apply.
- `data.docker_dockerfile`: required arguments inside absent keyword blocks no longer fail validation.
- `action.docker_plugin_create` on Windows: tar entry permissions follow the docker CLI rule (`&= 0755`, `|= 0111`).
- `data.docker_container_changes` and verbose `data.docker_system_df` lists come back in a stable order.

### Security

- docker_swarm and docker_swarm_init inspect_json previously included swarm join tokens in state without being marked sensitive; it now contains cluster info only.
