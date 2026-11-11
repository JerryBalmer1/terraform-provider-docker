# Pass 6b recovery (2026-11-10)

Started 16:54 from develop at 9c3da58 with 341 staged paths (snapshot refs/backup/p6b-interrupted = 60ad3562, not
touched). No worktrees, no harness containers, no test processes.

## Lane status (from the lane reports, BUILD_REPORT.md Pass 6b and the tree)

- Lane 0: complete (re-pin compose v5.7.0 / docker-cli v29.9.3, harness compose binary, Invoke-E2E -Script, task-file
  placeholders, ownership). lane-0.md.
- Lane E: complete. 140 example tests, module, ExampleCoverage / ManualTaskCheck, 7 manual scenarios. 135/140 passed
  on the harness; the 5 failures were provider findings handed to Lane Z. lane-E.md.
- Lane W: complete. tools/pairwise, 4 models, run 2 clean except service replicas + global (handed to Z). lane-W.md.
- Lane V: complete. 93 skips audited (49 citations fixed, 1 skip removed), exec stdin, preflight follow-up, compose
  push ignore_missing / run start_dependencies + no_recreate. lane-V.md.
- Lane Z, done per its log lines and verified in the tree: patches E/W/V applied; every handed-over finding fixed
  (5 left open in open-findings.md with reproducers); `print` -> Write-Build in every build task file (no bare `print`
  call left); $PreReleaseTasks has ExampleTests and PairwiseTest after TestAcc; /dist/ ignored; CLAUDE.md task lists
  and repo map; climap; CHANGELOG ### Pass 6b; generated files regenerated; one PreRelease run (12/13: ExampleTests
  failed on a data.docker_tasks crash, fixed, the 3 affected examples re-run green); commit groups 0/E/W/V/Z cover
  the 341 staged paths exactly once (checked: 341 = 341, no duplicates, none missing).
- Lane Z, not done: the BUILD_REPORT ### Summary for Pass 6b; a full ExampleTests run after the docker_tasks fix.
  The session then waited on %TEMP%\tpd-p6b\logs\lane-E-session-run2.status after it had itself deleted
  %TEMP%\tpd-p6b (BUILD_REPORT line "[P6b-Z] [VERIFIED] %TEMP%\tpd-p6b ... removed"); nothing could ever write it.

## Gates now

`Invoke-Build ., FmtCheck` through build/Invoke-Logged.ps1: 15 tasks, 0 errors, 2m49s
(test/harness/.logs/p6b/s1-default-fmt.log).

## ExampleTests "run 1" and "run 2"

Run 1 was not slow: Lane E stopped it by hand because Invoke-TfExample ran `terraform init` with the harness's
Terraform 1.14.9, which fails for a dev-overridden provider the registry does not have (open-findings.md 3). The
runner then stopped running init and run 2 was the first full run: 140 tests (99 plain, 41 swarm), 124 passed,
16 failed (fixed by Lanes E and Z, except the 5 handed over and then fixed by Z). Inside PreRelease the full
ExampleTests took 21m42s (two harness up/down cycles, about 9 s per example); one failure (data.docker_tasks
concurrent-read crash, fixed). Nothing measured needs more than the 90-minute cap; what was missing was a per-example
timeout (a hang would stall the run indefinitely), a way to re-run a subset by name, and results outside %TEMP%.

## What this session changes

Step 2: per-example 5-minute deadline (+3 minutes teardown) in the module, -Filter, results and a by-name summary
under test/harness/.logs/examples, a pass with no selected example is skipped, one plugin cache. Step 3: the
remaining Lane Z items, each PreRelease gate run once through the wrapper under its cap.
