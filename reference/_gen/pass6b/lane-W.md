# Pass 6b Lane W

Pairwise combination tests for docker_container, docker_network, docker_volume and docker_service.

## Log

[P6b-W] [BUILD] tools/pairwise (Go, stdlib plus modules already in go.mod; go.mod/go.sum unchanged). The schema comes from the provider in-process (providerserver GetProviderSchema, as tools/genskill does): schema.go turns it into a node tree with kind (attribute, nested attribute, block), Terraform type, nesting and max items. A model that names an attribute, a nested key or an object key the provider does not have, a computed-only attribute, or a value of the wrong type fails generation with the known names listed. `-schema <type>` prints the live writable surface for model authors.
[P6b-W] [BUILD] Inspect paths come from the outputs contract, not a second mapping: contract.go reads reference/inventory.json (the source of internal/provider/contract_gen.go). It resolves each model's snake_case `inspect` path through the `fields` of the type's structured `inspect` output (engine:container, engine:network, engine:volume, swarm:service), for example host_config.memory -> HostConfig.Memory. A segment the contract does not list fails while the contract still has fields at that level. Below the listed levels (fields_complete false), it is matched at compare time by normalized name, which is the contract's rule for unlisted levels.
[P6b-W] [BUILD] Model format (reference/_gen/pairwise/<type>.json, documented in tools/pairwise/model.go):
- type, seed, inspect {item, endpoint, id_attr}, fixed (always set), prelude (extra HCL).
- attributes {<path>: {inspect, match equal|subset|none, no_inspect_reason, note, values}}. Paths may go through single blocks and nested attributes, and the first element of list blocks.
- A value is plain JSON (set, and expected as is), or {$set, $expect, $label, $hcl}; null means unset.
- constraints [{when, forbid|requires, reason, daemon_error}] name values by label or by JSON value.
[P6b-W] [BUILD] cover.go: an AETG-style greedy covering array with constraints. Pairs that no valid row can hold are computed by backtracking, counted as excluded and skipped. Each row keeps the best of 30 candidates; a candidate starts from the value with the most uncovered pairs and fills the other factors in shuffled order with the value that covers the most new pairs, never one that leaves the row unsatisfiable. PCG is seeded from the model's seed, so the same model gives byte-identical output.
[P6b-W] [BUILD] gen.go writes <out>/<type>/<nn>/main.tf: a standalone root module with required_providers JerryBalmer1/docker, an empty provider block (DOCKER_HOST from env), variable pw_registry (the harness registry prefix), locals.prefix = pw-<type>-<nn>, and resource "<type>" "pw". It also writes expect.json (assignment and checks, each with its upstream path) and <type>/array.json.
[P6b-W] [BUILD] compare.go:
- `-compare` reads state.json (terraform show -json), GETs the inspect endpoint from DOCKER_HOST (tcp only), and checks each expectation: equal, or subset (map keys, list elements matched to distinct elements, recursive). An unset pointer or omitempty field equals the zero value. Writes compare.json.
- `-plan-summary` lists the non-no-op resource changes of the second plan with the changed, unknown and replace paths. Writes plan-summary.json.
- `-report` merges run.json, compare.json and plan-summary.json of every config into one JSON file (per type: configs, applied, clean, findings, destroyed; per config: findings) and prints SUMMARY and FINDING lines.
[P6b-W] [BUILD] tools/pairwise/run.ps1 is the harness-side runner, started by Invoke-E2E.ps1 -Script. It refuses to run unless DOCKER_HOST is tcp://127.0.0.1:12375. Per config: init, apply, show -json, pairwise -compare, plan -detailed-exitcode (when it exits 2: show -json and pairwise -plan-summary), then destroy, which always runs, even after a failed apply. Each step's output goes to <step>.log and the outcome to run.json. Terraform is TF_ACC_TERRAFORM_PATH when the harness sets it (1.14.9 there), else terraform on PATH. Providers come from dev_overrides through TF_CLI_CONFIG_FILE. Images use TF_VAR_pw_registry = "$TF_ACC_REGISTRY_NODES/" (manager:12380/busybox:1.36).
[P6b-W] [BUILD] build/pairwise.tasks.ps1:
- PairwiseGen builds tools/pairwise and generates into $env:PAIRWISE_OUT/configs (default <temp>/tpd-pairwise; PAIRWISE_TYPES picks a subset).
- PairwiseTest (depends on PairwiseGen) builds the provider into PAIRWISE_OUT/bin/provider and writes the dev_overrides CLI config. It runs Invoke-E2E.ps1 -Script tools/pairwise/run.ps1 in a plain pass (container, network, volume), then a -SwarmInit pass (service), and writes PAIRWISE_OUT/results.json with `pairwise -report`. It fails on a failed harness pass or on any finding.
[P6b-W] [TEST] go test ./tools/pairwise: all pass.
- TestCoverSmallModelSizes: 3x3 gives 9 rows, 2^3 gives 4 (optimal), 3x2x2 gives 6 (lower bound).
- TestCoverConstraints: forbid and requires; excluded pair count; no row violates.
- TestCoverAllConstrainedAway.
- TestModelErrors (17 cases): unknown attribute, unknown nested path, unknown key in a block value, computed-only, wrong type, wrong list element, one value, unknown inspect field, missing inspect, none without a reason, bad match, constraint with an unknown value, unknown factor or no reason, overlapping factors, duplicate label, unknown $ key, unknown top-level key.
- TestResolveInspect.
- TestRenderConfig (HCL quoting and ${ escaping, blocks, list blocks, maps, prelude).
- TestRealModelsCoverEveryPair: the four real models against the live schema and inventory.json; every feasible pair covered, no constraint violated, 4-15 attributes.
- TestGenerateDeterministic: two generations are byte-identical.
- TestCheckAll, TestCompareDirAgainstFakeEngine (httptest Engine), TestSummarizePlan, TestReport, TestHCLString, TestRunModes.
[P6b-W] [VERIFIED] Every generated config passes `terraform validate` with the dev-override provider (offline, before the harness run).
[P6b-W] [BUILD] Models in reference/_gen/pairwise/. Each attribute's values include unset (null); the counts are after run 2's constraints.
- docker_container: 15 attributes, 44 values, 901 pairs, 25 configs. Attributes: restart, memory, cpu_shares, env, labels, ports (block: ephemeral, fixed host port, udp), healthcheck (block), mounts (block: tmpfs with size, bind read-only), init, read_only, user, cap_add, logging (block), ulimits (block), network_mode.
- docker_network: 11 attributes, 30 values, 2 constraints, 404 pairs plus 4 excluded, 17 configs. Attributes: driver (bridge, macvlan), internal, attachable, enable_ipv4, enable_ipv6, ipam_config (block: subnet+gateway, subnet+ip_range+aux_address), ipam_driver, ipam_options, options (mtu, icc), labels, scope.
- docker_volume: 5 attributes, 14 values, 77 pairs, 13 configs. Attributes: name (set or unset), driver, driver_opts (tmpfs, tmpfs size+uid, bind), labels, adopt.
- docker_service: 14 attributes, 37 values, 634 pairs, 18 configs. Attributes: mode, replicas, env, labels, container_labels, resources.limits.memory_bytes, resources.reservations.nano_cpus, restart_policy (block), update_config (block), healthcheck (block), endpoint_spec (block: ingress port, dnsrr), mounts (block), read_only, stop_grace_period.
[P6b-W] [ASSUMED] docker_volume has only 5 attributes because a local volume's writable surface is small. cluster_volume_spec needs a CSI volume plugin, which the harness does not have, so it is not modelled.
[P6b-W] [ASSUMED] docker_network runs in the plain pass, so the overlay driver (which needs a swarm) is not a value.
[P6b-W] [ASSUMED] Volume configs with name unset get an Engine-generated name, not the pw-volume-<nn> prefix. They run one at a time and are destroyed.
[P6b-W] [TEST] Harness run 1, under the harness lock, as Invoke-Build PairwiseTest (log %TEMP%\tpd-p6b\logs\lane-W-pairwise-run1.log, results lane-W-pairwise-run1-results.json):
| Type | Configs | Applied | Clean | Findings | Destroyed |
|---|---|---|---|---|---|
| docker_container | 25 | 25 | 14 | 11 | 25 |
| docker_network | 16 | 11 | 11 | 5 | 16 |
| docker_service | 18 | 18 | 12 | 6 | 18 |
| docker_volume | 13 | 5 | 5 | 8 | 13 |
Every applied config had an empty second plan.
[P6b-W] [FIXED] Model error, not a provider bug: docker_container cap_add accounts for all 11 container findings in run 1. Requested ["NET_ADMIN"], HostConfig.CapAdd ["CAP_NET_ADMIN"]. The Engine stores capabilities in canonical CAP_ form, and plan and state were clean. The model now expects ["CAP_NET_ADMIN"] (`$expect`, with a note).
[P6b-W] [FIXED] Finding: docker_network with enable_ipv4 = false and an IPv4 ipam_config pool, configs pw-network-04, -07 and -11 in run 1 (e.g. 07: driver bridge, enable_ipv6 true, internal true, ipam_config 10.232.0.0/24 + ip_range + aux_address).
- Apply failed with "Provider produced inconsistent result after apply: .ipam_config: block count changed from 1 to 0".
- Cause: the Engine accepts the pool and silently drops it (no error).
- Fix: internal/provider/network_validate_p6b_w.go adds docker_network ValidateConfig. It is an error at validate time ("IPv4 pool on a network with IPv4 disabled") when enable_ipv4 is known false and a known ipam_config subnet is IPv4. Unknown values and IPv6 pools pass.
- Tests: unit test TestNetworkValidateIPv4PoolWithIPv4Disabled (6 cases plus unknowns). Acceptance test TestAccNetworkP6bW_ipv4DisabledPool (skips without TF_ACC): a PlanOnly ExpectError step, then an IPv6-only network.
- No schema or description change.
[P6b-W] [FIXED] Finding: docker_volume without name, all 8 nameless configs in run 1 (pw-volume-01, 04, 06, 07, 09, 10, 11, 13).
- Apply failed with "Provider produced inconsistent result after apply: .labels: new element \"com.docker.volume.anonymous\" has appeared" (with labels set), or ".labels: was null, but now cty.MapVal({\"com.docker.volume.anonymous\": \"\"})" (labels unset).
- Cause: Engine 29 labels a volume created with Name "" as anonymous.
- Fix: internal/provider/volume_resource.go volumeFromInspect drops com.docker.volume.anonymous (new volumeStripAnonymous) unless the configuration sets that label.
- Tests: unit test TestVolumeFromInspectAnonymousLabel. Acceptance test TestAccVolumeP6bW_unnamed (skips without TF_ACC): an unnamed volume, then an unnamed volume with labels.
[P6b-W] [TEST] Constraints added to reference/_gen/pairwise/docker_network.json:
- (1) Engine rejection. When enable_ipv4 = false, enable_ipv6 must be true. daemon_error: "Error response from daemon: IPv4 or IPv6 must be enabled". Configs pw-network-01 (enable_ipv6 false) and pw-network-14 (enable_ipv6 unset) in run 1.
- (2) Not a daemon rejection. When enable_ipv4 = false, the IPv4 ipam_config values are forbidden. The Engine silently drops these pools; after the fix above the provider rejects the combination at validate time. The constraint records that reason.
- The other three models have no constraints.
[P6b-W] [ASSUMED] Constraint (1) was seen with the bridge driver and the default driver. In run 1, pw-network-04 (macvlan, enable_ipv4 false, enable_ipv6 unset, with an IPv4 pool) got past the daemon. The constraint is applied for every driver.
[P6b-W] [TEST] Harness run 2, after the fixes, the cap_add expectation and the network constraints. Under the lock, as Invoke-Build PairwiseTest (log lane-W-pairwise-run2.log, results lane-W-pairwise-run2-results.json):
| Type | Configs | Applied | Clean | Findings | Destroyed |
|---|---|---|---|---|---|
| docker_container | 25 | 25 | 25 | 0 | 25 |
| docker_network | 17 | 17 | 17 | 0 | 17 |
| docker_volume | 13 | 13 | 13 | 0 | 13 |
| docker_service | 18 | 18 | 12 | 6 | 18 |
Every second plan was empty (exit 0). PairwiseTest still fails on the 6 open service findings.
[P6b-W] [TODO] Open finding: docker_service with replicas set and mode = "global", configs pw-service-03, 04, 10 and 17 (replicas 1) and 13 and 18 (replicas 2), in run 1 and run 2.
- Example, pw-service-03: mode global, replicas 1, endpoint_spec ingress 18081, mounts tmpfs, read_only true, restart_policy any 5s, update_config parallelism 2 start-first, memory limit 128 MiB.
- Requested replicas 1; observed Spec.Mode.Replicated.Replicas absent (Spec.Mode is {"Global":{}}). The second plan is empty, and apply and destroy succeed.
- The provider drops replicas for non-replicated modes by design: svcExpandMode, and the replicas description says "ignored by the other modes".
- Added (internal/provider/service_validate_p6b_w.go): docker_service ValidateConfig warns ("replicas is ignored for this mode") when replicas is configured and mode is known and not replicated. Unit test TestServiceValidateReplicasIgnoredMode. It is a warning only, so existing configurations keep working; the mismatch itself stays.
- Decision for the user or Lane Z: either make this an error (then the model gets a constraint "when mode global, forbid replicas 1, 2"), or accept it as designed and add that constraint with the reason "replicas is ignored outside replicated mode (documented)". The pair was not dropped from the model.
[P6b-W] [VERIFIED] Gates in the worktree (logs in %TEMP%\tpd-p6b\logs\lane-W-*.log):
- go test ./tools/pairwise: ok.
- go build ./... and go vet ./...: ok.
- go test -short ./internal/provider: ok (final, with all three fixes, 154s).
- Invoke-Build FmtCheck: ok.
- PairwiseTest on the harness: run 2 numbers above. It fails only on the 6 open service findings.
- No schema or description changed, so docs/, .claude/skills, README_resources.md and fieldmap.json do not drift. go.mod and go.sum are unchanged.
[P6b-W] [TODO] Z: add `PairwiseTest` to the acceptance / pre-release story where it fits. It needs the harness, about 15 minutes for two harness up/down cycles, and is not in the default task. Suggested exact change: in build/prerelease.tasks.ps1 add 'PairwiseTest' to $PreReleaseTasks after 'TestAcc' (the file is Lane Z's), and add one line to CLAUDE.md "Build and test": `Invoke-Build PairwiseTest          # dind harness: pairwise combination configs (tools/pairwise, reference/_gen/pairwise) applied, inspect-compared, re-planned empty, destroyed`, plus `Invoke-Build PairwiseGen` (generate only). Also add a repo-map bullet for tools/pairwise in CLAUDE.md.
[P6b-W] [TODO] Z: BUILD_REPORT.md Pass 6b section: carry the findings above (2 fixed, 1 open by design; 1 model correction; 2 constraints).
[P6b-W] [TODO] Z: if the replicas/global decision is "error", change the warning in internal/provider/service_validate_p6b_w.go to AddAttributeError and update the replicas description in service_schema.go ("ignored by the other modes" -> "only valid with mode = replicated"). This is a schema description change, so regenerate docs and skills. Then add the constraint to reference/_gen/pairwise/docker_service.json.
