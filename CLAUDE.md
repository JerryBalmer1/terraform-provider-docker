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

Six Terraform kinds: resources and data sources; actions (`docker_exec`, the prune / save / load / import actions,
the node and service actions); ephemeral resources; provider functions; list resources for `terraform query`, one per
managed type with identity. The counts and short lists below are generated from `internal/provider/registry_gen.go`
(`Invoke-Build DocCounts`); `DocCountsCheck`, in the default task, fails when they, any other "<n> <kind>" count in
this file or README.md, or a backticked `docker_*` name that is not registered disagree with the registry.

<!-- doccounts:begin (generated from internal/provider/registry_gen.go by go run ./tools/doccounts -write; Invoke-Build DocCountsCheck fails on drift) -->
Registered: 17 resources, 54 data sources, 52 actions, 4 ephemeral resources, 4 provider functions, 8 list resources.

- resources (17): `docker_buildx_builder`, `docker_compose`, `docker_config`, `docker_container`, `docker_image`, `docker_network`, `docker_node`, `docker_plugin`, `docker_registry_image`, `docker_registry_manifest_list`, `docker_secret`, `docker_service`, `docker_stack`, `docker_swarm_init`, `docker_swarm_node`, `docker_tag`, `docker_volume`
- ephemeral resources (4): `docker_exec_output`, `docker_registry_login`, `docker_registry_token`, `docker_swarm_tokens`
- provider functions (4): `compose_env`, `image_ref`, `parse_image_ref`, `port_spec`
- list resources (8): `docker_config`, `docker_container`, `docker_image`, `docker_network`, `docker_secret`, `docker_service`, `docker_swarm_node`, `docker_volume`
<!-- doccounts:end -->

Actions and list resources need Terraform >= 1.14, write-only `*_wo` arguments >= 1.11, and ephemeral resources >= 1.10.
README_resources.md (generated) lists every type by layer.

## Repo map

- `internal/provider/`: the whole provider, one Go package. Registration is generated: `registry_gen.go` comes from `reference/inventory.json` via `tools/registrygen` (`Invoke-Build RegistryGen`; `RegistryCheck` in the default task). Constructors are named `New<CamelName><Kind>`.
  - Tests: `*_test.go`. Unit tests use an httptest fake Engine. `TestAcc*` are acceptance tests that skip without `TF_ACC` or the environment they need.
