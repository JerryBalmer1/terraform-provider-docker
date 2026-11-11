# Pass 6d open findings

Supersedes reference/_gen/pass6c/open-findings.md.

- Pass 6c findings 1, 2, 3 and 5 ship with v0.1.0 as known issues: reference/_gen/deferred.json kind `known_issue`
  (source reference/_gen/deferred.d/known_issues.json), ids `buildx-builder-cloud-unverified`,
  `compose-pre-start-per-replica`, `compose-use-api-socket-config-hash` and `compose-build-ssh-project-dropped`, each
  with a reproduction, a "Known issues" section on its docs page and a CHANGELOG.md [0.1.0] "Known issues" bullet.
  Finding 1's reproduction in the Pass 6c file named the wrong variables; the test reads
  `TF_ACC=1 TF_ACC_BUILD_CLOUD=1 TF_ACC_BUILD_CLOUD_BUILDER=<org>/<builder> DOCKER_REGISTRY_USER=... DOCKER_REGISTRY_PASS=...`.
- Pass 6c finding 4: the part "data.docker_registry_image `config` is never filled" is closed (Pass 6d Lane N; config,
  labels and created read from the registry's config blob; TestAccP6cImageDataRoundTrip asserts both image data sources'
  `config` non-null and equal). What is left of it is a new known issue, `registry-image-config-plain-http-remote`: a
  plain-HTTP registry on a non-loopback host still gives null config, with a warning. The rest of finding 4 (no harness
  round trip for the other data sources and list resources, only unit-level shape checks) stays open and is not a
  release blocker.

## New in Pass 6d

## 1. Upstream drift since Pass 6c

DriftCheck (2026-11-11) fails on two documentation-only sources: compose v5.7.0 -> v5.7.1 and the Dockerfile reference
dockerfile/1.28.0 -> dockerfile/1.29.0 (buildx v0.38.2 -> v0.39.0 also moved; diffed through fieldcov, 0 new operations
and 0 new fields, so it passes). No new operation or field was found, but neither release has been reviewed, so they are
not in reference/_gen/drift-accepted.json. Either review them and add `version:compose` / `version:buildkit` entries
(version_seen v5.7.1 / dockerfile/1.29.0), or re-pin in a later pass. Report:
test/harness/.logs/drift/drift-report.md.
