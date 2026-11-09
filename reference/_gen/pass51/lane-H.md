# Pass 5.1 Lane H: harness coverage

## Log

[P5.1-H] [ASSUMED] The brief assumed less infrastructure than already existed. test/harness/docker-compose.yml already had `registry` (registry:2 at 127.0.0.1:12380 / manager:12380, deletes on), `mirror` (Docker Hub pull-through cache), manager, worker1-3 and tlsnode. Three things were actually missing. (a) Nothing was ever seeded into registry:2, so every test pulled from Docker Hub through the mirror. (b) `Invoke-E2E -SwarmInit` only ran `swarm init`. All workers stayed outside the swarm, because the docker_swarm_node / E2E tests join worker1-3 themselves and need them inactive, so the swarm pass never had a ready worker. (c) The harness never exported TF_ACC_PLUGIN, TF_ACC_PLUGIN_DIR or TF_ACC_CSI_PLUGIN, although the skip messages said it did.
[P5.1-H] [BUILD] Registry seeding: test/harness/seed-images.txt lists busybox:1.36, alpine:3.20, nginx:alpine, nginx:1.27-alpine and docker/dockerfile:1. harness-up.ps1 and harness-up.sh pull each on the manager through the mirror, push it as manager:12380/<ref>, remove both local tags (so the manager starts empty) and export TF_ACC_SEEDED_IMAGES.
[P5.1-H] [BUILD] Tests point at the seeded copies through internal/provider/h_harness_images_test.go. `hAccTest` is resource.Test plus a rewrite of every step's Config; `hAccImage`, `hAccRepo`, `hAccHostImage` and `hAccConfig` cover Go-side refs (Dockerfiles, checks). The rewrite is busybox:1.36 -> manager:12380/busybox:1.36, a name that works on the manager and on every worker. Without the harness the Hub ref is returned unchanged. All 109 `resource.Test` calls in TestAcc* functions of the H-owned files now go through hAccTest; Dockerfile and check literals were mapped in compose_acc_c, e2e_acc_p3, image_acc_p3, image_build_buildkit, image_data_source, image_resource, img_acc, reg_registry_acc, registry_image_resource and tag_resource.
[P5.1-H] [BUILD] Unit test TestHAccImageRewrite covers the rewrite rules (qualified refs untouched, longest match first, no partial tag matches).
[P5.1-H] [BUILD] Standby worker: worker4 was added to docker-compose.yml (127.0.0.1:12381, DOCKER_WORKER4_HOST). New scripts/harness-swarm-init.ps1 and .sh run swarm init, join worker4 as a worker, wait for Ready/Active, then create and enable the tfacc-csi plugin on the manager. Invoke-E2E.ps1 -SwarmInit calls the .ps1 script. worker1-3 stay unjoined for the tests that join them.
[P5.1-H] [BUILD] Plugin fixture: test/harness/tfaccplugin/main.go is a test-only static Go binary built with stdlib plus grpc/protobuf, which are already in go.mod; go.mod and go.sum are unchanged. It has two modes. `volume` is a no-op docker.volumedriver/1.0 with a settable DEBUG env. `csi` is a controller-only CSI driver (docker.csicontroller/1.0 + csinode/1.0) that answers the csi.v1 calls swarmkit makes for create, drain and delete, using hand-encoded protobuf via protowire, so the module needs no CSI spec dependency. harness-up builds it with GOOS=linux CGO_ENABLED=0 into test/harness/.plugins/{volume,csi} (config.json + rootfs/, git-ignored, removed by harness-down). It pushes the volume plugin as 127.0.0.1:12380/tfacc/noop-volume:1 and exports TF_ACC_PLUGIN, TF_ACC_PLUGIN_DIR, TF_ACC_CSI_PLUGIN=tfacc-csi and TF_ACC_CSI_PLUGIN_DIR.
[P5.1-H] [ASSUMED] Plugin choice: a self-built static binary rather than a published plugin. It needs no kernel module, no FUSE and no host mount, so it runs inside dind; vieux/sshfs needs /dev/fuse and CAP_SYS_ADMIN on the host kernel. It also comes from source, so neither Docker Hub nor a plugin registry account is needed. CSI is therefore not skipped: the same binary provides a CSI controller that dind can run.
[P5.1-H] [BUILD] TestAccPlugin_basic installs TF_ACC_PLUGIN (a reference) instead of the hard-coded vieux/sshfs. The value "1" still means vieux/sshfs:latest. testAccPluginPreCheck now returns the reference.
[P5.1-H] [BUILD] TestAccObjClusterVolume now calls swarmAccManagerAddr, which it lacked (it is in $SwarmAccRegex), and skips with a written reason when TF_ACC_CSI_PLUGIN is not enabled on DOCKER_HOST.
[P5.1-H] [BUILD] Docker Hub search: harness-up runs `docker search` once from the manager. It exports TPD_ACC_INTERNET=1 when that answers, else TF_ACC_SKIP_INTERNET=1. TestAccImageSearch therefore runs online, anonymously with no credentials, because daemon search goes straight to index.docker.io and registry:2 has no search API. Offline it skips with its existing written reason.
[P5.1-H] [BUILD] TestAccRegistryImageDataSource now reads the harness copy via the host-side name 127.0.0.1:12380/alpine:3.20, with Docker Hub as the fallback. TestAccRegistryTokenEphemeral and TestAccP3RegistryImageDataSources stay on Hub, anonymously, because they test Hub's token service and a Hub multi-arch index; both skip with TF_ACC_SKIP_INTERNET.
[P5.1-H] [BUILD] Plain pass: Invoke-E2E.ps1 gained -Skip, which becomes go test -skip. Invoke-Build TestAcc passes $SwarmAccRegex as -Skip of the plain pass and as -Run of the swarm pass, so the 33 swarm tests run once instead of skipping in the plain pass. I chose this over a written skip reason. A script check confirmed that every TestAcc that calls swarmAccManagerAddr is matched by the regex and that nothing else is.
[P5.1-H] [FIXED] test/harness/.env values are now double-quoted in both writers. Before, TF_ACC_COMPOSE_CLI=C:/Program Files/... broke `source .env` in bash, and `set -e` scripts such as harness-swarm-init.sh. Invoke-E2E's .env reader already strips the quotes.
[P5.1-H] [BUILD] harness-down.{ps1,sh} remove .plugins/ and clear the new variables; harness-down.ps1 also now clears TF_ACC_COMPOSE_CLI. README.md documents worker4, seeding, the plugin fixtures, the Hub-only tests, -Skip, the new variables and ports 12375-12381.
[P5.1-H] [FIXED] TestAccStackP3_aliasAndVolumes failed once worker4 existed. Its task's volume can now be created on worker4, but the checks look on the manager. The web service is now pinned to `node.role==manager`; this was a test change only.
[P5.1-H] [FIXED] TestAccImageContractOutputs expected "docker.io/library/"+image. For the registry copy the normalized ref is manager:12380/busybox:1.36, so the expectation now follows the image. Re-verified via Invoke-E2E: PASS.
[P5.1-H] [TEST] Targeted harness runs: 22 image/registry/plugin/compose/E2E plain tests PASS, including TestAccPlugin_basic, TestAccObjPluginCreatePush and TestAccImageSearch. 12 swarm tests PASS, including TestAccActionNodePromoteDemote_basic and TestAccObjClusterVolume. The manager image store held only manager:12380/* images afterwards, which confirms that pulls came from the local registry.
[P5.1-H] [VERIFIED] Full `Invoke-Build TestAcc` run. Plain pass: 72 pass / 2 fail / 0 skip. The two failures are TestAccComposeParity (see the TODO below) and TestAccImageContractOutputs (fixed above, re-verified). Swarm pass: 34 pass / 0 fail / 0 skip. Logs are in test/harness/last-acc.log and last-acc-swarm.log.
[P5.1-H] [VERIFIED] `Invoke-Build Build, Vet, Test` succeeded. go.mod and go.sum are unchanged. The ownership check passes.
[P5.1-H] [TODO] R: internal/provider/obj_plugin_actions.go (docker_plugin_create context tar). On a Windows host, tar.FileInfoHeader gives regular files mode 0666, so a plugin created from Windows has a non-executable entrypoint and cannot be enabled. Exact change: after `hdr, err := tar.FileInfoHeader(info, link)`, add `if runtime.GOOS == "windows" && info.Mode().IsRegular() { hdr.Mode |= 0o111 }`. This is the docker CLI's chmodTarEntry behaviour. Found by code reading and not reproduced live: TestAccObjPluginCreatePush only creates and pushes.
[P5.1-H] [TODO] P / compose owner: TestAccComposeParity fails in the full run on genuine provider-vs-CLI diffs, not on anything in the harness. c2s_webapp and c2s_webapp_debug: the order of the com.docker.compose.depends_on label differs (CLI "db:...,cache:..." in declaration order, provider sorted). unit_expand: the config-hash differs, volumes_from is "<proj-web-1>" in the CLI vs "proj-web-1:ro" in the provider, and the CLI passes a bare "UNSET" env entry that the provider drops. All are provider code in compose_*.go, so I left them alone.
[P5.1-H] [TODO] Z: CLAUDE.md "bash" section references `make` targets, but there is no Makefile in the repo.

## Lane P harness requests

1. Preload the parity images: not done, by design. Seeding leaves the manager's image store empty, because other plain-pass tests (prune, image lists) expect a clean manager. The Hub mirror's cache volume persists, so the parity pulls are already local after the first run. Lane P's end note says that is fine.
2. No harness change needed: confirmed. harness-up never prunes or touches /srv on the manager after the seeding step, which runs before any test.
3. IPv6 on the manager: no change. Lane P confirmed kitchen_sink works as is, and it passed in the full run.

## Summary

Before (Pass 5 final logs):
- Plain: 71 pass / 0 fail / 37 skip, plus 10 ComposeParity sub-case skips that belong to Lane P.
- Swarm: 32 pass / 0 fail / 2 skip.

The 37 plain skips:
- 33 swarm tests skipped with "DOCKER_HOST is not a swarm manager": TestAccActionSwarmRotateTokens_basic, ActionNodeDrainActivate_basic, ActionServiceScaleForceUpdateRollback_basic, StackDataSources, ComposeSwarm, P3SwarmListDataSources, SwarmDataSources, DockerConfigBasic, DockerConfigWriteOnly, SwarmTokensEphemeral, ListSecretQuery, SecretBasic, SecretWriteOnly, ServiceP3_globalMode, ServiceP3_jobModes, ServiceP3_forceUpdate, ServiceP3_laneCAttributes, ServiceP3_fileNameDefault, DockerConfigP3_importFillsData, ServiceBasic, StackP3_aliasAndVolumes, StackP3_updateResubmits, StackBasic, NodeResource_basic, ActionNodePromoteDemote_basic, ActionSwarmRotateCA_basic, ActionSwarmUnlockKey_basic, SwarmNodeP3_importThenApply, SwarmNodeP3_adoptJoined, SwarmNodeP3_gpuPlacement, SwarmNode_basic, SwarmNode_twoWorkersPromote, SwarmTokens_basic.
- TestAccImageSearch: TPD_ACC_INTERNET not set.
- TestAccObjPluginCreatePush: TF_ACC_PLUGIN_DIR not set.
- TestAccObjClusterVolume: TF_ACC_CSI_PLUGIN not set.
- TestAccPlugin_basic: TF_ACC_PLUGIN not set.

The 2 swarm skips: TestAccObjClusterVolume (TF_ACC_CSI_PLUGIN not set) and TestAccActionNodePromoteDemote_basic ("needs a ready worker node").

After (full Invoke-Build TestAcc):
- Plain: 72 pass / 2 fail / 0 skip. The swarm tests are excluded with -skip and run in the swarm pass.
- Swarm: 34 pass / 0 fail / 0 skip.
- Every environment skip is gone. The fixed ImageContractOutputs failure was re-verified to pass.

Skips that remain only when the conditions are missing, each with a written reason in the test:
- TestAccImageSearch, TestAccRegistryTokenEphemeral and TestAccP3RegistryImageDataSources skip when the manager cannot reach Docker Hub (TF_ACC_SKIP_INTERNET). They test Hub itself, and search bypasses mirrors.
- TestAccObjClusterVolume skips if tfacc-csi could not be enabled.
- TestAccPlugin_basic and TestAccObjPluginCreatePush skip if Go is missing when harness-up runs.
