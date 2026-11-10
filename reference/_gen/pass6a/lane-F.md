# Pass 6a Lane F

Configure preflight diagnostics, and the dind harness moved to Engine 29.9.3.

## Log

[P6a-F] [VERIFIED] Configure already contacted the daemon before this pass: one `GET /_ping` with API negotiation (5s timeout, plus `ssh.connect_timeout` per hop). Any failure was a single generic warning ("Docker daemon not reachable"), and a negotiated version below min_api_version was an error. The ping is kept. Unreachable daemons still give a warning, never an error, so validate, plan and offline use keep working.
[P6a-F] [BUILD] internal/provider/preflight.go: `classifyDaemonError` is the one classification helper. It sorts a failure into not reachable, permission denied, TLS failure or bad response. It checks typed errors first (fs.ErrPermission, EACCES, EPERM, Windows ERROR_ACCESS_DENIED, x509.UnknownAuthorityError, HostnameError, CertificateInvalidError, tls.CertificateVerificationError, RecordHeaderError, AlertError), then the texts produced by the moby client and crypto/tls. The text fallback is needed because moby v0.6.2 drops the error chain on permission denied. `daemonCheckDiagnostic` produces a distinct summary and detail for each class.
[P6a-F] [BUILD] endpointConfig gained HostSource and TLSSource, set by resolveEndpoint. HostSource is the `host` argument, the `ssh` block, Docker context "x" (named by the `context` argument, DOCKER_CONTEXT, or `currentContext` in <dir>/config.json), DOCKER_HOST, or the built-in default. TLSSource is the tls block, `cert_path` <dir>, Docker context, or DOCKER_TLS_VERIFY + DOCKER_CERT_PATH <dir>. The TLS diagnostic lists the settings in effect without secrets: source, CA path (or inline PEM, or system roots), client certificate, skip-verify. It also gives a cause hint: plain HTTP to a TLS daemon, TLS to a plain daemon, unknown CA, hostname mismatch, expired certificate, or a client certificate that is missing or rejected.
[P6a-F] [BUILD] API floor (d): the "Docker daemon too old" error on min_api_version now names the negotiated version, the daemon's reported version, the host and its source, and min_api_version. A daemon below client.MinAPIVersion (1.40) used to fall into the generic "not reachable" warning. It now gets the same error with both versions and the client minimum.
[P6a-F] [BUILD] Unknown values: Configure skips the ping, logging at info level, when a `tls` or `ssh` value or disable_docker_daemon_check is unknown, because the ping would test a different endpoint. host, api_version, context and cert_path were already errors when unknown (unchanged).
[P6a-F] [BUILD] Documented in the provider MarkdownDescription (new "Daemon check" paragraph after the existing text; the first sentence is unchanged) and in the disable_docker_daemon_check description.
[P6a-F] [TEST] Unit tests in internal/provider/preflight_test.go:
- TestClassifyDaemonError: 13 cases.
- TestResolveEndpointHostSource.
- TestPreflightUnreachable: closed port; checked directly and through Configure.
- TestPreflightPermissionDenied: wrapped EACCES and EPERM, going through a real moby client whose dialer fails.
- TestPreflightTLS: httptest TLS server with an untrusted CA; plain HTTP to the TLS server; server requiring a client certificate with none configured.
- TestPreflightAPIFloor: fake /_ping + /version at 1.41 against floor 1.44, and at 1.30, below the client minimum.
- TestPreflightSkipsUnknown.
internal/provider/preflight_windows_test.go (windows build tag) adds TestPreflightPipeAccessDenied: an npipe dial failing with ERROR_ACCESS_DENIED. All pass. Non-Windows paths compile on every OS: syscall.EACCES and EPERM exist on Windows, and errno 5 is matched only when runtime.GOOS is windows.
[P6a-F] [VERIFIED] Engine before: docker:28-dind, Engine 28.5.2 (`dockerd --version`, build 89c5e8f), API 1.51, overlay2. After: docker:29.9.3-dind, the newest dind tag on Docker Hub (2026-11-06). Confirmed from the daemon itself: `docker version` reports 29.9.3, API 1.56, min API 1.40. harness-up prints "manager: Engine 29.9.3 API 1.56, storage overlay2" and "cdnode: Engine 29.9.3 API 1.56, storage overlayfs driver-type=io.containerd.snapshotter.v1".
[P6a-F] [VERIFIED] Engine 29 puts a fresh /var/lib/docker on the containerd store by default (probe with no flags: Driver overlayfs, driver-type io.containerd.snapshotter.v1). Both `--storage-driver=overlay2` and `--feature containerd-snapshotter=false` give the overlay2 graph driver.
[P6a-F] [BUILD] test/harness/docker-compose.yml:
- Every node is on docker:29.9.3-dind.
- manager, worker1-4 and tlsnode explicitly use overlay2 (`--storage-driver=overlay2 --feature=containerd-snapshotter=false`).
- New standalone node `cdnode` on the containerd store (`--feature=containerd-snapshotter=true`), at 127.0.0.1:12383, with volume cdnode-data. It is not in the swarm.
[P6a-F] [BUILD] harness-up.ps1 and harness-up.sh wait for cdnode and print the Engine version, API and storage driver of the manager and cdnode. They warn if the manager is on containerd. They export TF_ACC_CONTAINERD_HOST only when cdnode really is on the containerd store. harness-down.ps1 clears the variable. test/harness/README.md documents the cdnode row, a new "Engine version and image stores" section, the env table row, and ports 12375-12383. seed-images.txt needed no change: cdnode pulls manager:12380/<ref> through --insecure-registry.
[P6a-F] [TEST] New acceptance tests in internal/provider/harness_p6a_f_acc_test.go. Each skips cleanly without TF_ACC or DOCKER_HOST, and the containerd tests also skip without TF_ACC_CONTAINERD_HOST or when that daemon is not on the containerd store.
- TestAccImageP6a_loadMultiPlatform: generates a two-platform OCI tar (linux/amd64 and linux/arm64, no emulation needed) and loads it with the docker_image_load action on cdnode. Reads back both platform manifests, both Available.
- TestAccServiceP6a_swapResources: swarm pass. Sets resources.swap_bytes and memory_swappiness, creates then updates, and reads SwapBytes and MemorySwappiness back from ServiceInspect.
- TestAccSwarmInitP6a_taskDefaults: plain pass, on cdnode. Runs swarm init with TaskDefaults.LogDriver, then swarm update, inspecting after each, then leaves the swarm.
- TestAccContainerP6a_umask: container umask (API 1.56), read back from HostConfig.Umask. This feature had no acceptance test before.
[P6a-F] [BUILD] TestAccImageAttestations now picks its path from the daemon's capability, not only TPD_ACC_API_155. It uses cdnode when TF_ACC_CONTAINERD_HOST is set. Below API 1.55 it runs the gate-error step. At 1.55 or higher on a graph driver it skips with the reason. On API 1.55+ with the containerd store it runs the real path. TestAccBuildxP53_multiPlatform runs on cdnode when TF_ACC_CONTAINERD_HOST is set, so the two-platform build runs for real.
[P6a-F] [BUILD] reference/_gen/deferred.d/acceptance.json: removed TestAccImageAttestations, TestAccBuildxP53_multiPlatform and TestServiceP53APIGate, because those features now run for real (the third is covered by TestAccServiceP6a_swapResources). TestAccNpipeSmoke remains. No new rows: everything targeted now runs on the harness. `go run ./tools/deferred` reports no validation error, acceptance 1.
[P6a-F] [TEST] Plain pass (F-plain1.log), `-Run 'TestAccNetwork|TestAccVolume|TestAccContainer|TestAccImage|TestAccBuildx|TestAccSwarmInitP6a'`: 39 pass, 0 fail, 0 skip.
[P6a-F] [TEST] Swarm pass (F-swarm1.log), `-SwarmInit -Run 'TestAccService|TestAccSwarm'`: 19 pass, 0 fail, 4 skip. The four skips are the docker_swarm_init tests that need a manager outside any swarm; they are expected in this pass.
[P6a-F] [VERIFIED] TestAccServiceP53_daemonDropsMountFields passes on 29.9.3. The service spec still drops BindOptions.CreateMountpoint, ReadOnlyNonRecursive, ReadOnlyForceRecursive and Mount.Consistency, and image mounts in services are still rejected. The fieldmap.d/S.json skips stay correct. Only the test comment changed, to record the re-run.
[P6a-F] [VERIFIED] SwarmSpec.TaskDefaults.LogDriver on 29.9.3: swarm init with {json-file, max-size=1m} reads back nil, and swarm update with {local, max-size=2m} reads back nil. The Engine still ignores it and the skip is correct. TestAccSwarmInitP6a_taskDefaults pins that it is dropped and fails, naming the field to implement, if the Engine starts keeping it.
[P6a-F] [VERIFIED] `Invoke-Build ., FmtCheck`: Build, Vet and Test pass. GenSkillCheck fails with "genskill: drift: reference/provider.md differs". That is expected: the provider and disable_docker_daemon_check descriptions changed, and the generated outputs belong to Lane Z. The rest of the default task plus FmtCheck (InventoryCheck, InventoryValidate, RegistryCheck, ContractCheck, DocCountsCheck, FieldCoverage, FmtCheck): 8 tasks, 0 errors.
[P6a-F] [ASSUMED] Configure keeps permission-denied and TLS failures as warnings, not errors. Configure also runs during plan, and the existing contract says an unreachable daemon never fails validate or plan. An error would break offline plans for the same misconfiguration that an unreachable host only warns about.
[P6a-F] [TODO] Lane Z: run `Invoke-Build GenSkill` and `Invoke-Build Docs`, and stage .claude/skills/terraform-provider-docker/reference/provider.md, README_resources.md if it changes, and docs/index.md. Reason: the provider MarkdownDescription gained the "Daemon check" paragraph and disable_docker_daemon_check was reworded (internal/provider/provider.go).
[P6a-F] [TODO] Lane Z: in reference/_gen/fieldmap.d/S.json, update the four TaskDefaults skip reasons (SwarmInit/Spec/TaskDefaults/LogDriver/{Name,Options}, SwarmSpec/TaskDefaults/LogDriver/{Name,Options}). Append: "Re-verified on Engine 29.9.3 (API 1.56) by TestAccSwarmInitP6a_taskDefaults: nil after init and after update." Replace the stale "the harness run of TestAccSwarmInitP53_spec" pointer with that test name.
[P6a-F] [TODO] Lane Z: optionally add to the Consistency and BindOptions skip reasons in fieldmap.d/S.json: "Re-verified on Engine 29.9.3 (API 1.56): TestAccServiceP53_daemonDropsMountFields passes."
[P6a-F] [TODO] Lane Z: CLAUDE.md "Repo map" test/harness line should list the new node: "the dind swarm harness (Engine 29.9.3: manager, worker1-4, TLS node, containerd-store node cdnode, registry:2, Docker Hub mirror)". .github/workflows/ci.yml needs no change: the harness picks its image itself.
[P6a-F] [TODO] Lane Z: CHANGELOG entries:
- Configure preflight now classifies daemon failures: not reachable (with the host source), permission denied, TLS (with the settings in effect), and too old (with both versions).
- Acceptance harness on Engine 29.9.3 / API 1.56, with a containerd-store node (TF_ACC_CONTAINERD_HOST).
$SwarmAccRegex needs no change: TestAccServiceP6a_swapResources matches `Service`. TestAccSwarmInitP6a_taskDefaults deliberately does not match, so it runs in the plain pass on cdnode.

