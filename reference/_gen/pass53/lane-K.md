# Pass 5.3 Lane K: docker_compose field coverage, hooks, owner label, D5/D6

Upstream sources:
- docker/compose v5.1.4: a shallow clone at HEAD 4732a2ed20a49c43a7b44bb4928259162c3ae3be (it matches reference/SOURCES.md), checked out in the session scratchpad. Line numbers below are from that clone.
- compose-spec/compose-go v2.16.2: from the Go module cache (the version in go.mod).
- docker/cli v29.9.3+incompatible: from the Go module cache (`go list -m -f '{{.Dir}}' github.com/docker/cli`).
- moby/moby/api v1.56.1 and moby/moby/client v0.6.2: from the module cache.
- Engine API: reference/upstream/engine-api/api/swagger.yaml, docs/CHANGELOG.md.
- compose-spec: reference/upstream/compose-spec/05-services.md, container_spec.md, schema/compose-spec.json.

## Log

[P5.3-K] [BUILD] Local mode stops ignoring these service keys and maps them onto the Engine calls the provider already makes (new internal/provider/compose_local_k.go, edits in compose_local.go):
- blkio_config goes to HostConfig.Blkio*, as compose does (pkg/compose/create.go:761-797 setBlkio).
- links and external_links go to HostConfig.Links and to EndpointSettings.Links on every endpoint (convergence.go:779-833 getLinks; create.go:441-454).
- mac_address, at service level and per network, goes to the endpoint's MacAddress. The service-level value goes to the primary network, and a conflict with that network's own value is an error, as in compose (create.go:340-368 prepareContainerMACAddress, :550-555).
- umask goes to HostConfig.Umask. Engine API 1.56 added it (engine-api CHANGELOG v1.56), so composeCheckAPI fails on an older daemon.
- credential_spec file and registry go to SecurityOpt "credentialspec=file://|registry://". This is the Windows daemon form; compose v5.1.4 does not send it in local mode. credential_spec config is an error in local mode, because only a Swarm task can mount a Swarm config.
- use_api_socket follows compose's project transformation (apiSocket.go:31-94): a bind mount of /var/run/docker.sock, DOCKER_CONFIG=/run/secrets/docker unless the service sets it, and a config.json holding the provider's registry_auth credentials, copied in at create time. The transformation is applied before the config-hash is computed, so the hash matches the CLI's.
- volumes[].volume.labels go to Mount.VolumeOptions.Labels (create.go:1285-1295 buildVolumeOptions).
[P5.3-K] [BUILD] pull_policy daily, weekly, every_<d>, and refresh with pull_refresh_after now work the way compose v5.1.4 mustPull does (pull.go:350-378): pull when the image is missing or `Metadata.LastTagTime + period` is in the past. The CLI reads the same field (images.go:157). No state is needed: the daemon records the last pull time. A kept container whose image ID changes after such a pull is recreated (convergence.go:337 imageUpdated). Refresh reports "pull_policy <p> is due" as drift, so the next plan shows an update. pull_refresh_after reuses compose-go's every_<d> duration parser (types.go:303-324 GetPullPolicy), so no new module is needed.
[P5.3-K] [BUILD] Lifecycle hooks are implemented in the new compose_hooks.go:
- post_start is exec'd after every start, including restarts of kept containers, as in compose (convergence.go:905-931; hook.go:34-85 runHook).
- pre_stop is exec'd before a container is removed on recreate and on destroy (down.go:298-311). Destroy re-parses the configuration in state to find the hooks. As in compose, "not found" and "conflict" errors are ignored.
- pre_start steps run as init containers. compose-go resolves each step's inheritance at load time (loader/normalize.go:156-197 resolvePreStartHooks). The provider creates each step from its own container spec, with VolumesFrom set to the service container, after that container is created and before it starts. The step container is labelled oneoff and removed afterwards. A non-zero exit fails the service and the error includes the last log lines.
- per_replica steps run for every replica. The other steps run once, before the first replica created in that apply, and only when no replica of the service is kept. This matches compose-spec 05-services.md "pre_start", Compose v5.3.0. compose v5.1.4 predates pre_start, so there is no CLI to compare with.
[P5.3-K] [BUILD] Build: additional_contexts go to the seam's Contexts. A "service:<x>" context becomes docker-image://<x's image>, and composeBuild builds x first when its image is missing. entitlements are passed through, and privileged adds security.insecure. This follows compose's bake path (build_bake.go:175-181, :223, :474-483).
[P5.3-K] [BUILD] Swarm mode (compose_swarm.go, the new compose_swarm_k.go):
- A secret's driver and driver_opts go to SecretSpec.Driver, and when a driver is set no data is sent. template_driver goes to SecretSpec.Templating and ConfigSpec.Templating. This matches docker/cli cli/compose/convert/compose.go:105-160. compose-go still requires file or environment on a driver secret (loader/validate.go:242-248).
- credential_spec goes to Privileges.CredentialSpec. A config credential spec also gets a Runtime ConfigReference, and composeSwarmResolve swaps the config name for its ID (docker/cli service.go:288-345, :680-715). composeLoad keeps a config that only credential_spec.config references, because compose-go's pruning drops it.
- blkio_config, umask, use_api_socket and mac_address are now listed in the swarm plan warning. ContainerSpec has none of these fields.
- The secret and config hash form is unchanged unless driver or templating is set.
[P5.3-K] [BUILD] docker_compose_publish (action_compose_images.go, the new compose_publish_k.go):
- Every env_file path in the published compose files is replaced by "<sha256(abs path)>.env", as compose does (publish.go:230-260 processFile; transform/replace.go:65-106). This is a change: before, the paths were published as written. The provider replaces only the scalar token, while compose replaces to the end of the line, so trailing comments and flow lists are kept.
- `with_env` adds the env files as application/vnd.docker.compose.envfile layers (publish.go:626-638; internal/oci/push.go:61, :87-98).
- `resolve_image_digests` adds an image-digests.yaml layer built with compose-go's Project.WithImagesResolved and the Engine API distribution inspect (publish.go:265-280; pull.go:259-274). A generic helper infers the digest type, so github.com/opencontainers/go-digest stays an indirect dependency.
- `oci_version` takes "1.0" or "1.1". When it is unset, the action tries 1.1 and falls back to 1.0 on a 4xx other than 401, 403 or 407 (push.go:99-126, :147-205).
[P5.3-K] [BUILD] D5 (c_compose_refs.go composeLocalConfigHashCurrent; compose_local_apply.go apply and drift; compose_resource.go spec_hash):
- Every container that carries the spec-hash label is now recreated when its com.docker.compose.config-hash label differs from the planned value, which is compose mustRecreate (convergence.go:326-338). Containers with references are compared against the resolved hash.
- spec_hash includes the config-hash (`obj.CLI`, omitempty) only for services that set something the per-object hash cannot see: an env entry with no value, hooks, env_file, label_file, extends, develop, attach, models or provider. Such a change therefore plans an update. For every other project spec_hash is byte-for-byte unchanged.
- TestComposeHashCompat passes with its goldens unchanged. Both rules hold, so no digests are reported.
[P5.3-K] [BUILD] D6: a swarm-mode environment entry with no value is now sent as the bare "KEY", exactly as `docker stack deploy` does. The source is docker/cli v29.9.3 cli/compose/convert/service.go:614-626 convertEnvironment (`case nil: output = append(output, name)`), called from :136.
- The swarm service hash keeps the frozen KEY=VALUE list, so the TestComposeHashCompat swarm goldens are unchanged.
- composeSwarmServiceDrift compares the live ContainerSpec.Env with the desired list ("has a different environment").
- spec_hash gets a digest of the environment that is sent (composeSwarmService.CLIOnly), so adding or removing a `KEY: null` plans an update.
[P5.3-K] [BUILD] Owner label (the new compose_owner.go):
- Local mode: containers (composeLocalWire), networks and volumes carry the label. The conflict check runs before creating a container, network or volume whose name exists with another owner.
- Swarm mode: services (create and update), networks, secrets and configs carry the label.
- The resource has `adopt`. Read, Create and Update record the project's owner label in private state (composeOwnerRecord). ModifyPlan calls ownerPlanCheck. Create also refuses a project whose existing objects carry another owner.
- The label goes only into the create request, never into the compose service. com.docker.compose.config-hash and the spec hash therefore do not change, which TestComposeFakeHooksOwnerD5K checks.
- docker_compose_run and pre_start step containers are inject-only: they get the label but no conflict check, because one-off containers are skipped. TestComposeFakeRunOwnerK covers this. This follows the coordinator's update.
[P5.3-K] [VERIFIED] Models stay PARTIAL. The Engine API v1.56 swagger has no model endpoint, and compose v5.1.4 drives models through the docker-model CLI plugin (pkg/compose/model.go:82 GetPlugin, :118, :161-190, :197, exec.CommandContext).
[P5.3-K] [VERIFIED] compose:cli-publish is narrowed to `--app`. That option copies the service images and pushes an image index (publish.go:109-150; internal/oci/resolver.go:93-123 oci.Copy), which needs a registry-to-registry copy client the provider lacks.
[P5.3-K] [FIXED] TestComposeRewriteEnvFilesK asserted that "a.env" was absent from the output, and that string can occur at the end of a sha256 hash, so the test failed intermittently. It now checks for " a.env".
[P5.3-K] [TEST] New unit tests, all passing: TestComposeLocalFieldsK, TestComposeLocalFieldErrorsK, TestComposeFakeHooksOwnerD5K, TestComposeFakeOwnerK, TestComposeFakeRunOwnerK, TestComposeFakePullRefreshK, TestComposeFakeHookFailuresK, TestComposeBuildRequestK, TestComposeSwarmFieldsK, TestComposeRewriteEnvFilesK, TestComposePublishWithEnvK, TestComposePublishOCIVersionK.
- TestComposeFakeHooksOwnerD5K checks the event order (create, step create/start/remove, start, post_start exec), the owner label on the container, network and volume, D5 recreation with pre_stop first, a no-op re-plan, and pre_stop on destroy.
- The compose fake Engine (compose_fake_test.go) now serves exec (hijacked), container wait, logs, image LastTagTime and image IDs, and records an event list.
[P5.3-K] [VERIFIED] `Invoke-Build Build, Vet, Test, FmtCheck`: 4 tasks, 0 errors. `Invoke-Build FieldCoverage`: lane K has 0 gaps and no ERROR lines (compose: 389 total, 312 code, 43 manual, 29 skip, 5 partial, 0 gap). TestComposeHashCompat passes with its goldens unchanged.
[P5.3-K] [ASSUMED] `go mod tidy -diff` shows the pre-existing fsutil and genproto direct/indirect drift, plus go.sum lines for containerd/console, morikuni/aec, tonistiigi/vt100 and x/time. None of these come from Lane K's imports, which are all direct requirements already. go.mod and go.sum are unchanged.
[P5.3-K] [TODO] 0 fieldcov does not look inside map values of compose properties. compose:service/networks is one auto-mapped pointer, so the per-network keys mac_address, gw_priority, interface_name, link_local_ips, driver_opts and priority are never filed. Lane K implemented networks.<n>.mac_address. gw_priority, interface_name and link_local_ips are still not sent in local mode, and nothing reports it.
[P5.3-K] [TODO] 0 fieldcov does not follow type conversions. ctypes.FileReferenceConfig(ref) and ctypes.FileObjectConfig(obj) hide reads of ServiceSecretConfig, ServiceConfigObjConfig, SecretConfig and ConfigObjConfig fields. compose-go's ThrottleDevice and WeightDevice have no yaml tags, so their path, rate and weight never auto-map. Lane K mapped all of these by hand in fieldmap.d/K.json.
[P5.3-K] [TODO] Z: regenerate the docs, the skills, the inventory and fieldmap.json. Changed schemas: the docker_compose `adopt` attribute and description; the docker_compose_publish attributes with_env, resolve_image_digests and oci_version, plus its description. Changed partials: the compose:services, compose:secrets and compose:configs markers are removed; compose:cli-publish and compose:models are narrowed.
[P5.3-K] [TODO] Z (CHANGELOG): see "One-time recreates" below.

