---
name: engine-api-watch
description: Use at the start of every pass on terraform-provider-docker, and whenever a Docker Engine, Docker Desktop, moby or Engine API version is mentioned, upgraded or suspected of adding features. Runs tools/apiwatch to diff the pinned Docker Engine API spec against the newest published one, explains how to read its output, and how to file the result as TODO lines in BUILD_REPORT.md before re-pinning.
---

# Engine API watch

The provider talks only to the Docker Engine API, so every new endpoint or
field in that API is either something a provider type should expose or
something we decide to skip. `tools/apiwatch` makes that decision explicit
and keeps it from being forgotten.

State lives in `tools/apiwatch/pinned.json`: the API version last reviewed
(`api_version`), where its spec came from (`url`, `url_template`), the sha256
of the spec bytes, and the byte-exact copy under `tools/apiwatch/spec/`.
`client_api_version` is the API version of the Go client in go.mod
(github.com/docker/docker), so you can see how far the client lags the spec.

## When to run it

- **At the start of every pass**, before planning lanes. A pass plan that
  ignores new API surface misses parity work.
- **Whenever a Docker version comes up**: a user mentions Docker Engine /
  Desktop / moby X, `docker version` on the harness or Desktop shows a newer
  API than pinned, go.mod bumps github.com/docker/docker or
  github.com/moby/moby/*, or CI's `Invoke-Build ApiWatchCheck` step fails.
- Not needed for passes that only touch docs or tests, unless CI's
  ApiWatchCheck step is red.

## How to run it

```
Invoke-Build ApiWatch                        # or: go run ./tools/apiwatch
Invoke-Build ApiWatchCheck                   # what CI runs (go run ./tools/apiwatch -check); exit 1 = newer spec published
go run ./tools/apiwatch -from 1.51 -to 1.56  # any two versions (both downloaded)
go run ./tools/apiwatch -todo -tag P5-0      # the same diff as BUILD_REPORT.md lines
go run ./tools/apiwatch -update              # re-pin the newest (or -to X) version
```

The default compares the pinned copy with the newest published version, found
by probing `v<major>.<minor+1>.yaml`, `+2`, ... on docs.docker.com until a 404.
Exit codes: 0 ok, 1 `-check` found an unpinned spec, 2 any error (network,
parse). A network failure is not a finding: say so and retry, don't re-pin.

## Reading the output

```
apiwatch: Engine API v1.51 -> v1.56

CHANGED  POST /volumes/create   -> resource docker_volume   (body #/definitions/VolumeCreateOptions -> #/definitions/VolumeCreateRequest)
NEW      GET /images/{name}/attestations -> resource docker_image  (operationId ImageAttestations)
FIELD+   HostConfig.Umask       -> resource docker_container
FIELD-   ContainerConfig.MacAddress -> resource docker_container
DEF+     IPAMStatus             -> resource docker_network  (1 field(s))
...
endpoints: 1 new, 6 changed, 0 removed; definitions: 31 new, 4 removed; fields: 6 added, 27 removed
```

- `NEW` / `CHANGED` / `REMOVED` are endpoints (`METHOD /path`). CHANGED lists
  added (`+in:name`) and removed (`-in:name`) parameters, a changed body
  schema, or a new deprecation.
- `FIELD+` / `FIELD-` are top-level properties of a definition; `DEF+` /
  `DEF-` are whole definitions.
- `-> <kind> <type>` is the provider type that should cover the change:
  `resource`, `data`, `action`, `ephemeral`, `function`, `list`, `provider`
  (a provider-block argument), `skip: <reason>` (deliberately not covered),
  or `unmapped` (no rule matched).
- The mapping is a set of rules in `tools/apiwatch/mapping.go`. It is a
  starting point, not a verdict: a `FIELD+` on `HostConfig` mapped to
  `docker_container` may also belong on `docker_service` (TaskSpec) or
  `docker_compose`. Check the reference in
  `.claude/skills/terraform-provider-docker/reference/` before deciding.
- `FIELD-` / `REMOVED` matter as much as additions: if the provider sets or
  reads a removed field (e.g. `ContainerConfig.MacAddress`,
  `NetworkSettings.IPAddress`), newer daemons ignore or drop it, and the
  provider may show perpetual drift. grep `internal/provider` for the Go
  field name.

## Filing the result

1. Run `go run ./tools/apiwatch -todo -tag P<pass>-<lane>`. It prints one
   line per provider type:
   - `[TODO] ... resource docker_container: review and cover or log as skipped: ...`
     for mapped groups,
   - `[ASSUMED] ... no provider change (<reason>): ...` for `skip` groups,
   - `[TODO] ... unmapped (...)` for groups no rule matched.
2. Append those lines to BUILD_REPORT.md under the current pass's log (the
   report is append-only). For each `unmapped` group, add a rule to
   `tools/apiwatch/mapping.go` (with a test case in `apiwatch_test.go`) and
   rerun so the line becomes a mapped TODO or a reasoned skip.
3. Only then run `go run ./tools/apiwatch -update` to pin the newest version,
   and log `[BUILD] apiwatch pinned vX (sha256 ...)`. Re-pinning without
   filing loses the delta: the next run diffs from the new pin.
4. Turning a TODO into work: each mapped line names the type whose owner
   lane implements it. The usual pattern is a new attribute plus unit test,
   acceptance test (skipping cleanly), example update, and a description
   genskill can read. A field that only exists on newer daemons needs an API
   version guard (the provider negotiates the version), so say which version
   introduced it.

## Gotchas

- docs.docker.com occasionally re-publishes a version's YAML; `-check` then
  reports "re-published". Diff with `-from X -to X` is not possible (one URL),
  so compare the pinned copy by running the default with `-to X`, file any
  changes, then `-update -to X`.
- The spec copy is committed byte-exact (`.gitattributes` marks it `-text`);
  never reformat it.
- The pinned version can be ahead of `client_api_version`: the provider still
  works against newer daemons through version negotiation, but new fields
  need the client library (go.mod) bumped before they can be set.

## DriftCheck

`Invoke-Build DriftCheck` (tools/drift, weekly in `.github/workflows/drift.yml`) covers the Engine API too: it
compares the moby `api/v*` tags and the docs.docker.com spec with the pin and, when the spec moved, maps the new
operations and writable fields through fieldcov. apiwatch stays the tool for the endpoint-level diff and for
re-pinning `tools/apiwatch/pinned.json`.
