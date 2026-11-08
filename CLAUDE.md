# CLAUDE.md

## What this is

`terraform-provider-docker` (registry address `JerryBalmer1/docker`) is a
Terraform provider on terraform-plugin-framework v1.19 that owns the whole
Docker chain through the Docker Engine API. It never shells out to the docker
CLI or the compose plugin.

Four layers, in dependency order:

1. **Image**: build, pull, tag, push (`docker_image`, `docker_tag`,
   `docker_registry_image`, `docker_buildx_builder`).
2. **Single host**: `docker_network`, `docker_volume`, `docker_container`,
   `docker_plugin`, `docker_compose` (local mode).
3. **Swarm topology**: `docker_swarm_init`, `docker_swarm_node`.
4. **Swarm workload**: `docker_secret`, `docker_config`, `docker_service`,
   `docker_stack`, `docker_compose` (swarm mode).

Six Terraform kinds:
- resources and data sources;
- actions (`docker_exec`, the prune / save / load / import actions, the node and service actions);
- ephemeral resources (`docker_swarm_tokens`, `docker_registry_token`, `docker_exec_output`);
- provider functions (`parse_image_ref`, `image_ref`, `compose_env`, `port_spec`);
- list resources for `terraform query`, one per managed type with identity.

Actions and list resources need Terraform >= 1.14, write-only `*_wo` arguments >= 1.11, and ephemeral resources >= 1.10.
README_resources.md (generated) lists every type by layer.

## Repo map

- `internal/provider/`: the whole provider, one Go package. Each kind or lane registers through its own `register_*.go`; `provider.go` only calls those.
  - Tests: `*_test.go`. Unit tests use an httptest fake Engine. `TestAcc*` are acceptance tests that skip without `TF_ACC` or the environment they need.
- `tools/genskill/`: renders `.claude/skills/terraform-provider-docker/` (SKILL.md + reference/), `.claude/skills/docker-cli-map/SKILL.md` and README_resources.md from the live provider schema. `-check` fails on drift; `-strict` fails on missing descriptions.
- `tools/compose2stack/`: converts a compose file once into `docker_stack` HCL (swarm) or container/network/volume HCL (local).
- `tools/parity/`: kreuzwerker/docker v4.6.0 types vs ours. The hand-maintained list is in `tools/parity/paritydata`. `-check BUILD_REPORT.md` fails on a missing type with no `[BLOCKED]` line.
- `tools/apiwatch/`: diffs the pinned Docker Engine API spec (`pinned.json` + `spec/`) against the newest published one, mapped to provider types.
- `tools/climap/commands.json`: the hand-maintained docker CLI command list behind the docker-cli-map skill.
- `test/harness/`: the dind swarm harness (manager, worker1-3, TLS node, registry:2, Docker Hub mirror). `Invoke-E2E.ps1` is the only entry point for acceptance runs.
- `examples/`, `templates/`, `docs/`: tfplugindocs inputs and output. `docs/` is generated; don't hand-edit it.
- `.claude/skills/`: agent skills (see the end of this file).
- `BUILD_REPORT.md`: the log of every pass.

## Standing rules

- **Never commit, push or tag.** Edit, `git add -A`, then stop with a report. The user commits.
- **One git worktree per lane** when a pass runs parallel lanes:
  - Location: `C:\__Code\tpd-wt\lane<X>`, detached at HEAD plus the Lane 0 changes copied in.
  - Lanes never share files. Each lane owns an explicit file list, and cross-lane needs are logged as TODO lines with the exact change.
  - The orchestrator merges by copying each lane's owned files back into the main tree. Diff with CRs ignored, because the checkout uses autocrlf.
- **Acceptance runs go through `test/harness/Invoke-E2E.ps1`** (or `Invoke-Build TestAcc`, which wraps it), never against the Docker Desktop daemon directly.
  - Only one lane or agent runs the harness at a time.
  - Swarm-manager tests need the `-SwarmInit` pass; see `$SwarmAccRegex` in terraform-provider-docker.build.ps1.
