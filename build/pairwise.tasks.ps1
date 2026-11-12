# Pairwise combination tests (Pass 6b Lane W): tools/pairwise generates configs for docker_container, docker_service,
# docker_network and docker_volume from reference/_gen/pairwise/<type>.json; PairwiseTest applies them on the harness.
# Dot-sourced by terraform-provider-docker.build.ps1, so it shares that script's variables and helpers.
#
# Environment:
#   PAIRWISE_OUT    work directory (default test/harness/.logs/pairwise, git-ignored): bin/ (tool), mirror/ (provider
#                   filesystem mirror), configs/, results.json, logs
#   PAIRWISE_TYPES  comma-separated subset of the four types (default: all four)

# Synopsis: Generate the pairwise configs (tools/pairwise -gen) into $env:PAIRWISE_OUT/configs; no harness.
task PairwiseGen {
    requires -Variable GoExe
    $work = Get-PairwiseWork
    $tool = Join-Path $work.Bin "pairwise$($work.Exe)"
    exec { & $GoExe build -o $tool ./tools/pairwise }
    exec { & $tool -gen -out $work.Configs -types ($work.Types -join ',') }
}

# Synopsis: Apply every pairwise config on the dind harness, compare with inspect, require an empty second plan, destroy; fails on any "Outputs contract value dropped" line. (Pass 6b Lane W)
task PairwiseTest PairwiseGen, {
    requires -Variable GoExe
    requires -Path $Harness
    $work = Get-PairwiseWork
    $tool = Join-Path $work.Bin "pairwise$($work.Exe)"
    # The provider from a local filesystem mirror (TfDockerTest Initialize-TfProviderMirror: go build, zipped under the
    # registry path layout), so terraform init runs for real, writes the lock file and verifies the checksum.
    Import-Module (Join-Path $BuildRoot 'test/TfDockerTest') -Force
    $runner = Join-Path $BuildRoot 'tools/pairwise/run.ps1'
    # Gate ledger (build/ledger.tasks.ps1): one unit per generated config <type>/<nn>; unchanged configs are skipped
    # (-Full runs all). The skipped configs' folders are removed from this run's configs/ (PairwiseGen regenerates
    # them every run), so the runner and the report see only the configs that run.
    Invoke-LedgerGate -Gate pairwise -PlanArgs @('-configs', $work.Configs) -Run {
        param($toRun)
        $run = @{}
        foreach ($d in $toRun) { $run[$d.key] = $d }
        foreach ($t in Get-ChildItem -LiteralPath $work.Configs -Directory) {
            foreach ($c in Get-ChildItem -LiteralPath $t.FullName -Directory) {
                if (-not $run.ContainsKey("$($t.Name)/$($c.Name)")) { Remove-Item -LiteralPath $c.FullName -Recurse -Force }
            }
        }
        $types = @($work.Types | Where-Object { $tt = $_; @($toRun | Where-Object { $_.extra.type -eq $tt }).Count })
        $mirror = Initialize-TfProviderMirror -MirrorDir (Join-Path $work.Root 'mirror') -Force
        $cliConfig = New-TfMirrorConfig -ProvidersDir $mirror.Providers -Path (Join-Path $work.Root 'terraform.rc')
        $plainTypes = @($types | Where-Object { $_ -ne 'docker_service' })
        $swarmTypes = @($types | Where-Object { $_ -eq 'docker_service' })
        $failed = @()
        foreach ($pass in @(@{ Name = 'plain'; Types = $plainTypes; Swarm = $false }, @{ Name = 'swarm'; Types = $swarmTypes; Swarm = $true })) {
            if ($pass.Types.Count -eq 0) { continue }
            $log = Join-Path $work.Root "pairwise-$($pass.Name).log"
            $e2eArgs = @{
                Script     = $runner
                ScriptArgs = @('-OutDir', $work.Configs, '-Types', ($pass.Types -join ','), '-Pairwise', $tool, '-CliConfig', $cliConfig)
                Log        = $log
            }
            if ($pass.Swarm) { $e2eArgs.SwarmInit = $true }
            try { $global:LASTEXITCODE = 0; & $Harness @e2eArgs; if ($LASTEXITCODE -ne 0) { $failed += $pass.Name } }
            catch { Write-Warning "PairwiseTest $($pass.Name) pass: $_"; $failed += $pass.Name }
        }
        $results = Join-Path $work.Root 'results.json'
        $summary = @(exec { & $tool -report -out-dir $work.Configs -results $results })
        $summary | ForEach-Object { Write-Build $(if ($_ -like 'FINDING*') { 'Yellow' } else { 'Cyan' }) $_ }
        Write-Build Cyan "PairwiseTest: results in $results"
        $findings = @($summary | Where-Object { $_ -like 'FINDING*' })
        # One ledger result per config: no run.json = no result; a FINDING line for its config name = fail.
        $rows = foreach ($d in $toRun) {
            $runJson = Join-Path (Join-Path $work.Configs $d.key) 'run.json'
            if (-not (Test-Path -LiteralPath $runJson)) { continue }
            $secs = 0
            try { $secs = [long]((Get-Content -LiteralPath $runJson -Raw | ConvertFrom-Json).apply_seconds) } catch {}
            $bad = @($findings | Where-Object { $_ -like "FINDING $($d.extra.config):*" }).Count
            @{ key = $d.key; status = $(if ($bad) { 'fail' } else { 'pass' }); duration_ms = $secs * 1000 }
        }
        $err = if ($failed.Count) { "harness pass failed: $($failed -join ', ')" } elseif ($findings.Count) { "$($findings.Count) finding(s); see $results" } else { $null }
        @{ Results = @($rows); Error = $err }
    }
}

# Get-PairwiseWork returns the work directories (PAIRWISE_OUT) and the types (PAIRWISE_TYPES).
function Get-PairwiseWork {
    $root = if ($env:PAIRWISE_OUT) { $env:PAIRWISE_OUT } else { Join-Path $BuildRoot 'test/harness/.logs/pairwise' }
    $types = if ($env:PAIRWISE_TYPES) { @($env:PAIRWISE_TYPES -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ }) }
    else { @('docker_container', 'docker_network', 'docker_service', 'docker_volume') }
    $w = [pscustomobject]@{
        Root    = $root
        Bin     = Join-Path $root 'bin'
        Configs = Join-Path $root 'configs'
        Types   = $types
        Exe     = $(if ($IsWindows -or $env:OS -eq 'Windows_NT') { '.exe' } else { '' })
    }
    New-Item -ItemType Directory -Force -Path $w.Bin | Out-Null
    $w
}
