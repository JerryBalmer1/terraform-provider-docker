# Gate ledger (Pass 6g): unchanged test units are not re-run. Dot-sourced by terraform-provider-docker.build.ps1, so it
# shares that script's variables and helpers.
#
# tools/ledger plans a gate (one row per unit: a Go package, a TestAcc function, an example folder, a pairwise config),
# the gate runs only the units the plan marks to run, and tools/ledger records the outcomes in
# test/harness/.ledger/<gate>.json (committed) once, at the end of the gate. A unit is skipped when its input hash
# (its inferred inputs plus the always-in set) equals the one recorded with its last pass. -Full or TPD_LEDGER=off runs
# every unit; PreRelease always runs with the ledger off. Rows are written by tools/ledger only, never by hand.

$LedgerWork = Join-Path $TpdWork 'ledger'
$LedgerOff = [bool]$Full -or ($env:TPD_LEDGER -eq 'off')
$script:LedgerTool = $null

# Builds tools/ledger once per Invoke-Build run.
function Get-LedgerTool {
    if ($script:LedgerTool) { return $script:LedgerTool }
    $null = New-Item -ItemType Directory -Force $LedgerWork
    $exe = Join-Path $LedgerWork ('ledger' + $(if ($IsWindows -or $env:OS -eq 'Windows_NT') { '.exe' } else { '' }))
    exec { & $GoExe build -o $exe ./tools/ledger }
    $script:LedgerTool = $exe
    $exe
}

# Invoke-LedgerGate: the one entry point of the gate tasks.
#   -Gate     unit, testacc, examples or pairwise
#   -PlanArgs extra tools/ledger plan arguments (-swarm-regex, -configs)
#   -Run      script block called with the decisions to run (objects with Key, Hash, Reason, Extra); it runs them and
#             returns @{ Results = @(@{ key; status = pass|skip|fail|notrun; duration_ms }); Error = <text or $null> }.
#             It is not called when nothing is to run (no harness comes up).
# Writes the ledger, prints "<Gate>: ran N, skipped M, failed K" (also appended to $env:TPD_LEDGER_SUMMARY, which
# Invoke-Logged puts on its exit line), then fails the task if the run reported an error or a unit failed.
function Invoke-LedgerGate {
    param(
        [Parameter(Mandatory)][ValidateSet('unit', 'testacc', 'examples', 'pairwise')][string]$Gate,
        [string[]]$PlanArgs = @(),
        [Parameter(Mandatory)][scriptblock]$Run
    )
    # Locals carry an lg prefix: -Run executes in a child scope of this function, so a plain name here ($tool, $plan,
    # $lines) would shadow the gate task's variable of the same name inside the block (Pass 6g: the pairwise block ran
    # the ledger binary as its $tool).
    $lgExe = Get-LedgerTool
    $lgStamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $lgPlanFile = Join-Path $LedgerWork "$Gate-$lgStamp.plan.json"
    $lgResFile = Join-Path $LedgerWork "$Gate-$lgStamp.results.json"
    $lgArgs = @('plan', '-gate', $Gate, '-out', $lgPlanFile) + $PlanArgs
    if ($LedgerOff) { $lgArgs += '-full' }
    foreach ($l in @(exec { & $lgExe @lgArgs })) { Write-Build $(if ($l -match '^ledger \S+ (RUN |skip) ') { 'Gray' } else { 'Cyan' }) $l }
    $lgPlan = [IO.File]::ReadAllText($lgPlanFile) | ConvertFrom-Json
    $lgToRun = @($lgPlan.decisions | Where-Object run)
    $lgOutcome = $null
    if ($lgToRun.Count) {
        # The block's other output (harness and test logs) goes to the host; the outcome is its hashtable with Results.
        foreach ($o in @(& $Run $lgToRun)) {
            if ($o -is [System.Collections.IDictionary] -and $o.Contains('Results')) { $lgOutcome = $o } else { Write-Host $o }
        }
        if (-not $lgOutcome) { $lgOutcome = @{ Results = @(); Error = 'the gate returned no outcome' } }
    } else {
        Write-Build Green "${Gate}: every unit unchanged since its last pass; nothing to run"
    }
    $lgResults = @(if ($lgOutcome -is [hashtable] -and $lgOutcome.ContainsKey('Results')) { $lgOutcome.Results } elseif ($lgOutcome -and $lgOutcome.PSObject.Properties['Results']) { $lgOutcome.Results } else { @($lgOutcome) | Where-Object { $_ -and $_.PSObject.Properties['Key'] } })
    [IO.File]::WriteAllText($lgResFile, (ConvertTo-Json -InputObject @($lgResults | ForEach-Object { [ordered]@{ key = $_.key; status = $_.status; duration_ms = [long]$_.duration_ms } }) -Depth 3))
    $lgRec = @(exec { & $lgExe record -plan $lgPlanFile -results $lgResFile })
    $lgSummary = ($lgRec | Where-Object { $_ -like 'ledger *' } | Select-Object -Last 1) -replace '^ledger ', ''
    $lgFailed = 0
    if ($lgSummary -match 'failed (\d+)') { $lgFailed = [int]$Matches[1] }
    Write-Build $(if ($lgFailed -or ($lgOutcome -and $lgOutcome.Error)) { 'Red' } else { 'Green' }) "$Gate gate: $($lgSummary -replace "^${Gate}: ", '')$(if ($LedgerOff) { ' (ledger off: full run)' })"
    if ($lgOutcome -and $lgOutcome.Error) { throw "$Gate gate: $($lgOutcome.Error)" }
    assert ($lgFailed -eq 0) "$Gate gate: $lgFailed unit(s) failed or produced no result"
}