- **BUILD_REPORT.md is append-only.** Each pass gets a `## Pass N` section: a log, then a summary.
  - Log lines are `[P<pass>-<lane>] [TAG] text`, with TAG one of BUILD, TEST, VERIFIED, FIXED, ASSUMED, TODO, or `[BLOCKED] <lane> <what> <why> <what was tried>`.
  - A blocked item never stops a lane, and a blocked lane never stops the pass: log it, leave the code compiling (stub with a clear error and a TODO), and move on.
- **Nothing shells out.** The one documented exception is docker credential helpers, run by docker/cli's credentials package and only when the provider sets `allow_credential_helpers = true` (default false). SSH is pure Go (golang.org/x/crypto/ssh).
- **Every new type ships complete:**
  - a full implementation;
  - unit tests;
  - an acceptance test that skips cleanly without its environment (and `tfversion.SkipBelow` for 1.10 / 1.11 / 1.14 features);
  - an example under `examples/` in the tfplugindocs layout;
  - a MarkdownDescription whose first sentence stands alone (genskill reads it);
  - a row in `tools/parity/paritydata`;
  - if it replaces a CLI command, an entry in `tools/climap/commands.json`.
- Attributes with RequiresReplace say "Forces replacement." in their description (`genskill -strict`).
- At the start of every pass run the **engine-api-watch** skill (`go run ./tools/apiwatch`) and file what it finds.

## Build and test

PowerShell (Windows; no make needed). [terraform-provider-docker.build.ps1](terraform-provider-docker.build.ps1)
is an [Invoke-Build](https://github.com/nightroman/Invoke-Build) script; run it from the repo root
(`Install-Module InvokeBuild -Scope CurrentUser` once):

```
Invoke-Build ?                   # list tasks
Invoke-Build                     # default: Build, Vet, Test, GenSkillCheck, InventoryCheck
Invoke-Build Build, Vet, Test    # go build + provider binary, go vet, unit tests (-short, TF_ACC unset)
Invoke-Build Tools               # unit tests of tools/ only
Invoke-Build GenSkill            # regenerate .claude/skills + README_resources.md (GenSkillCheck: -check)
Invoke-Build Docs                # GenSkill, then tfplugindocs generate + validate
Invoke-Build Check               # gofmt, vet, genskill -check/-strict, tfplugindocs validate
Invoke-Build TestAcc             # dind harness: plain pass, then -SwarmInit pass (TF_ACC=1)
Invoke-Build Parity              # kreuzwerker parity table
Invoke-Build ApiWatch            # Engine API drift vs tools/apiwatch/pinned.json
Invoke-Build Inventory           # reference/inventory.json + INVENTORY.md from reference/_gen/fragments (InventoryCheck: diff must be empty)
Invoke-Build ReferenceVerify     # re-hash reference/upstream against the SHAs pinned in reference/SOURCES.md
Invoke-Build Compose2Stack -In docker-compose.yml -Out stack.tf -Mode swarm
```

Nobody hand-edits `reference/inventory.json` or `INVENTORY.md`: edit `reference/_gen/fragments/*.json`, then `Invoke-Build Inventory`.

bash (Linux/macOS/CI):

```
make build vet test
make skill docs check
make testacc
make parity apiwatch
make compose2stack IN=docker-compose.yml OUT=stack.tf MODE=swarm
go run ./tools/parity -check BUILD_REPORT.md
go run ./tools/apiwatch -check
```

Acceptance tests by hand in bash:

```
bash test/harness/scripts/harness-up.sh && set -a && source test/harness/.env && set +a
TF_ACC=1 go test ./internal/provider/ -run TestAcc -count=1 -v -timeout 120m
bash test/harness/scripts/harness-down.sh
```

## Skills

Agent skills live in [.claude/skills/](.claude/skills/):

- `terraform-provider-docker/`: how to write Terraform with this provider. Generated by `go run ./tools/genskill`; don't hand-edit.
- `docker-cli-map/`: the docker / compose / swarm CLI command to provider type map. Generated from `tools/climap/commands.json` + `tools/parity/paritydata`.
- `engine-api-watch/`: when and how to run `tools/apiwatch` and file its results. Hand-written.