[P5.3-K] [TEST] Harness run 1 (Invoke-E2E -Run 'TestAccCompose(LifecycleK|Parity|Local)$|TestAccComposeLocal'). TestAccComposeParity, TestAccComposeLocal: PASS. TestAccComposeLifecycleK: FAIL, because the check read HostConfig.Links, which the daemon empties on a user-defined network (it keeps the legacy links on the endpoint). The test was fixed. Also, ComposeParity's example_shop#01 case skipped: the CLI compose v5.1.4 rejects `pre_start` ("services.db additional properties 'pre_start' not allowed"), so I removed pre_start from examples/resources/docker_compose/resource.tf. post_start and pre_stop stay there; the CLI supports both.
[P5.3-K] [TEST] Harness run 2 (-Run 'TestAccCompose(LifecycleK|Parity)$'). TestAccComposeParity: 13/13 sub-cases PASS, including example_shop#01 with the hooks example, and the one known difference (unit_expand volumes_from :ro). TestAccComposeLifecycleK: failed at step 2 on its own assertion. The daemon stores the bare "GONE" in Config.Env as sent (it unsets it only in the process environment), and the check had expected it to be absent. The assertion was fixed.
[P5.3-K] [TEST] Harness run 3 (-Run 'TestAccComposeLifecycleK$'): PASS. Checked on the daemon: the links on the endpoint, the pre_start step's file in the shared volume, the post_start file, the volume labels, the owner label on the container and the volume, D5 recreating web when `GONE: null` is added, and a no-op re-plan.
[P5.3-K] [TEST] Harness run 4 (-SwarmInit -Run 'TestAccComposeSwarm(EnvK)?$'): TestAccComposeSwarmEnvK PASS (D6: service Env contains the bare "GONE"; the config's Templating is golang; the owner label is on the service and the config). TestAccComposeSwarm PASS.

## Summary

### D items
- **D5: done.** Containers are recreated by com.docker.compose.config-hash, like compose mustRecreate, for every container that carries the spec-hash label. spec_hash carries the config-hash only for services with keys the object hash cannot see. TestComposeHashCompat goldens are unchanged, so no digests are reported.
- **D6: done.** Swarm mode sends a bare "KEY" for a null environment entry (docker/cli v29.9.3 cli/compose/convert/service.go:614-626). Drift compares the environment, and the swarm goldens are unchanged. The unit test is TestComposeSwarmFieldsK; the swarm acceptance test is TestAccComposeSwarmEnvK (see Tests).

### One-time recreates (for the CHANGELOG)
- **Local mode, D5.** Affected: a container created by Pass 5 to 5.2, carrying io.terraform.provider.compose-spec-hash, whose com.docker.compose.config-hash label no longer equals the service's current ServiceHash while its spec hash still matches. This is a service whose config changed after creation only in what the CLI hash sees: a `KEY: null` entry added or removed (the Pass 5.2 residual), or a changed hook, env_file, label_file, extends, develop, attach, models or provider.
  - For projects with any of those keys, spec_hash changes once, so the first plan after upgrading shows an in-place update. The update recreates only the containers whose label differs.
  - Containers without the spec-hash label (created before Pass 5 or by the CLI) are judged as before.
  - Services without those keys see no plan change.
- **Local mode, features that used to be ignored.** Services that set blkio_config, links, external_links, mac_address, umask, credential_spec, use_api_socket or volumes[].volume.labels get a new HostConfig or endpoint, so they are recreated once to apply the setting.
- **Swarm mode, D6.** Services with a `KEY: null` environment entry are updated in place once to add the bare key. Secrets with driver or template_driver, and configs with template_driver, are recreated once, because their hash now includes the driver.
- **Owner label.** Setting or changing `owner` recreates nothing.

### Field gaps (K.json: 117 at start, 0 now)
- Implemented and auto-mapped: 40, including blkio weight, credential_spec, external_links, links, mac_address, umask, use_api_socket, pull_refresh_after, volume labels, the hooks' command, user, privileged, working_dir and environment, the pre_start equivalents, additional_contexts, entitlements and build privileged.
- Implemented with a manual attr mapping: 43. These are blkio device path, rate and weight, secrets and configs uid, gid and mode for services and pre_start, top-level config content, environment, file, labels and template_driver, secret driver, driver_opts, environment, labels and template_driver, and per_replica.
- Skipped: 29.
  - Resolved by compose-go before the provider sees them: env_file format and required, label_file, extends, include, and external.name (deprecated). Each skip cites a compose-go file:line.
  - attach is CLI log streaming.
  - develop.watch belongs to the skipped compose:service-develop item.
  - provider runs an external binary.
  - ports[].app_protocol has no Engine API field, and compose ignores it.
  - build.isolation exists only in the legacy builder.
  - build secrets uid, gid and mode have no client-side setting.
- Partial: 5. These are the model fields and the service models bindings, under PARTIAL(pass5) compose:models.

### PARTIAL markers (pass53_partial_k.go)
- Removed: compose:services, compose:secrets, compose:configs.
- Kept and narrowed:
  - compose:cli-publish: only `--app` is missing.
  - compose:models: Engine API 1.56 has no model runner, and compose uses the docker-model CLI plugin.

### Tests
- Unit tests: all pass, both the new K tests and the existing compose tests, including TestComposeHashCompat, TestComposeParityDiffC and TestComposeFakeLocalRefsC.
- Acceptance (harness), final status:
  - TestAccComposeParity: 13/13 PASS.
  - TestAccComposeLocal: PASS.
  - TestAccComposeLifecycleK: PASS.
  - TestAccComposeSwarmEnvK (-SwarmInit): PASS.
  - TestAccComposeSwarm (-SwarmInit): PASS.

### Owner label wiring
- docker_compose: full treatment.
  - Local mode: labels on containers, networks and volumes; conflict checks; adopt; private-state record; plan check; Create refuses a foreign project.
  - Swarm mode: labels on services (create and update), networks, secrets and configs.
- docker_compose_run: inject-only.
