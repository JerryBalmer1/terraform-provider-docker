# Pre-release tasks (Pass 6a Lane P), dot-sourced by terraform-provider-docker.build.ps1: they share its variables
# and helpers ($GoExe, $GitExe, $BuildRoot, $Go, $Python, exec, assert, print).

# ---------------------------------------------------------------------------- upstream drift

# Where DriftCheck writes its report: $env:DRIFT_REPORT when set (the drift workflow), else the temp directory.
# The JSON report is the same path with .json. Fetched specs and fieldcov output go to the temp directory too,
# never under reference/.
$DriftWork = Join-Path ([System.IO.Path]::GetTempPath()) 'tpd-drift'
$DriftReport = if ($env:DRIFT_REPORT) { $env:DRIFT_REPORT } else { Join-Path $DriftWork 'drift-report.md' }

# Synopsis: Upstream drift: newest release of every reference/SOURCES.md source vs its pin; new operations / writable fields must be mapped or in reference/_gen/drift-accepted.json (go run ./tools/drift; needs the network; never changes a pin).
task DriftCheck {
    requires -Variable GoExe, GitExe
    $null = New-Item -ItemType Directory -Force $DriftWork
    exec { & $GoExe run ./tools/drift -work $DriftWork -report $DriftReport -go $GoExe -git $GitExe } -ErrorMessage (
        "Upstream drift (exit 1: unmapped items not in reference/_gen/drift-accepted.json; exit 2: a probe or fieldcov error). " +
        "Report: $DriftReport")
    print Green "DriftCheck: report $DriftReport"
}

# ---------------------------------------------------------------------------- pre-release gate

# Every pre-release gate, in order. One list so a later pass can add a gate: insert its task name here.
# '.' is the default task (Build, Vet, Test and the generated-content checks). TestAcc runs the dind harness;
# Release is Lane G's (it builds the release artifacts and never tags, pushes or publishes).
$PreReleaseTasks = @(
    'DriftCheck'
    'DeferredReport'
    '.'
    'FmtCheck'
    'DocsValidate'
    'StubReport'
    'Parity'
    'GuideCheck'
    'ContractCheck'
    'TestAcc'
    'Release'
)

# Synopsis: Every pre-release gate ($PreReleaseTasks) in its own nested build; runs on past a failure, prints one table (task, result, duration), then fails if any gate failed. No tag, push or publish.
# Running on (instead of stopping at the first failure) keeps the table complete: one run shows every gate that
# needs work. Each gate is a nested Invoke-Build of this script, so a failing gate cannot stop the next one.
# PRERELEASE_TASKS (comma-separated task names) replaces the list, for testing PreRelease itself on cheap tasks
# only; the run then says so and is not a release gate.
task PreRelease {
    $tasks = $PreReleaseTasks
    $override = [bool]$env:PRERELEASE_TASKS
    if ($override) {
        $tasks = @($env:PRERELEASE_TASKS -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
        print Yellow "PreRelease: PRERELEASE_TASKS overrides the gate list (testing only, not a release gate): $($tasks -join ', ')"
    }
    assert ($tasks.Count -gt 0) 'PreRelease: no tasks to run'

    $rows = [System.Collections.Generic.List[object]]::new()
    $total = [System.Diagnostics.Stopwatch]::StartNew()
    foreach ($t in $tasks) {
        print Cyan "==== PreRelease [$($rows.Count + 1)/$($tasks.Count)]: $t"
        $sw = [System.Diagnostics.Stopwatch]::StartNew()
        $err = $null
        try { Invoke-Build $t $BuildFile -Go $Go -Python $Python }
        catch { $err = $_ }
        $sw.Stop()
        $msg = ''
        if ($err) {
            $msg = (("$err" -split "`r?`n") | Where-Object { $_.Trim() } | Select-Object -First 1)
            if ($msg.Length -gt 100) { $msg = $msg.Substring(0, 97) + '...' }
        }
        $rows.Add([pscustomobject]@{ Task = $t; Result = $(if ($err) { 'FAIL' } else { 'ok' }); Duration = $sw.Elapsed; Error = $msg })
    }
    $total.Stop()

    $failed = @($rows | Where-Object Result -eq 'FAIL')
    $fmt = '{0,-16} {1,-6} {2,10}  {3}'
    print Cyan ''
    print Cyan ("PreRelease summary" + $(if ($override) { ' (PRERELEASE_TASKS override)' } else { '' }))
    print Cyan ($fmt -f 'task', 'result', 'duration', 'error')
    foreach ($r in $rows) {
        print $(if ($r.Result -eq 'ok') { 'Green' } else { 'Red' }) ($fmt -f $r.Task, $r.Result, $r.Duration.ToString('hh\:mm\:ss'), $r.Error)
    }
    print Cyan ($fmt -f 'total', $(if ($failed.Count) { 'FAIL' } else { 'ok' }), $total.Elapsed.ToString('hh\:mm\:ss'), "$($rows.Count - $failed.Count) of $($rows.Count) passed")
    assert ($failed.Count -eq 0) "PreRelease: $($failed.Count) gate(s) failed: $(($failed | ForEach-Object Task) -join ', ')"
}
