# Pass 5.1 Lane S: honest status

[P5.1-S] [BUILD] reference/_gen/merge.py: status markers are now two forms. `// STUB(pass5) <item id>` = placeholder, status missing (what tools/registrygen -stubs writes; unchanged). `// PARTIAL(pass5) <item id> <note>` = registered with a documented sub-feature absent or a no-op, status partial, note = status_note (several markers for one item are joined with "; "). Parsing is in one function, parse_markers().
[P5.1-S] [BUILD] Migrated, not kept: the Pass 5 form `// STUB(pass5) <id> partial: <note>` is now an error that names the new form. There was no marker of the old form left in the tree, so nothing had to move.
[P5.1-S] [BUILD] merge.py errors: a PARTIAL marker without a note; a marker without a valid `layer:name` id; text after a STUB id; a marker for an unknown, skipped or unregistered item (not in registry_gen.go). Test files (`*_test.go`) are still not scanned.
[P5.1-S] [BUILD] merge.py `--self-test` runs 13 marker cases (both forms, the old form, no note, blank note, no id, bad id, trailing text, missing space, mixed lines, near-misses). Every merge also runs the self-test and counts failures as errors, so `Invoke-Build Inventory` (and InventoryCheck) exercise it. `--provider <dir>` (only with `--dry-run`) reads registry_gen.go and the markers from another directory so marker forms can be tried without touching internal/provider.
[P5.1-S] [BUILD] reference/_gen/validate.py rule S1: an item whose status is `missing` (unregistered, or a STUB placeholder) fails InventoryValidate, and allowed-violations.txt cannot excuse it (an S1 line there is reported STALE). `partial` items are printed as `PARTIAL S1 <id>: <note>` and never fail.
[P5.1-S] [VERIFIED] What made "stubs fail the default task" true before this pass: nothing. merge.py computed `missing` for a stub but did not error; InventoryCheck only diffs the generated files against git (a committed `missing` passes); RegistryCheck passes because stub constructors are registered; StubReport only prints and is not in the default task. Now InventoryValidate (in the default task) fails on any stub or unregistered item, and partials pass.
[P5.1-S] [TEST] Dry runs (`merge.py --dry-run --provider <scratch copy of registry_gen.go + one marker file>`) on engine:volume: PARTIAL with note -> exit 0, status partial, note kept; STUB -> exit 0, status missing, and validate.py on that inventory exits 1 (`MISSING S1 engine:volume`), still 1 with an S1 line in --allowed; STUB ... partial: -> exit 1 (old form); PARTIAL without note -> exit 1; PARTIAL engine:no-such-item -> exit 1 (unknown); PARTIAL on an item removed from registry_gen.go -> exit 1 (unregistered); PARTIAL compose:version -> exit 1 (skipped); a marker in a _test.go file -> ignored, exit 0. `merge.py --self-test` -> ok.
[P5.1-S] [BUILD] PARTIAL markers next to the code, in Lane E files: buildx:bake in buildx_bake_parse.go (target: contexts, cross-target refs) and action_buildx_bake.go (policy, resources); buildx:history-inspect in datasource_buildx_history.go; buildx:builder in buildx_builder_resource.go; buildx:build in image_build_buildkit.go.
[P5.1-S] [BUILD] internal/provider/pass51_partial_s.go (comments only, each marker with its why and the file that lacks the feature): compose:cli-publish, engine:registry-image-data, compose:services, compose:models, compose:secrets, compose:configs.
[P5.1-S] [VERIFIED] Each marked gap was checked in code: bake returns "target: contexts are not supported" (buildkit_build.go bkExtra) and only logs "resources and policy are not applied"; history materials are a literal empty list and attachments carry digests/types only; docker_buildx_builder `driver` has OneOf(docker-container); build.platforms errors on more than one element (bake does pass platform lists); data docker_registry_image never reads InsecureSkipVerify; docker_compose_publish has only repository / insecure_skip_verify / result_path plus the source arguments (upstream compose_publish.md documents --with-env, --resolve-image-digests, --oci-version, --app); composeLocalUnsupported warns and ignores hooks, blkio_config, credential_spec, use_api_socket, external_links; composeLocalCreate treats any pull_policy other than never/always/build as missing; compose_resource.go: "Models are recorded but not run"; compose_swarm.go warns "driver and template_driver are not supported and are ignored". None of these is a decision in reference/DECISIONS.md.
[P5.1-S] [ASSUMED] buildx:build is marked for multi-platform even though the build.platforms description says "exactly one element": `docker buildx build --platform a,b` is upstream-documented, no DECISIONS.md entry drops it, and the stated reason (one platform per tag) does not hold on the containerd store.
[P5.1-S] [ASSUMED] engine:registry-image-data is marked although its description says "no effect": a direct registry read is possible (docker_registry_image_tags already does one), and renames.json records insecure_skip_verify as "implemented under the upstream name".
[P5.1-S] [ASSUMED] compose:models is marked: running models needs Docker Model Runner, which is not in the Engine API, the same class as the kubernetes/remote drivers the user asked to mark.
[P5.1-S] [VERIFIED] `Invoke-Build Inventory, InventoryValidate`: 252 items, 0 merge errors; 159 items, 1156 outputs, 0 violations; status 10 partial (listed), 0 missing. `Invoke-Build ContractCheck, RegistryCheck`: pass (138 types). `go build ./...` and `go vet ./internal/provider/` pass; gofmt clean on the six Go files. `p51_ownership.py --check S`: ok.
[P5.1-S] [TODO] Z: InventoryCheck fails until reference/inventory.json and reference/INVENTORY.md are staged (git diff --exit-code against the index); expected, the new statuses are the change.
[P5.1-S] [TODO] Z (from H's file): terraform-provider-docker.build.ps1: replace the whole `task StubReport { ... }` block (and its Synopsis line) with:
```
# Synopsis: List status markers in internal/provider: STUB(pass5) placeholders (fail) and PARTIAL(pass5) sub-feature gaps (listed only).
task StubReport {
    $dir = Join-Path $BuildRoot 'internal/provider'
    $hits = @(Get-ChildItem -LiteralPath $dir -Filter *.go -File |
            Where-Object { $_.Name -notlike '*_test.go' } |
            Select-String -Pattern '//[ \t]*(STUB|PARTIAL)\(pass5\)[ \t]+(\S+)[ \t]*(.*)$' -CaseSensitive)
    $stubs = @($hits | Where-Object { $_.Matches[0].Groups[1].Value -eq 'STUB' })
    $partials = @($hits | Where-Object { $_.Matches[0].Groups[1].Value -eq 'PARTIAL' })
    print Cyan ("Stubs ({0}):" -f $stubs.Count)
    foreach ($h in ($stubs | Sort-Object Filename, LineNumber)) { print Gray ("  {0}  ({1}:{2})" -f $h.Matches[0].Groups[2].Value, $h.Filename, $h.LineNumber) }
    print Cyan ("Partials ({0}):" -f $partials.Count)
    foreach ($h in ($partials | Sort-Object { $_.Matches[0].Groups[2].Value }, Filename, LineNumber)) { $m = $h.Matches[0]; print Gray ("  {0}  {1}  ({2}:{3})" -f $m.Groups[2].Value, $m.Groups[3].Value.Trim(), $h.Filename, $h.LineNumber) }
    $msg = "StubReport: {0} stubs, {1} partials" -f $stubs.Count, $partials.Count
    if ($stubs.Count) { throw "$msg. Stubs fail: implement each one (reference/inventory.json shows them as missing)." }
    print $(if ($partials.Count) { 'Yellow' } else { 'Green' }) $msg
}
```
  The body was dry-run in PowerShell 7 against the tree: "Stubs (0)", "Partials (11)" (10 items; buildx:bake has two markers), "StubReport: 0 stubs, 11 partials", no throw. The default task needs no build-file change: InventoryValidate already fails on stubs (S1).
[P5.1-S] [TODO] Z: reference/_gen/SCHEMA.md lines 35-36: replace "partial =\n  // registered with a `STUB(pass5) <id> partial: <note>` marker in internal/provider/*.go; exists = registered, no marker." with "partial =\n  // registered with a `// PARTIAL(pass5) <id> <note>` marker in internal/provider/*.go (a documented sub-feature absent or a no-op; the note is required); exists = registered, no marker. validate.py S1 fails on missing, never on partial."
[P5.1-S] [TODO] Z: reference/_gen/README.md line 87: "and the `STUB(pass5)` marker lines" -> "and the `STUB(pass5) <id>` (missing) and `PARTIAL(pass5) <id> <note>` (partial) marker lines".
[P5.1-S] [TODO] Z: CLAUDE.md line 80: `Invoke-Build StubReport          # list the Pass 5 stubs / STUB(pass5) markers left` -> `Invoke-Build StubReport          # list STUB(pass5) stubs (fail) and PARTIAL(pass5) sub-feature gaps (listed)`. Also worth one standing-rule line: "A registered type missing a documented sub-feature carries `// PARTIAL(pass5) <item id> <note>` next to the code that lacks it; remove it in the change that implements it."
[P5.1-S] [TODO] Z (optional): tools/parity/paritydata already calls docker_buildx_builder and docker_compose partial; nothing to change there.

## Considered and not marked

- compose:jobs and docker_compose_run refusing jobs: decided in DECISIONS.md Q18 (warning only; run-on-apply later).
- compose `develop`/watch: its own skipped item (compose:service-develop). `attach`: CLI log streaming, nothing to apply. `provider` services: they run an external binary, and nothing shells out.
- Service keys ignored in docker_compose swarm mode (build, container_name, privileged, devices, ...): `docker stack deploy` ignores the same keys.
- Volume definition changes warned about, never applied (Pass 3 Lane B): the compose CLI does not recreate a changed volume either.
- Pass 5 [ASSUMED] behaviour choices with the feature present: docker_service_logs tail per service, remove_orphans default true, config-hash not resolving service: refs, bake variables only from `variables`, disk usage through BuildKit, builder last_activity and name heuristic, docker_tag id, registry_token object, docker_compose_run failing on non-zero exit.
- Re-encoded `*_json` outputs (moby client v0.6.2 has no raw body): output fidelity, documented, not a missing sub-feature.
- P3-D linked-resource actions: a framework v1.19 limit, not a documented feature of those types.
- P5-Z acceptance tests that never run on the harness (CSI/plugin/Hub, node promote): test coverage, not a provider feature.
- data docker_buildx_builder / docker_buildx_builders seeing only daemon-run builders: a consequence of the buildx:builder driver/instance-store gap, already marked there.
- Pass 1 registry_auth credHelpers TODO: implemented since (allow_credential_helpers); provider-level, no inventory item.
- engine:image-attestations (Pass 4 gate TODO): registered and implemented in Pass 5.

## Summary

Status is now honest: 10 of 171 non-skipped items are partial (11 markers), none missing. Markers have two forms, STUB(pass5) <id> (missing) and PARTIAL(pass5) <id> <note> (partial). The old `STUB ... partial:` form was migrated: it is now an error, and none were left in the tree. Malformed markers and markers for unknown, skipped or unregistered items fail the merge. merge.py self-tests its parser on every run. validate.py S1 makes any stub or unregistered item fail InventoryValidate in the default task. Before this, nothing in the default task failed on a stub. Partials never fail.

Inventory totals (exists / partial / missing), before (HEAD) -> after:

| kind | before | after |
|---|---|---|
| resources (28) | 28 / 0 / 0 | 22 / 6 / 0 |
| data sources (55) | 55 / 0 / 0 | 53 / 2 / 0 |
| actions (51) | 51 / 0 / 0 | 49 / 2 / 0 |
| ephemeral (4) | 4 / 0 / 0 | 4 / 0 / 0 |
| list resources (8) | 8 / 0 / 0 | 8 / 0 / 0 |
| functions (4) | 4 / 0 / 0 | 4 / 0 / 0 |
| blocks (21) | 21 / 0 / 0 | 21 / 0 / 0 |
| **all non-skipped (171)** | 171 / 0 / 0 | 161 / 10 / 0 |
| skipped | 81 | 81 |

By layer, after: engine 1 partial (data), compose 5 partial (4 resource facets of docker_compose + 1 action), buildx 4 partial (2 resource items, 1 data, 1 action).

Partial items: buildx:bake, buildx:build, buildx:builder, buildx:history-inspect, compose:cli-publish, compose:services, compose:models, compose:secrets, compose:configs, engine:registry-image-data (notes in reference/INVENTORY.md and above).