- `tools/genskill/`: renders `.claude/skills/terraform-provider-docker/` (SKILL.md + reference/), `.claude/skills/docker-cli-map/SKILL.md` and README_resources.md from the live provider schema. `-check` fails on drift; `-strict` fails on missing descriptions.
- `tools/compose2stack/`: converts a compose file once into `docker_stack` HCL (swarm) or container/network/volume HCL (local).
- `tools/parity/`: kreuzwerker/docker v4.6.0 types and attributes vs ours. Types: `tools/parity/paritydata` (hand-maintained). Attributes: every kreuzwerker attribute (`paritydata/kreuzwerker_v4.6.0_attrs.json`) must appear in `reference/_gen/renames.json`, mapped or dropped with a reason; a `gap:` row fails, `won't implement:` passes. `-check BUILD_REPORT.md` also fails on a missing type with no `[BLOCKED]` line. `-guide` regenerates `templates/guides/migrating-from-kreuzwerker.md.tmpl` (Invoke-Build Guide / GuideCheck).
- `tools/apiwatch/`: diffs the pinned Docker Engine API spec (`pinned.json` + `spec/`) against the newest published one, mapped to provider types.
- `tools/drift/`: upstream drift for every reference/SOURCES.md source (git ls-remote tags, compose-spec main HEAD, docs.docker.com probe). Moved specs are fetched into TEMP and diffed with fieldcov `-swagger` / `-compose-schema` / `-bake-ref` / `-fields-out`; an unmapped new operation or field, or a doc-only release, fails unless listed in `reference/_gen/drift-accepted.json` (Invoke-Build DriftCheck; the weekly `.github/workflows/drift.yml` opens or updates the "Upstream drift" issue). It never changes a pin.
- `tools/deferred/`: generates `reference/_gen/deferred.json`, everything knowingly not done (fieldmap.d skips and partials, PARTIAL markers, inventory skips, the acceptance tests in the hand-maintained `reference/_gen/deferred.d/acceptance.json` and the example parts in `reference/_gen/deferred.d/examples.json`), each with its reason and source (Invoke-Build DeferredReport / DeferredCheck).
- `tools/pairwise/`: pairwise combination tests for `docker_container`, `docker_service`, `docker_network` and `docker_volume`. Reads the live provider schema, takes candidate values and Engine constraints from `reference/_gen/pairwise/<type>.json` (reviewable; a rejected combination carries the daemon's error text), writes configs covering every value pair (fixed seed, deterministic), and compares terraform state with the daemon's inspect through the contract field maps (Invoke-Build PairwiseGen / PairwiseTest).
- `test/TfDockerTest/`: PowerShell module behind the example tests (Invoke-TfExample: apply, an empty second plan, destroy with a dev-override CLI config and no init, 5-minute budget per example; Get-DockerObject; Assert-TfState). Every `examples/**/` folder has a `<name>.tests.ps1` built on it (Invoke-Build ExampleTests).
- `test/__manual/<scenario>/`: scenarios for a person at the keyboard (main.tf + README.md saying what to look at), driven by the underscore tasks `_List`, `_Up`, `_Init`, `_Plan`, `_Apply`, `_Destroy`, `_Down` (`-Scenario <name>`, `_Up -Swarm`). Nothing in the default task, PreRelease or CI may depend on an underscore task (ManualTaskCheck).
- `.goreleaser.yml`, `.github/workflows/release.yml`, `build/release.tasks.ps1`, `RELEASING.md`: the release pipeline (tag v* -> GoReleaser, GPG-signed SHA256SUMS, cosign bundle, SLSA provenance). `build/prerelease.tasks.ps1` holds DriftCheck and PreRelease; the build script dot-sources both task files.
- `tools/climap/commands.json`: the hand-maintained docker CLI command list behind the docker-cli-map skill.
- `tools/fieldcov/`: the field-level coverage gate. Every writable leaf field of the Engine API swagger (write operations the inventory maps to a resource, action or ephemeral resource), the compose-spec definitions and the bake target attributes is mapped to a provider attribute (auto-detected from the code and the schema), or carries a reasoned skip or partial in `reference/_gen/fieldmap.d/<lane>.json`. `reference/_gen/fieldmap.json` is generated (`Invoke-Build FieldMap`); `FieldCoverage` (default task) fails on any gap.
- `tools/doccounts/`: generates and checks the type counts in CLAUDE.md and README.md against `registry_gen.go` (`Invoke-Build DocCounts` / `DocCountsCheck`).
- `test/harness/`: the dind swarm harness (Engine 29.9.3, API 1.56: manager, worker1-4, TLS node and containerd-store node cdnode (`TF_ACC_CONTAINERD_HOST`), the others pinned to overlay2; registry:2, Docker Hub mirror). harness-up also installs, into `test/harness/.bin`, Terraform and the docker compose release that reference/SOURCES.md pins (`TF_ACC_COMPOSE_CLI`, used by TestAccComposeParity). `Invoke-E2E.ps1` is the only entry point for acceptance runs; `-Script <ps1>` runs a PowerShell script (ExampleTests, PairwiseTest) instead of go test under the same harness lifecycle.
- `examples/`, `templates/`, `docs/`: tfplugindocs inputs and output. `docs/` is generated; don't hand-edit it.
- `.claude/skills/`: agent skills (see the end of this file).
- `BUILD_REPORT.md`: the log of every pass.

## Standing rules

- **Never commit, push or tag.** Edit, `git add -A`, then stop with a report. The user commits.
- **One git worktree per lane** when a pass runs parallel lanes:
  - Location: `C:\__Code\tpd-wt\lane<X>`, detached at HEAD plus the Lane 0 changes copied in.
  - Lanes never share files. Each lane owns an explicit file list, and cross-lane needs are logged as TODO lines with the exact change.
  - The orchestrator merges by copying each lane's owned files back into the main tree. Diff with CRs ignored, because the checkout uses autocrlf.
- **Acceptance runs go through `test/harness/Invoke-E2E.ps1`** (or `Invoke-Build TestAcc`, which wraps it), never against the Docker Desktop daemon directly. The one exception is the npipe smoke test (see Build and test).
  - Only one lane or agent runs the harness at a time.
  - Swarm-manager tests need the `-SwarmInit` pass; see `$SwarmAccRegex` in terraform-provider-docker.build.ps1.
- **Waiting on long runs** (Pass 6b: a session polled for eight hours for a status file in a %TEMP% folder it had deleted itself):
  - Logs, pid and status files live under `test/harness/.logs/<pass>/` (git-ignored). Nothing a pass needs lives under %TEMP%.
  - Start every long command through `build/Invoke-Logged.ps1 -Name <name> -CapMinutes <cap> -Command '<command>'` (add `-HarnessDown` for harness runs). It writes `<name>.pid` at start, `<name>.log` / `<name>.err.log`, and `<name>.exit` (exit code and duration) at the end. At the cap it kills the process tree, runs harness-down with `-HarnessDown`, and writes exit `124`.
  - Never wait on a file in a loop. Check at most once a minute: does `<name>.exit` exist, and is a PID in `<name>.pid` alive? No exit file and no live PID means the run crashed: stop waiting and report the last 30 lines of the log.
  - Caps: ExampleTests 90 minutes, PairwiseTest 60, TestAcc 45, anything else 15. Never raise a cap. Never start a second run of the same task without saying why the first one failed.
  - Run the PreRelease gates one at a time through the wrapper, not PreRelease as one long command. A session stops and reports after 3 hours of wall clock.
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
  - if it replaces a CLI command, an entry in `tools/climap/commands.json`;
  - field coverage: its writable upstream fields mapped, skipped or partial (`Invoke-Build FieldCoverage`);
  - if it creates labellable Docker objects, an owner-label entry in `internal/provider/owner_label.go` (`TestOwnerLabelCoverage` fails otherwise; see reference/CONTRACT.md "Owner label").
- **PARTIAL(pass5) markers** record a registered type whose documented sub-feature is absent or a no-op:
  - Format: `// PARTIAL(pass5) <item id> <note>`, next to the code that lacks the feature. The item id is `layer:name` from `reference/inventory.json`; the note is required and names what is missing.
  - Not for placeholders: an unimplemented type is `// STUB(pass5) <item id>` (nothing after the id). Its status is `missing`, which fails InventoryValidate (validate.py S1; allowed-violations.txt cannot excuse it).
  - Status is computed by `reference/_gen/merge.py` from registry_gen.go and the markers (`partial` = registered with a PARTIAL marker). Never hand-set it in a fragment or the inventory. Partials are listed and never fail.
  - Only non-test `internal/provider/*.go` files are scanned (`*_test.go` is ignored). A marker with no note, or for an unknown, skipped or unregistered item, fails the merge.
  - Remove the marker in the change that implements the feature.
- Attributes with RequiresReplace say "Forces replacement." in their description (`genskill -strict`).
- At the start of every pass run the **engine-api-watch** skill (`go run ./tools/apiwatch`) and file what it finds.

## Build and test

[terraform-provider-docker.build.ps1](terraform-provider-docker.build.ps1) is an
[Invoke-Build](https://github.com/nightroman/Invoke-Build) script. Run it from the repo root
(`Install-Module InvokeBuild -Scope CurrentUser` once). Invoke-Build also runs under PowerShell 7 (`pwsh`) on
Linux and macOS, so the same tasks work there (the harness script `Invoke-E2E.ps1` requires PowerShell 7):

```
Invoke-Build ?                   # list tasks
Invoke-Build                     # default: Build, Vet, Test, GenSkillCheck, InventoryCheck, InventoryValidate, RegistryCheck, ContractCheck, DocCountsCheck, FieldCoverage, ExampleCoverage, ManualTaskCheck
Invoke-Build ContractGen         # regenerate internal/provider/contract_gen.go (outputs contract) from the inventory
Invoke-Build RegistryGen         # regenerate internal/provider/registry_gen.go from reference/inventory.json
Invoke-Build StubReport          # list STUB(pass5) stubs (fail) and PARTIAL(pass5) sub-feature gaps (listed)
Invoke-Build Build, Vet, Test    # go build + provider binary, go vet, unit tests (-short, TF_ACC unset)
Invoke-Build Tools               # unit tests of tools/ only
Invoke-Build GenSkill            # regenerate .claude/skills + README_resources.md (GenSkillCheck: -check)
Invoke-Build Docs                # GenSkill, then tfplugindocs generate + validate
Invoke-Build Check               # gofmt, vet, genskill -check/-strict, tfplugindocs validate
Invoke-Build TestAcc             # dind harness: plain pass, then -SwarmInit pass (TF_ACC=1)
Invoke-Build E2E                 # dind harness: TestAccE2E only
Invoke-Build ExampleTests        # every examples/**/<name>.tests.ps1 through Pester on the dind harness (plain + -SwarmInit pass), NUnit XML + summary in test/harness/.logs/examples; -Filter 'docker_tag,actions/*' re-runs a subset
Invoke-Build ExampleValidate     # example tests as a validate-only dry run (no harness); ExampleModuleTest: the TfDockerTest module's own Pester tests
Invoke-Build ExampleCoverage     # every registered type has an example and a test, or a deferred.d/examples.json entry (default task)
Invoke-Build ManualTaskCheck     # no task in the default task, $PreReleaseTasks or CI depends on an underscore task (default task)
Invoke-Build PairwiseTest        # tools/pairwise configs applied on the dind harness: inspect vs state, empty second plan, destroy (PairwiseGen: configs only)
Invoke-Build _List               # manual scenarios (test/__manual); then _Up [-Swarm], _Init/_Plan/_Apply/_Destroy -Scenario <name>, _Down
Invoke-Build Parity              # kreuzwerker parity: types + every attribute vs reference/_gen/renames.json (ParityCheck: + BUILD_REPORT.md [BLOCKED] lines, as CI)
Invoke-Build Guide               # regenerate the migration guide template from renames.json (GuideCheck: fail if stale)
Invoke-Build FieldCoverage       # every writable Engine API / compose-spec / bake field mapped, skipped or partial (enforce mode)
Invoke-Build FieldMap            # regenerate reference/_gen/fieldmap.json from the specs, the provider and reference/_gen/fieldmap.d
Invoke-Build ApiWatch            # Engine API drift vs tools/apiwatch/pinned.json (ApiWatchCheck: fail on a newer spec, as CI)
Invoke-Build DriftCheck          # upstream drift: newest release of every reference/SOURCES.md source vs its pin, moved specs diffed with fieldcov (needs the network)
Invoke-Build DeferredReport      # regenerate reference/_gen/deferred.json, totals by kind, items added/removed since HEAD (DeferredCheck: fail if stale)
Invoke-Build PreRelease          # every gate in $PreReleaseTasks (build/prerelease.tasks.ps1), runs on, one summary table; no tag/push/publish
Invoke-Build ReleaseCheck        # .goreleaser.yml matrix == $ReleaseTargets (build/release.tasks.ps1), every target cross-compiles (no goreleaser)
Invoke-Build Release             # local goreleaser snapshot into dist/ (never signs or publishes), then checks every asset
Invoke-Build DocCounts           # regenerate the type counts in CLAUDE.md / README.md (DocCountsCheck: fail on drift)
Invoke-Build Inventory           # reference/inventory.json + INVENTORY.md from reference/_gen/fragments (InventoryCheck: diff must be empty)
Invoke-Build InventoryValidate   # outputs contract and status: a missing item fails, partials are listed
Invoke-Build ReferenceVerify     # re-hash reference/upstream against the SHAs pinned in reference/SOURCES.md
Invoke-Build Compose2Stack -In docker-compose.yml -Out stack.tf -Mode swarm
```

Nobody hand-edits `reference/inventory.json` or `INVENTORY.md`: edit `reference/_gen/fragments/*.json`, then `Invoke-Build Inventory`.

- Invoke-Build is the only entry point; there is no Makefile. CI (`.github/workflows/ci.yml`, ubuntu, pwsh) installs InvokeBuild and runs, in order: `Invoke-Build` (the default task), `Invoke-Build Check`, `Invoke-Build ParityCheck, GuideCheck`, `Invoke-Build ReleaseCheck`, `Invoke-Build ApiWatchCheck`, then `Invoke-Build E2E` (dind harness through `Invoke-E2E.ps1`). Run the same tasks locally to reproduce a CI failure. A weekly workflow (`.github/workflows/drift.yml`) runs `Invoke-Build DriftCheck` and opens or updates the issue "Upstream drift" on failure; `.github/workflows/release.yml` builds, signs and publishes a pushed `v*` tag (RELEASING.md).
- The one acceptance test that talks to Docker Desktop directly is `TestAccNpipeSmoke` (`internal/provider/i_npipe_acc_test.go`): Windows only, gated by `TF_ACC_NPIPE=1`, it creates and removes only `tfacc-npipe-*` objects. Run it by hand: `TF_ACC=1 TF_ACC_NPIPE=1 go test -run TestAccNpipeSmoke ./internal/provider -timeout 15m -count=1`.
- Acceptance tests by hand go through the harness script, one run at a time. Add `-SwarmInit` for the swarm-manager tests and `-KeepUp` to leave the harness up:

```
pwsh -NoProfile -File test/harness/Invoke-E2E.ps1 -Run TestAccNetwork_basic -Timeout 30m
pwsh -NoProfile -File test/harness/Invoke-E2E.ps1 -SwarmInit -Run 'TestAccService' -Timeout 60m -Log test/harness/last-acc-swarm.log
```

## Skills

Agent skills live in [.claude/skills/](.claude/skills/):

- `terraform-provider-docker/`: how to write Terraform with this provider. Generated by `go run ./tools/genskill`; don't hand-edit.
- `docker-cli-map/`: the docker / compose / swarm CLI command to provider type map. Generated from `tools/climap/commands.json` + `tools/parity/paritydata`.
- `engine-api-watch/`: when and how to run `tools/apiwatch` and file its results. Hand-written.
