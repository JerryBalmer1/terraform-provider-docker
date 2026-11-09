# Pass 5.1 Lane R: raw-output fidelity and records

## Log

[P5.1-R] [VERIFIED] moby/client v0.6.2 (module cache) keeps a raw body only for ContainerInspect, NetworkInspect, VolumeInspect, PluginInspect, NodeInspect, ServiceInspect, TaskInspect, SecretInspect, ConfigInspect (`*InspectResult.Raw`, decodeWithRaw) and ImageInspect (`ImageInspectWithRawResponse`); ContainerStats returns the body reader. SwarmInspect, DistributionInspect, RegistryLogin (/auth), Info, ServerVersion, DiskUsage, ContainerDiff, ContainerTop, ContainerStatPath, ImageHistory, ImageSearch, ImageAttestations and every list call keep none. Pass 5 already used every raw body that exists: no missed Raw.
[P5.1-R] [VERIFIED] Enumerated the 110 raw outputs of reference/inventory.json; 51 are Engine API responses (or lists derived from them) and in scope. Left out, not Engine responses: dockerfile / source_dockerfile / the 22 docker_dockerfile block renders, compose_yaml, stack_yaml, logs (4), exec stdout/stderr (4), content_base64, the buildx/bake/metadata outputs (Lane S), registry manifest_json / image_config_json / provenance_json / sbom_json / tags_json (registry bodies, not Engine), projects_json and stacks_json (CLI views).
[P5.1-R] [FIXED] Byte-exact outputs depended on read boundaries: decodeWithRaw captures what json.Decoder read through a TeeReader, so the Engine's trailing "\n" is in Raw or not depending on how the body arrived. New rEngineBody (internal/provider/r_rawjson.go) drops only the whitespace around the JSON value; applied in copInspect, imgInspectRaw, ctrFillContract, swarmInspectInput and the network/volume/plugin resource + data source fills; data.docker_container_stats now uses it instead of copTrimJSON (removed).
[P5.1-R] [BUILD] rCanonicalJSON / rCanonicalList / rSortJSONArray in r_rawjson.go: compact, keys sorted at every level, UseNumber (numbers keep the encoder's text), HTML not escaped, no trailing newline. Used by objJSON, composeMarshal, imgMarshal, swarmListInput, swarmContractInput, regDistributionFill, registry_login auth_json, container changes/top/stat, containers_json, the ctrFillContract fallback and contractCompute's structured-only *_json fallback.
[P5.1-R] [FIXED] Deterministic list order: container changes sorted by Path; system df verbose Items sorted by ID (volumes by Name). The other lists were already sorted (name/ID) or keep a meaningful Engine order (history, search, top rows, attestations).
[P5.1-R] [FIXED] docker_swarm / docker_swarm_init inspect_json (not sensitive) contained JoinTokens.Worker/Manager: swarmContractInput marshalled swarm.Swarm, not its ClusterInfo, contradicting the contract ("JoinTokens are excluded"). Now ClusterInfo only; regression assert in TestRRawReencodedDeterministic/docker_swarm.
[P5.1-R] [TEST] internal/provider/r_rawjson_test.go: TestRRawByteExactDataSources (11 data sources) and TestRRawByteExactResources (volume, network via Read; container, image, tag, swarm resources via their shared fill helpers) serve the fake Engine body with reversed key order, inner spaces and surrounding newlines and require output == body (trimmed) and sha256 of it; TestRRawReencodedDeterministic (24 data sources) and TestRRawReencodedHeaderAndAuth (container_file stat header, registry_login, regDistributionFill) read twice with the Engine answer varied in key order and list order and require identical canonical bytes and hash; TestRCanonicalJSON, TestRContractFallbackCanonical. All pass.
[P5.1-R] [FIXED] cop_container_ops_test.go TestCopContainerChanges: changes[0] is now /etc (sorted by Path).
[P5.1-R] [BUILD] Fragments: re-encoded outputs no longer say "exact": reference/_gen/fragments/outputs/A.json (15 outputs + stat_json), B.json (7), D.json (1). Byte-exact descriptions unchanged. C.json / E.json (compose, stack) descriptions were already accurate; unchanged.
[P5.1-R] [BUILD] CONTRACT.md: "Known exposures" lists every re-encoded output as "Re-encoded, not byte-exact"; new "Raw JSON fidelity" section (the two forms, out-of-scope outputs); new "Known differences from upstream defaults" (docker_compose.remove_orphans: CLI false, ours true, why).
[P5.1-R] [BUILD] compose_resource.go: resource description and remove_orphans MarkdownDescription state the default true vs the CLI's false and why (first sentences unchanged; not RequiresReplace).
[P5.1-R] [BUILD] renames: renames_post.py accepts override rows with `"add": true` (appended when no row matches, updated when one does, table re-sorted with build_renames.py's key; idempotent, verified by running twice). renames_overrides.json adds ephemeral.docker_registry_token token -> token.token (string -> object {token, expires_in, issued_at}).
[P5.1-R] [VERIFIED] Parity would NOT have flagged the missing row: it only walks kreuzwerker v4.6.0 attributes, and docker_registry_token is ours. Added a check in tools/parity (previous.go): paritydata/ours_pre_pass5_attrs.json (678 attributes, extracted with the new `-extract-previous` flag from the genskill reference at 97ec6bf, the last pre-Pass 5 commit) vs the current .claude/skills reference; every attribute gone or changed type needs a renames.json previous_attr row (or a parent's). Unit tests in previous_test.go.
[P5.1-R] [FIXED] The new check found 20 unrecorded changes, not 1: the token row plus ephemeral.docker_exec_output container -> container_id and 18 list-item fields of data.docker_configs/secrets/nodes/services that moved under the upstream objects (e.g. nodes.hostname -> nodes.description.hostname, services.mode string -> services.spec.mode object). All 20 added as `add` rows; renames.json +20 rows, no existing row changed.
[P5.1-R] [VERIFIED] With the token row removed from a copy of renames.json, `go run ./tools/parity -renames <copy> -reference .claude/skills/terraform-provider-docker/reference` exits 1 naming `ephemeral.docker_registry_token token: string -> object`; with the real file, Invoke-Build Parity passes (685 kreuzwerker attributes 0 open; 678 previous, 0 unrecorded).
[P5.1-R] [VERIFIED] Invoke-Build Guide: migration guide unchanged (it lists kreuzwerker rows only; the new rows have kreuzwerker_attr null). GuideCheck passes.
[P5.1-R] [ASSUMED] The previous-schema check reads the generated .claude/skills reference, which Z regenerates at the end; if a Pass 5.1 lane changes an attribute's type, Parity will ask for a renames row (intended). With a custom -renames and no -reference the check is skipped (tool tests use synthetic renames files).
[P5.1-R] [TODO] Z: regenerate reference/inventory.json + INVENTORY.md (Invoke-Build Inventory), contract_gen.go (ContractGen), docs and skill (Docs / GenSkill): fragments A/B/D descriptions and the compose_resource.go descriptions changed.
[P5.1-R] [TODO] Z: BUILD_REPORT.md Pass 5.1: supersede the Pass 5 [P5-A]/[P5-B] ASSUMED lines on raw outputs with the table below (they also missed that docker_swarm inspect_json carried the join tokens).

## Summary

Raw outputs in inventory.json: 110. Engine API responses (in scope): 51.

| | before | after |
|---|---|---|
| byte-exact (client raw body) | 22 (trailing newline kept or not depending on read boundaries) | 22 (deterministic: surrounding whitespace dropped) |
| re-encoded | 29 (encoding/json, struct field order; 2 with Engine-dependent list order; swarm inspect_json leaked join tokens) | 29 canonical + deterministic (keys sorted, lists sorted or meaningful order) |
| missing raw bodies Pass 5 overlooked | 0 | 0 |

| type.output | before | after | method |
|---|---|---|---|
| docker_container.inspect_json | exact (+/- \n) | exact | ContainerInspect Raw + rEngineBody |
| data.docker_container.inspect_json | exact (+/- \n) | exact | ContainerInspect Raw + rEngineBody |
| docker_image.inspect_json | exact (+\n) | exact | ImageInspectWithRawResponse + rEngineBody |
| docker_tag.inspect_json | exact (+\n) | exact | ImageInspectWithRawResponse + rEngineBody |
| data.docker_image.inspect_json | exact (+\n) | exact | ImageInspectWithRawResponse + rEngineBody |
| docker_network.inspect_json / data | exact (+/- \n) | exact | NetworkInspect Raw + rEngineBody |
| docker_volume.inspect_json / data | exact (+/- \n) | exact | VolumeInspect Raw + rEngineBody |
| docker_plugin.inspect_json / data | exact (+/- \n) | exact | PluginInspect Raw + rEngineBody |
| docker_config / data.docker_config .inspect_json | exact (+/- \n) | exact | ConfigInspect Raw + rEngineBody |
| docker_secret / data.docker_secret .inspect_json | exact (+/- \n) | exact | SecretInspect Raw + rEngineBody |
| docker_service / data.docker_service .inspect_json | exact (+/- \n) | exact | ServiceInspect Raw + rEngineBody |
| docker_node, docker_swarm_node / data.docker_node .inspect_json | exact (+/- \n) | exact | NodeInspect Raw + rEngineBody |
| data.docker_task.inspect_json | exact (+/- \n) | exact | TaskInspect Raw + rEngineBody |
| data.docker_container_stats.stats_json | exact (trailing \n trimmed) | exact | stats body + rEngineBody |
| docker_swarm_init / data.docker_swarm .inspect_json | re-encoded, JoinTokens leaked | canonical, ClusterInfo only | rCanonicalJSON(ClusterInfo) |
| docker_registry_image.distribution_json / data.docker_registry_image.inspect_json | re-encoded | canonical | rCanonicalJSON |
| ephemeral.docker_registry_login.auth_json | re-encoded | canonical | rCanonicalJSON |
| data.docker_containers.containers_json | re-encoded, sorted | canonical, name/ID | rCanonicalList |
| data.docker_images / image_history / image_search / image_attestations | re-encoded | canonical | imgMarshal -> rCanonicalJSON |
| data.docker_info / version / networks / volumes / plugins / system_df | re-encoded (df Items unsorted) | canonical (df Items sorted) | objJSON -> rCanonicalJSON, rSortJSONArray |
| data.docker_container_changes.changes_json | re-encoded, Engine order | canonical, sorted by Path | rSortJSONArray + rCanonicalList |
| data.docker_container_top.top_json | re-encoded | canonical | rCanonicalJSON |
| data.docker_container_file.stat_json | re-encoded | canonical | rCanonicalJSON |
| data.docker_configs / nodes / secrets / services / tasks | re-encoded, sorted | canonical | swarmListInput -> rCanonicalList |
| data.docker_compose_containers / images / top / volumes, data.docker_stack.services_json | re-encoded | canonical | composeMarshal -> rCanonicalJSON |

Renames: +20 rows (ephemeral.docker_registry_token.token string -> object, plus 19 other own changes the audit missed); Parity now checks our own pre-Pass 5 schema (678 attributes, 0 unrecorded).

Open items: the two TODOs for Z above. data.docker_image_attestations is covered by the shared imgMarshal path and TestRCanonicalJSON only: the fake Engine negotiates API 1.47 and the data source needs 1.55.