## Findings

### 1. Skips that are wrong on the new Engine

None. Every skip checked still holds on Engine 29.9.3 (API 1.56):
- SwarmSpec.TaskDefaults.LogDriver: dropped on init and on update. Request: init with {json-file, max-size=1m}, read back nil; update with {local, max-size=2m}, read back nil.
- Service Mount BindOptions.CreateMountpoint, ReadOnlyNonRecursive, ReadOnlyForceRecursive and Mount.Consistency: dropped.
- Service image mounts: rejected.

### 2. Breakages on the new Engine, fixed

None found in the targeted runs: plain 39/39 and swarm 19 pass + 4 expected skips.

Changes the move needed, made in Lane F files:
- Engine 29 defaults to the containerd store, so the overlay2 nodes now opt out explicitly. Without that, the whole swarm would have silently moved to the containerd store and the graph-driver coverage would be gone.
- TestAccImageAttestations gated only on TPD_ACC_API_155. On an API 1.56 manager without the containerd store it would have run the real path and failed. It now detects API version and image store.

## Summary

Done:
- Configure preflight with classified diagnostics (unreachable, permission, TLS, API floor), one helper, and 8 unit tests.
- Harness on docker:29.9.3-dind (Engine 29.9.3, API 1.56; was Engine 28.5.2, API 1.51), plus the containerd-store node cdnode (TF_ACC_CONTAINERD_HOST).

Tests now running for real: TestAccImageAttestations, TestAccBuildxP53_multiPlatform, and the new TestAccImageP6a_loadMultiPlatform, TestAccServiceP6a_swapResources, TestAccSwarmInitP6a_taskDefaults and TestAccContainerP6a_umask.

Wrong skips found: none. TODOs for Lane Z: 5.
