# Pass 7 Lane 0: Docker Hub harvest, inventory and lane plan

Lane 0 is frozen once the lanes start. Ownership: `reference/_gen/pass5/ownership.json` section `pass7`
(`reference/_gen/generators/pass7/p7_ownership.py`; `--check <lane> <listing>` before handing over). Commit groups:
`reference/_gen/generators/pass7/p7_commit_groups.py <lane-0.patch>` (groups 0, H, T, O, D, Z, in that order).
One worktree per lane under `C:\__Code\tpd-wt\lane<X>`, detached at HEAD with the Lane 0 changes copied in.

## What Lane 0 left

- Spec: `reference/upstream/hub/docs.docker.com-hub-latest.yaml`, OpenAPI 3.2.0, info.version **2-beta**, sha256
  7c280db399f8..., downloaded 2026-11-11 (SOURCES.md row `hub`, pin `hub_api` in `reference/_gen/pins.py` and
  inventory.json `spec_pins`; ReferenceVerify `$ReferenceSha256`; DriftCheck source kind `openapi` in tools/drift).
- Inventory: `reference/_gen/fragments/hub.json` + `fragments/outputs/hub.json`, written by
  `reference/_gen/generators/pass7/p7_hub.py`. Layer `hub`, 39 items: 11 resources, 24 data sources (12 plural),
  1 action, 1 ephemeral resource, 2 skips. 54 of 54 operations placed, 14 of 14 tags covered (4 info tags in
  `info_tags`). 26 items carry `requires` (org / plan=...).
- Every non-skipped hub item has `planned: "pass7"`: status `planned` (validate.py S1 lists it, never fails),
  registrygen and contractgen leave it out, fieldcov credits its 61 writable fields as `planned`. **The lane that
  registers a type removes `planned` from its item** (in p7_hub.py, then rerun it; Lane Z merges): merge.py fails on a
  registered item that is still planned, and from then on its fields must map (auto-schema) or fieldcov reports gaps.
- Tooling changed in Lane 0: merge.py (hub layer, `openapi` sources, hub op/tag coverage, `planned`, `requires`),
  validate.py (S1 planned), pins.py (`spec_pins`), registrygen + contractgen (skip planned), fieldcov (hub source),
  tools/drift (openapi kind), SCHEMA.md (`hub_api_version`, new item keys), CONTRACT.md (Hub owner-label exclusion).
- Live probe: [hub-probe.md](hub-probe.md). It drives two naming questions below.

## Decisions to apply (user, Pass 7)

- Names only from the spec. Groups, not teams.
- `docker_hub_access_token` (resource) returns the value once: sensitive `token` + optional `secret_output_path`
  input. Organization access tokens are the separate `docker_hub_org_access_token`.
- Ephemeral `docker_hub_token` (POST /v2/auth/token), JWT sensitive. Provider block: `hub_username` and `hub_token`
  (sensitive), env fallbacks `DOCKER_HUB_USERNAME` / `DOCKER_HUB_TOKEN`.
- No owner labels on Hub objects (CONTRACT.md "Docker Hub objects").
- Every `docker_hub_*` type registers with `ownerLabelExclusions` reason "Docker Hub objects have no labels in the Hub API".

## Open naming questions (answer before the lanes start)

- **P7-Q1** `docker_hub_repository` and `docker_hub_repository_group`: the spec has no delete (and no repository
  update) route. Keep them as resources whose destroy only forgets (as inventoried), make repository creation an
  action, or defer both?
- **P7-Q2** A PAT-issued JWT is refused by `/v2/access-tokens`, audit logs and every org route (probe P7-F1). Add a
  write-only `hub_password` provider argument for PAT management, document the PAT types as password-auth only, or
  have org items take an OAT (`hub_token` = an OAT works for the org routes that say so)?

## Lanes

