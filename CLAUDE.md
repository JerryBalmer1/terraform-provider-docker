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
- `tools/drift/`: upstream drift for every reference/SOURCES.md source (git ls-remote tags, compose-spec main HEAD, docs.docker.com probe). Moved specs are fetched into the work directory (test/harness/.logs/drift) and diffed with fieldcov `-swagger` / `-compose-schema` / `-bake-ref` / `-fields-out`. Doc-only sources get a surface diff of pin vs new release (`tools/drift/surface.go`, a sparse git checkout): compose and docker CLI commands and options, buildx commands and options, and the Dockerfile reference's instructions, flags, parser directives, build checks and build args. A moved source with no surface change passes with a notice and a row in `reference/_gen/drift-log.json`. An unmapped new operation or field, or an added, removed or changed surface item, fails unless deferred in `reference/_gen/deferred.d/upstream.json` or listed in `reference/_gen/drift-accepted.json` (Invoke-Build DriftCheck; the weekly `.github/workflows/drift.yml` opens or updates the "Upstream drift" issue). It never changes a pin.
- `tools/deferred/`: generates `reference/_gen/deferred.json`, everything knowingly not done (fieldmap.d skips and partials, PARTIAL markers, inventory skips, the acceptance tests in the hand-maintained `reference/_gen/deferred.d/acceptance.json` the example parts in `reference/_gen/deferred.d/examples.json` the uncovered preflight diagnostics in `reference/_gen/deferred.d/diagnostics.json` and the known issues a release ships with in `reference/_gen/deferred.d/known_issues.json`, each with a reproduction and checked against its docs page and CHANGELOG.md), each with its reason and source Upstream surface items DriftCheck found and that are knowingly not implemented are kind `upstream_surface`, from the hand-maintained `reference/_gen/deferred.d/upstream.json` (Invoke-Build DeferredReport / DeferredCheck).
- `tools/pairwise/`: pairwise combination tests for `docker_container`, `docker_service`, `docker_network` and `docker_volume`. Reads the live provider schema, takes candidate values and Engine constraints from `reference/_gen/pairwise/<type>.json` (reviewable; a rejected combination carries the daemon's error text), writes configs covering every value pair (fixed seed, deterministic), and compares terraform state with the daemon's inspect through the contract field maps (Invoke-Build PairwiseGen / PairwiseTest).
- `test/TfDockerTest/`: PowerShell module behind the example tests (Invoke-TfExample: a real terraform init from a local filesystem mirror of the provider (lock file written, checksums verified), apply, an empty second plan, destroy, 5-minute budget per example; it fails on any "Outputs contract value dropped" in terraform output; Get-DockerObject; Assert-TfState). Every `examples/**/` folder has a `<name>.tests.ps1` built on it (Invoke-Build ExampleTests).
- `test/__manual/<scenario>/`: scenarios for a person at the keyboard (main.tf + README.md saying what to look at), driven by the underscore tasks `_List`, `_Up`, `_Init`, `_Plan`, `_Apply`, `_Destroy`, `_Down` (`-Scenario <name>`, `_Up -Swarm`; `_Apply` / `_Destroy` run unattended, `-Prompt` asks; working copies and state in test/harness/.work/manual/<scenario>). Nothing in the default task, PreRelease or CI may depend on an underscore task (ManualTaskCheck).
- `.goreleaser.yml`, `.github/workflows/release.yml`, `build/release.tasks.ps1`, `RELEASING.md`: the release pipeline (tag v* -> GoReleaser, GPG-signed SHA256SUMS, cosign bundle, SLSA provenance). `build/prerelease.tasks.ps1` holds DriftCheck and PreRelease; the build script dot-sources both task files.
- `tools/releaseassets/`: the release asset-name gate (ReleaseCheck, Release). The Terraform Registry rejects any release asset besides the zips, `_manifest.json`, `_SHA256SUMS` and `_SHA256SUMS.sig`; it checks dist/ and that no `.github/workflows/release.yml` step uploads anything else.
- `reference/_gen/pins.py`: reference/SOURCES.md is the single source of the upstream pins; merge.py reads them through it, and `Invoke-Build PinCheck` (in InventoryCheck) fails on any other copy that differs.
- `tools/climap/commands.json`: the hand-maintained docker CLI command list behind the docker-cli-map skill.
- `tools/fieldcov/`: the field-level coverage gate. Every writable leaf field of the Engine API swagger (write operations the inventory maps to a resource, action or ephemeral resource), the compose-spec definitions and the bake target attributes is mapped to a provider attribute (auto-detected from the code and the schema), or carries a reasoned skip or partial in `reference/_gen/fieldmap.d/<lane>.json`. `reference/_gen/fieldmap.json` is generated (`Invoke-Build FieldMap`); `FieldCoverage` (default task) fails on any gap.
- `tools/ledger/` + `build/ledger.tasks.ps1`: the gate ledger. `test/harness/.ledger/<gate>.json` (committed) holds one row per test unit (key, input hash, status, pass time, commit, duration) for four gates: `unit` (one Go package, the default task's Test), `testacc` (one top-level TestAcc function), `examples` (one example folder), `pairwise` (one generated config). Inputs are inferred: Go imports, embeds, testdata and path literals for packages; for TestAcc a declaration graph of internal/provider (registry_gen.go is a barrier; a type's code enters through its name in a configuration string); for examples and pairwise configs the type names in their files. `tools/ledger/static.json` declares extra inputs, vouches for packages that run programs, or marks a unit unknown (always run). Every gate task calls `Invoke-LedgerGate`.
- `tools/doccounts/`: generates and checks the type counts in CLAUDE.md and README.md against `registry_gen.go`, and README.md's evidence block (writable fields from reference/_gen/fieldmap.json, TestAcc functions, TestAccComposeParity fixtures, examples/**/*.tests.ps1, tools/pairwise configs) (`Invoke-Build DocCounts` / `DocCountsCheck`).
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
- **Gate ledger** (Pass 6g): Test, TestAcc, ExampleTests and PairwiseTest skip a unit whose input hash equals the one recorded with its last pass, and report it as "skipped: unchanged since <commit>". The rules:
  - A unit runs when its hash changed, it has no row, its last run failed, or its inputs are unknown (the plan says why).
  - The hash covers the unit's inferred inputs plus the always-in set: go.mod, go.sum, registry_gen.go, contract_gen.go, reference/SOURCES.md, the build script, and everything under test/harness/ (except git-ignored files and .ledger), build/ and tools/ledger/. Harness gates also hash the TF_ACC_* / TPD_ACC_* / DOCKER_HUB_* environment. Any change under test/harness/, build/ or tools/ledger/ runs every unit of every gate.
  - `Invoke-Build <gate> -Full` (or `TPD_LEDGER=off`) ignores the ledger and runs everything. PreRelease always passes -Full: a release never skips. Run the PreRelease gates one at a time with -Full too.
  - A row is written only by tools/ledger, at the end of the gate (a crashed run writes nothing): a pass row only for a unit that passed in this run, with this run's input hash. Never edit a row by hand. To force one unit, change its input or run the gate with -Full.
  - Each gate prints `<gate> gate: ran N, skipped M, failed K`, and the Invoke-Logged exit line carries the same.
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
Invoke-Build TestAcc             # dind harness: plain pass, then -SwarmInit pass (TF_ACC=1); unchanged TestAcc functions skipped by the gate ledger (-Full: all)
Invoke-Build E2E                 # dind harness: TestAccE2E only
Invoke-Build ExampleTests        # every examples/**/<name>.tests.ps1 through Pester on the dind harness (plain + -SwarmInit pass), NUnit XML + summary in test/harness/.logs/examples; -Filter 'docker_tag,actions/*' re-runs a subset
Invoke-Build ExampleValidate     # example tests as a validate-only dry run (no harness); ExampleModuleTest: the TfDockerTest module's own Pester tests
Invoke-Build ExampleCoverage     # every registered type has an example and a test, or a deferred.d/examples.json entry (default task)
Invoke-Build ManualTaskCheck     # no task in the default task, $PreReleaseTasks or CI depends on an underscore task (default task)
Invoke-Build PairwiseTest        # tools/pairwise configs applied on the dind harness: inspect vs state, empty second plan, destroy; output under test/harness/.logs/pairwise (PairwiseGen: configs only)
Invoke-Build _List               # manual scenarios (test/__manual); then _Up [-Swarm], _Init/_Plan/_Apply/_Destroy -Scenario <name>, _Down
Invoke-Build Parity              # kreuzwerker parity: types + every attribute vs reference/_gen/renames.json (ParityCheck: + BUILD_REPORT.md [BLOCKED] lines, as CI)
Invoke-Build Guide               # regenerate the migration guide template from renames.json (GuideCheck: fail if stale)
Invoke-Build FieldCoverage       # every writable Engine API / compose-spec / bake field mapped, skipped or partial (enforce mode)
Invoke-Build FieldMap            # regenerate reference/_gen/fieldmap.json from the specs, the provider and reference/_gen/fieldmap.d
Invoke-Build ApiWatch            # Engine API drift vs tools/apiwatch/pinned.json (ApiWatchCheck: fail on a newer spec, as CI)
Invoke-Build DriftCheck          # upstream drift: newest release of every reference/SOURCES.md source vs its pin, moved specs diffed with fieldcov (needs the network; work and report under test/harness/.logs/drift)
Invoke-Build DeferredReport      # regenerate reference/_gen/deferred.json, totals by kind, items added/removed since HEAD (DeferredCheck: fail if stale)
Invoke-Build PreRelease          # every gate in $PreReleaseTasks (build/prerelease.tasks.ps1), runs on, one summary table; no tag/push/publish
Invoke-Build ReleaseCheck        # .goreleaser.yml matrix == $ReleaseTargets (build/release.tasks.ps1), asset-name gate (release.yml; dist/ if present), every target cross-compiles (no goreleaser)
Invoke-Build Release             # local goreleaser snapshot into dist/ (never signs or publishes), then checks every asset and the asset-name gate
Invoke-Build RegistrySmoke       # after publishing only: terraform init of jerrybalmer1/docker at CHANGELOG's top released version, signed by A18B1B39E1DFDD4C
Invoke-Build PinCheck            # every pinned version / hash in generators, build scripts, apiwatch, go.mod, harness == reference/SOURCES.md (in InventoryCheck)
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
