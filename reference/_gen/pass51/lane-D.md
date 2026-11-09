# Pass 5.1 Lane D: Dockerfile round-trips

## Log

[P5.1-D] [VERIFIED] Baseline: TestDockerfileGoldenDocExamples extracts 213 ```dockerfile blocks (reference.md + rules/*.md). 196 round-tripped. The other 17 were dropped silently by three `fragments++; continue` paths (BuildKit parser error; our parser error, after the existing "FROM scratch" completion for instructions before FROM; no stage). There was no list. The only guards were len(ex) >= 100 and roundTripped >= 60, so a regression of up to 136 examples would have passed.
[P5.1-D] [BUILD] Replaced the silent skips and the >= 60 threshold with internal/provider/testdata/dockerfile/roundtrip_allowlist.txt (format documented at the top: `<file>#<nnn> <sha256[:12]> fragment|blocked <reason>`). Every unlisted example must round-trip. A listed example fails the test if it now round-trips, if its text changed (hash), or if it no longer exists. A malformed or duplicate line also fails. The assertion is unchanged: parse -> render -> parse is DeepEqual, and render is idempotent. It is now in dockerfileRoundTrip (error-returning), and dockerfileAssertRoundTrip wraps it.
[P5.1-D] [BUILD] Snippet completion extended: an example made only of parser directives / comments (BuildKit: "file with no instructions") gets a "FROM scratch" line appended. This is the same completion the test already applied to instructions shown before FROM. These 8 examples now exercise the directive renderer (syntax, escape=\ and `, check=skip/error, unknown directives and a demoted syntax directive kept as comments) against the real doc text.
[P5.1-D] [TEST] 8 of the 17 round-trip after completion: reference.md#008 #009 #010 #011 #012 #016 #017 #018. The parser and renderer needed no change.
[P5.1-D] [VERIFIED] The other 9 are fragments (syntax templates, `...` elisions, one example the reference itself labels invalid). They are allowlisted with reasons. None is a parser/renderer gap, so none is BLOCKED.
[P5.1-D] [VERIFIED] Negative paths checked by hand with temporary allowlist edits (reverted): changed hash, missing id, and an entry that round-trips each fail with the allowlist line number.
[P5.1-D] [TEST] go test ./internal/provider -short -run TestDockerfile: all pass (GoldenDocExamples, GoldenFixtures, data source, seam, structured tests). go vet ./internal/provider clean. gofmt clean on dockerfile_seam_test.go.
[P5.1-D] [ASSUMED] No golden fixture added: no renderer change was made. The 8 completed directive examples are pinned by the doc-example test itself.

## Summary

| | before | after |
|---|---|---|
| examples total | 213 | 213 |
| round-trip | 196 | 204 |
| allowlisted (fragment) | 0 (17 skipped silently) | 9 |
| allowlisted (blocked) | 0 | 0 |

| id | what it is | what happened |
|---|---|---|
| from-platform-flag-const-disallowed.md#003 | three FROM stages with `...` bodies | allowlisted fragment: `...` elisions |
| reference.md#000 | `# Comment` / `INSTRUCTION arguments` | allowlisted fragment: generic format template |
| reference.md#004 | `# direc \` / `tive=value` | allowlisted fragment: the reference shows it as invalid (no continuation in directives) |
| reference.md#008 | unknown directive, then `# syntax=value` (a comment) | round-trips (directive header completed with FROM scratch) |
| reference.md#009 | five spellings of `#directive=value` | round-trips (completed) |
| reference.md#010 | `# syntax=docker/dockerfile:1` | round-trips (completed) |
| reference.md#011 | `# escape=\` | round-trips (completed) |
| reference.md#012 | ``# escape=` `` | round-trips (completed) |
| reference.md#015 | `# check=skip=<checks\|all>` / `# check=error=<boolean>` | allowlisted fragment: syntax template, duplicate check directive (BuildKit rejects it) |
| reference.md#016 | `# check=skip=JSONArgsRecommended,StageNameCasing` | round-trips (completed) |
| reference.md#017 | `# check=error=true` | round-trips (completed) |
| reference.md#018 | `# check=skip=JSONArgsRecommended;error=true` | round-trips (completed) |
| reference.md#028 | `FROM [--platform=<platform>] <image> [AS <name>]` | allowlisted fragment: syntax template |
| reference.md#029 | `FROM ... <image>[:<tag>] ...` | allowlisted fragment: syntax template |
| reference.md#030 | `FROM ... <image>[@<digest>] ...` | allowlisted fragment: syntax template |
| reference.md#120 | `ARG <name>[=<default value>] ...` | allowlisted fragment: syntax template |
| reference.md#134 | `ONBUILD <INSTRUCTION>` | allowlisted fragment: syntax template |

Files: internal/provider/dockerfile_seam_test.go (changed), internal/provider/testdata/dockerfile/roundtrip_allowlist.txt (new). Parser and renderer (dockerfile_seam.go) unchanged.