| lane | owns | items | acceptance |
|---|---|---|---|
| **H** Hub client + auth + repositories/tags | `internal/provider/hub_*` not claimed by T/O, `provider.go` (hub_username, hub_token, env fallbacks), `provider_hub*` | client: base URL https://hub.docker.com, JWT exchange + cache per provider, X-RateLimit-* handling (retry after Reset on 429), pagination (next links), raw body byte-exact; `docker_hub_repository`, data `docker_hub_repositories`, `docker_hub_repository`, `docker_hub_repository_tags`, `docker_hub_repository_tag`, resource `docker_hub_repository_immutable_tags`, data `docker_hub_repository_immutable_tags_verify` | live: TestAccHub* gated by `TF_ACC_HUB=1` + DOCKER_HUB_* (namespace jlbalmerjr, `tfacc-hub-*` repositories only; reads of a public repository); unit: httptest fake Hub |
| **T** tokens + ephemeral | `internal/provider/hub_access_token*`, `hub_org_access_token*`, `ephemeral_hub_token*` | ephemeral `docker_hub_token`; `docker_hub_access_token` (+ `secret_output_path`, file 0600, written once after create), data `docker_hub_access_tokens`, `docker_hub_access_token`; `docker_hub_org_access_token` + its two data sources (fake only) | unit against the fake; live only for docker_hub_token (PAT exchange). PAT CRUD live depends on P7-Q2 |
| **O** org-scoped, fake Hub only | `internal/provider/hub_org_*`, `hub_group*`, `hub_invite*`, `hub_scim*`, `hub_audit*`, `hub_repository_group*`, `hub_fake*` (the shared fake Hub server for unit tests) | `docker_hub_org_settings` (resource + data), `docker_hub_org_members`, `docker_hub_org_members_export`, `docker_hub_org_member`, `docker_hub_org_invites`, `docker_hub_invite`, action `docker_hub_invite_resend`, `docker_hub_group` (+ data, plural), `docker_hub_group_member`, `docker_hub_group_members`, `docker_hub_repository_group`, SCIM: 6 data sources + `docker_hub_scim_user` (scimToken auth: provider argument `hub_scim_token`, sensitive, env `DOCKER_HUB_SCIM_TOKEN`; a Lane H provider.go TODO), `docker_hub_audit_logs`, `docker_hub_audit_log_actions` | the fake Hub serves the spec's documented responses; acceptance tests exist and skip without `TF_ACC_HUB_ORG` (deferred.d/acceptance.json rows: no organization) |
| **D** docs, examples, skill | `examples/**/docker_hub_*`, `examples/provider/*`, `templates/*`, `.claude/skills/*`, tools/genskill, tools/climap (`docker login` -> docker_hub_token where it fits, hub-tool rows: none, the spec is the source), tools/parity (`docker_hub_*` rows: no kreuzwerker equivalent) | one example + `<name>.tests.ps1` per type (org ones validate-only, with a deferred.d/examples.json row); MarkdownDescription first sentences; the provider page's Hub section | ExampleValidate; ExampleCoverage |
| **Z** inventory and gates | fragments, generated files, merge/validate/pins, tools/{registrygen,contractgen,fieldcov,drift,doccounts,deferred}, deferred.d, CONTRACT.md, SCHEMA.md, BUILD_REPORT, CHANGELOG, README, CLAUDE.md | removes `planned` as lanes land, `Invoke-Build Inventory, RegistryGen, Inventory, ContractGen`; owner_label.go exclusions TODO for the lanes; CLAUDE.md repo map (hub layer, the openapi drift kind) and counts; deferred.d rows for the org/SCIM acceptance tests | default task, FieldCoverage (0 gaps), DriftCheck, DocCountsCheck, InventoryValidate (0 planned left, or each left one in a deferred row) |

## Cross-lane TODOs already known

- [P7-H] provider.go: `hub_username`, `hub_token` (sensitive), `hub_scim_token` (sensitive, for Lane O); env
  fallbacks DOCKER_HUB_USERNAME, DOCKER_HUB_TOKEN, DOCKER_HUB_SCIM_TOKEN. Expose the Hub client to T and O through
  one constructor (`hubClient(ctx, providerData)`), never a second auth path.
- [P7-T] `secret_output_path` has no precedent: docker_swarm_tokens has no such argument (the closest is the action
  `result_path`, CONTRACT.md Action results). Define it once (file mode 0600, value + newline, written after create,
  never re-read) and reuse it for `docker_hub_org_access_token`.
- [P7-Z] owner_label.go: add every registered docker_hub_* resource and action to `ownerLabelExclusions`; add the rows
  to CONTRACT.md's Excluded types table.
- [P7-Z] The structured outputs follow the spec; the live responses carry `digest`, `media_type`, `content_type`
  (tags) and `registry_region` (repository) beyond it (P7-F3, P7-F4): decide whether to add them by decision rows in
  fragments/outputs/hub.json or wait for the spec.
- [P7-Z] tools/fieldcov: a unit test for the hub source (planned credit, auto-schema once registered, gap lane H).
- [P7-Z] `reference/_gen/deferred.d/upstream.json` `about` and the DriftCheck synopsis (build/prerelease.tasks.ps1):
  mention the Hub keys `hub:op/<METHOD> <path>` and `hub:schema/<Schema>.<prop>`.

## Not in the spec (no items)

Docker Hardened Images and Docker Scout: the pinned spec (2-beta) has no tag or operation for either. Per
DECISIONS.md the spec is the only naming source, so they get no item until a spec adds them (DriftCheck's hub source
fails on the new operations).
