# Pass 5.3 Lane A: single-host engine objects

Worktree `%TEMP%\tpd-p53\laneA`, base LANE0_TREE 301b9e0ec61607cc696c380625d865d6339b2f7d. Patch `%TEMP%\tpd-p53\lane-A.patch`,
listing `reference/_gen/pass53/lane-A.files`. Nothing committed.

## Log

[P5.3-A] [BUILD] D1 docker_container `state` = running | created | exited (Docker `State.Status` names), internal/provider/ctr_p53.go + container_resource.go. Default `running`; running<->created in place, to/from `exited` forces replacement (ctrStateReplaceIf).
[P5.3-A] [BUILD] D1 `must_run` removed; schema version 0 -> 1 with a JSON state upgrader (ctrUpgradeV0): drops must_run, sets state = "running", fills expected_exit_codes / logs_tail defaults, prunes keys the current schema lacks at every level (attributes earlier passes removed without a version bump).
[P5.3-A] [BUILD] D1 state = "exited": create, start, ContainerWait(not-running) within wait_timeout (default 5m for exited, 60s otherwise; set in ModifyPlan when the config leaves it unset), then ContainerLogs (tail = logs_tail, default 1000). Outputs exit_code, finished_at (State.FinishedAt), logs + log_entries (shapes and sensitivity reused from the engine:container-logs contract). Exit code outside expected_exit_codes (default [0]) fails the apply with the last 20 lines; the container stays in state (tainted). A refresh keeps the captured output and never starts it again; a missing container is removed from state (plan to create).
[P5.3-A] [BUILD] D1 state = "created": never started by the provider; found running, ModifyPlan adds a warning and plans no change. state = "running": a stopped container is planned running=true and started in place (unchanged Pass 2 behaviour).
[P5.3-A] [BUILD] D1 validation (ValidateConfig): auto_remove + exited is an error pointing at docker_container_run; wait = true needs state = "running"; restart other than "no" with exited is an error (it would restart the one-shot run).
[P5.3-A] [BUILD] D1 files block: in-place re-upload now only for a stopped container with state = "created" (was must_run = false).
[P5.3-A] [FIXED] D1 Read rule / Lane I bug: stop_timeout read back only when the prior value was set (containerStopTimeoutRead, container_extra.go). Docker Desktop 29.5.3 fills Config.StopTimeout=1 when the create request omitted it, which produced "Provider produced inconsistent result after apply ... stop_timeout: was null, but now cty.NumberIntVal(1)". Unit test TestCtrStopTimeoutDaemonDefault (fake Engine returns StopTimeout=1 for a container created without it).
[P5.3-A] [VERIFIED] D1 Read rule: every argument comes from Config / HostConfig; ports from HostConfig.PortBindings (never NetworkSettings.Ports). TestCtrStoppedPortsNoDiff (exited container, no NetworkSettings.Ports: ports, mounts, networks unchanged) and TestCtrStateCreated (created container with a port binding) cover the #211 class; acceptance TestAccContainerP53_exited / _createdNotStarted check an empty second plan on the harness.
[P5.3-A] [BUILD] D1 migration: kwContainerStateRule (move_container.go): start = false -> created, attach = true -> exited, otherwise running; must_run no longer mapped. reference/_gen/pass5/renames_overrides.json: rows for start / attach (our_attr state), must_run (dropped, reason), logs / container_logs (reasons updated). renames.json regenerated locally with renames_post.py only to test (left out of the hand-over).
[P5.3-A] [BUILD] D1 guide templates/guides/container-state.md.tmpl (tfplugindocs guide; the user's docs-templates\guides\container-state.md).
[P5.3-A] [BUILD] D2 action docker_container_run (action_container_run.go, NewContainerRunAction): ensure image, ContainerCreate, ContainerStart, ContainerWait, ContainerLogs, ContainerDelete (remove default true, also on failure). Result {exit_code, stdout, stderr} (stdout/stderr sensitive fields) to progress + result_path; expected_exit_codes (default [0]); timeout (default 5m); tail (default 1000). Inventory item engine:container-run in reference/_gen/fragments/pass53-A.json (no `primary` flags: ContainerCreate/Start/Wait are primary in other items, merge.py rejects duplicates), outputs in fragments/outputs/P53A.json. Example examples/actions/docker_container_run/action.tf.
[P5.3-A] [BUILD] Owner label: docker_container, docker_network, docker_volume wired (adopt attribute, ownerCreateCheck before create when owner is set, ownerInject, ownerRecord in Read, ownerStrip on labels, ownerPlanCheck in ModifyPlan; network and volume got a ModifyPlan). docker_container_run is inject-only (Lane 0 update 2): pd.ownerInject on the run container's labels, no lookup, no conflict check, no adopt.
[P5.3-A] [BUILD] Field coverage, docker_container: annotations, blkio_weight (in place), blkio_weight_device, cgroup, console_size, cpu_count, cpu_percent, cpu_realtime_period / cpu_realtime_runtime (in place), cpuset_mems (in place), io_maximum_bandwidth, io_maximum_iops, isolation, links (daemon form "/name:/ctr/alias" read back as the user's form), masked_paths, readonly_paths, memory_swappiness, network_disabled, oom_kill_disable, oom_score_adj, pids_limit (in place; clearing lifts the limit in place), umask (octal string, apiSince 1.56: api/docs/CHANGELOG.md:21), uts_mode, volume_driver, shell (used by CMD-SHELL healthchecks), volumes (Config.Volumes anonymous volumes; image VOLUMEs not reported).
[P5.3-A] [BUILD] Field coverage, mounts: consistency; bind_options.non_recursive / create_mountpoint / read_only_non_recursive / read_only_force_recursive; tmpfs_options.options (list(list(string)), apiSince 1.46: CHANGELOG.md:304); type = "image" with image_options.subpath (apiSince 1.48: CHANGELOG.md:222; create-time check ctrCheckMountsAPI).
[P5.3-A] [BUILD] Field coverage, networks entries: links (EndpointSettings.Links), an input that reconnects in place like the other endpoint settings.
[P5.3-A] [BUILD] Field coverage, docker_network: scope (now Optional+Computed, Forces replacement), config_only, config_from, enable_ipv4 (apiSince 1.48: CHANGELOG.md:247).
[P5.3-A] [BUILD] Field coverage, docker_exec: console_size (ExecCreate, ExecStart and ExecAttach ConsoleSize; needs tty). docker_container_cp: copy_uidgid (PutContainerArchive copyUIDGID).
[P5.3-A] [VERIFIED] D3 start body: ContainerStart in moby client v0.6.2 sends no body; TestCtrStartSendsNoBody and TestContainerRunAction assert an empty body on POST /containers/{id}/start (fake Engine records the body length).
[P5.3-A] [VERIFIED] D3 Moby #50133: the fix (containerStop's deferred `ctr.Lock()` "Ensure container status changes are committed by handler of container exit before returning control to the caller") is present in the daemon sources in the module cache: github.com/docker/docker@v28.5.1+incompatible/daemon/stop.go:77, @v28.5.2/daemon/stop.go:74-80, github.com/moby/moby@v28.5.2+incompatible/daemon/stop.go:77.
[P5.3-A] [ASSUMED] D3 Moby #50133 minimum daemon version: the oldest daemon source available locally is v28.5.1, which has the fix; that this code is moby PR #50136 and the first release carrying it could not be verified from local sources (no git history of moby, no release notes under reference/upstream). Minimum verified: Docker Engine 28.5.1.
[P5.3-A] [TODO] 0 tools/fieldcov does not see client option fields whose names differ from the Engine parameter: ContainerStopOptions/ContainerRestartOptions.Timeout -> query `t` (cop_container_verbs.go writes it), CopyToContainerOptions.AllowOverwriteDirWithFile -> `noOverwriteDirNonDir` (inverted), NetworkCreateOptions.ConfigFrom (string) -> ConfigFrom.Network, PluginInstall/PluginUpgrade/PluginSet option bodies. Mapped manually in fieldmap.d/A.json.
[P5.3-A] [TODO] 0 tools/fieldcov reports engine:VolumeCreateRequest/ClusterVolumeSpec/AccessMode/Availability and engine:VolumeUpdate/Spec/AccessMode/Availability: Availability belongs to ClusterVolumeSpec in moby's Go type (volume.ClusterVolumeSpec.Availability, written by obj_cluster_volume.go:177), so the AccessMode level looks like a swagger nesting artefact the resolver follows. Mapped manually to cluster_volume_spec.availability.
[P5.3-A] [TEST] Unit (httptest fake Engine): ctr_p53_a_test.go (TestCtrP53Schema, TestCtrP53ExpandRead, TestCtrP53ReadHelpers, TestCtrStateCreated, TestCtrStoppedPortsNoDiff, TestCtrStateExited, TestCtrStartSendsNoBody, TestCtrStopTimeoutDaemonDefault, TestCtrModifyPlanState, TestCtrValidateState, TestCtrMountOptionsP53, TestCtrOwnerLabel, TestNetworkOwnerAndFields, TestVolumeOwnerLabel, TestContainerRunAction, TestExecConsoleSize) and move_container_state_a_test.go (TestMoveContainerState via kwMustMove, TestContainerUpgradeStateV0): pass.
[P5.3-A] [TEST] Harness (test/harness/Invoke-E2E.ps1, lock taken as lane A, Terraform 1.14.9, -Run 'TestAccContainerP53_|TestAccContainerRun_basic|TestAccContainerP3_mustRunRestart'): TestAccContainerP53_exited PASS, TestAccContainerP53_createdNotStarted PASS, TestAccContainerP53_startStopInspect PASS (5 stop -> immediate inspect rounds all "exited"), TestAccContainerRun_basic PASS, TestAccContainerP3_mustRunRestart (now state = "running") PASS. 0 fail, 0 skip.
[P5.3-A] [TEST] `Invoke-Build Build, Vet, Test, FmtCheck, FieldCoverage` passes in the worktree with the Z TODOs 1-4 below applied locally (they were verified that way, then reverted for the hand-over). With them reverted, three tests fail by design until Z applies them: TestMoveContainerPass5Schema (must_run), tools/genskill TestCLIMapRealProvider (climap names must_run), tools/parity TestGuideTemplateCurrent (renames changed). fieldcov: 0 gaps for lane A, no ERROR lines. go.mod / go.sum unchanged.

## Summary

### D items

- **D1** done: `state` with the three values, exited one-shot runs with outputs, created never started (warning when found running), validation, Read rule (stop_timeout fix, #211 tests), own-state upgrade v0 -> v1, kreuzwerker move rule, renames_overrides rows, guide `templates/guides/container-state.md.tmpl`.
- **D2** done: docker_container_run action, inventory fragment + outputs, example, unit + acceptance tests; climap and paritydata rows are TODOs for Z (below).
- **D3** done: unit test (empty start body), acceptance tests for start and stop-then-inspect; Moby #50133 fix verified present in daemon 28.5.1+ sources, earlier versions [ASSUMED] unverifiable locally.

### Field gaps (reference/_gen/pass53/field-gaps/A.json): 96 before, 0 after

- Implemented under the upstream name (auto-mapped by fieldcov): 55, listed in the BUILD lines above (container HostConfig / Resources / Config fields, ContainerUpdate resources, mount options, endpoint Links, network fields, exec ConsoleSize, PutContainerArchive copyUIDGID).
- Manual attr mappings (13, fieldmap.d/A.json): ContainerStop?t, ContainerRestart?t -> `timeout`; NetworkCreate/ConfigFrom/Network -> `config_from`; PluginCreate#body -> `context_dir`; PluginPull?remote, PluginUpgrade?remote -> `name`; PluginPull#body, PluginUpgrade#body -> `grant_permissions`; PluginSet#body -> `env`; PutContainerArchive?noOverwriteDirNonDir -> docker_container_cp `allow_overwrite_dir_with_file`; PutContainerArchive#body -> `files`; both volume Availability pointers -> `cluster_volume_spec.availability`.
- Skips (28), every reason in fieldmap.d/A.json:
  - ContainerCreate AttachStdin / AttachStdout / AttachStderr / StdinOnce: the provider never attaches (output is read with ContainerLogs after the exit).
  - ContainerCreate ArgsEscaped: Windows-only builder flag (swagger.yaml:1420), commands are exec-form arrays.
  - HostConfig.ContainerIDFile: client-side `--cidfile` (container_run.md:175-187); the ID is the `id` output.
  - ContainerCreate OnBuild: image build metadata (swagger.yaml:1461).
  - EndpointSettings NetworkID, EndpointID, Gateway, IPAddress, IPPrefixLen, IPv6Gateway, GlobalIPv6Address, GlobalIPv6PrefixLen (create and NetworkConnect, 16 pointers): daemon-assigned operational data (swagger.yaml:3397-3450); static addresses go through IPAMConfig; reported as computed attributes of each networks entry.
  - EndpointSettings DNSNames (2): daemon-computed (swagger.yaml:3451-3463, CHANGELOG.md:421-423).
  - ContainerExec AttachStdin: the exec actions have no stdin input.
  - ContainerExec DetachKeys, ContainerStart?detachKeys: interactive detach sequence only.
- Partial: none.

### PARTIAL markers

None existed on lane A items and none were added.

### Owner label wiring

| type | inject | create conflict check | adopt | Read record + strip | unit test |
|---|---|---|---|---|---|
| docker_container | yes | yes (ContainerInspect by name) | yes | yes | TestCtrOwnerLabel |
| docker_network | yes | yes (NetworkInspect by name) | yes | yes | TestNetworkOwnerAndFields |
| docker_volume | yes | yes (before the existing "already exists" error) | yes | yes | TestVolumeOwnerLabel |
| docker_container_run | yes (inject only) | no | no | n/a | TestContainerRunAction |

### Tests

- Unit: all pass (see TEST lines), including the full `./...` run.
- Acceptance on the harness: 5 run, 5 PASS, 0 FAIL, 0 SKIP.
- Gate: passes with the Z TODOs applied locally; three Z-owned tests need TODOs 1, 2 and 4.

### TODOs for other lanes / Z (exact changes, all verified locally, then reverted)

1. [TODO] Z internal/provider/move_container_test.go (TestMoveContainerPass5Schema): in kwPass5ContainerType replace `"must_run", tB,` with `"state", tS,`; in `want` replace `"must_run": true,` with `"state":    "running",`. Without it the test fails (`must_run = null, want true`), which is the intended D1 result.
2. [TODO] Z tools/climap/commands.json: replace the rows "docker run", "docker create", "docker start / docker stop" and add "docker run --rm" (TestCLIMapRealProvider fails on the removed must_run until then):
```
    {"cli": "docker run", "group": "docker", "map": [{"kind": "resource", "type": "docker_container", "attrs": ["image", "name", "command", "env", "ports", "mounts", "restart", "state"]}], "note": "A long-running `docker run -d` is a docker_container (`state = \"running\"`, the default). A one-shot run kept for inspection is a docker_container with `state = \"exited\"`; run-and-remove is the docker_container_run action (`docker run --rm`)."},
    {"cli": "docker run --rm", "group": "docker", "map": [{"kind": "action", "type": "docker_container_run", "attrs": ["image", "command", "env", "remove", "timeout", "expected_exit_codes", "result_path"]}], "note": "Creates, starts, waits for the exit, captures exit_code / stdout / stderr, then removes the container. For a one-shot container kept in state use docker_container with `state = \"exited\"`."},
    {"cli": "docker create", "group": "docker", "map": [{"kind": "resource", "type": "docker_container", "attrs": ["state"]}], "note": "`state = \"created\"` creates the container and never starts it."},
    {"cli": "docker start / docker stop", "group": "docker", "map": [{"kind": "resource", "type": "docker_container", "attrs": ["state"]}, {"kind": "action", "type": "docker_container_start", "attrs": ["container_id"]}, {"kind": "action", "type": "docker_container_stop", "attrs": ["container_id", "signal", "timeout"]}], "note": "`state` is the desired state (`running` starts a stopped container in place; `created` never starts it); the actions start or stop a container once without changing configuration."},
```
3. [TODO] Z tools/parity/paritydata/paritydata.go: add after docker_container_restart `{Action, "docker_container_run", "Pass 5.3: Run a one-shot container to completion and remove it (docker run --rm), capturing exit code, stdout and stderr"},` and change the docker_container_start note to `"Pass 5: Start a stopped container (docker_container with state = \"created\" never starts it)"`.
4. [TODO] Z regenerate: `python -I -B reference/_gen/generators/pass5/renames_post.py` (renames.json), `go run ./tools/parity -guide templates/guides/migrating-from-kreuzwerker.md.tmpl` (TestGuideTemplateCurrent), `Invoke-Build Inventory, RegistryGen, ContractGen` (new item engine:container-run), `Invoke-Build FieldMap`, GenSkill, Docs (new guide container-state).
5. [TODO] Z CHANGELOG (Breaking): docker_container `must_run` removed, replaced by `state` (state upgrade v0 -> v1 automatic; `must_run = false` has no exact counterpart, see the guide); `wait_timeout` default is 5m with state = "exited"; `stop_timeout` is no longer reported when unset; docker_network `scope` is now an Optional argument (Forces replacement).
6. [TODO] 0 fieldcov name mismatches and the Availability pointer (log lines above).
