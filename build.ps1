#Requires -Version 7.0
<#
.SYNOPSIS
    Windows-native build entry point for terraform-provider-docker (make is not needed).

.DESCRIPTION
    Mirrors the GNUmakefile targets with the same commands. Tasks run in the order given;
    each runs at most once per invocation. Runs from any working directory (it switches to
    the repository root, $PSScriptRoot, and switches back at the end).

      build    go build ./...; go build -o . .          (provider binary in the repo root)
      vet      go vet ./...
      test     go test ./... -count=1 -timeout 10m       (unit tests; TestAcc* skip themselves)
      skill    go run ./tools/genskill                    (skills/terraform-provider-docker + README_resources.md)
      docs     skill, then tfplugindocs generate + validate
      check    gofmt -l, go vet, genskill -check, genskill -strict, tfplugindocs validate
               (runs every step, then fails listing the steps that failed)
      testacc  test/harness/Invoke-E2E.ps1 -Run TestAcc (last-acc.log), then -SwarmInit swarm tests (last-acc-swarm.log)
      e2e      test/harness/Invoke-E2E.ps1               (dind manager + 2 workers, TestAccE2E)
      fmt      gofmt -w .
      install  go install .                               (into GOBIN / GOPATH\bin, the dev_overrides path)
      compose2stack  go run ./tools/compose2stack -in <In> -out <Out> -mode <Mode>
               (convert a compose file to HCL once; -In/-Out are relative to the caller's directory)
      parity   go run ./tools/parity                      (kreuzwerker/docker v4.6.0 types vs ours: have / partial / missing)

.PARAMETER In
    compose2stack: compose file to read (default docker-compose.yml).

.PARAMETER Out
    compose2stack: .tf file to write (default stack.tf).

.PARAMETER Mode
    compose2stack: conversion mode passed to -mode (default swarm).

.EXAMPLE
    ./build.ps1                       # build
    ./build.ps1 -Task build,vet,skill
    pwsh -NoProfile -File build.ps1 -Task check
    ./build.ps1 -Task compose2stack -In docker-compose.yml -Out stack.tf -Mode swarm
    ./build.ps1 -Task parity
#>
[CmdletBinding()]
param(
    # The same set as a ValidateSet, but checked per comma-separated item: `pwsh -File build.ps1
    # -Task build,vet` passes the single string "build,vet" (only -Command parses it as an array),
    # which a plain ValidateSet rejects.
    [ArgumentCompletions('build', 'vet', 'test', 'docs', 'skill', 'check', 'testacc', 'e2e', 'fmt', 'install', 'compose2stack', 'parity')]
    [ValidateScript({
            $valid = 'build', 'vet', 'test', 'docs', 'skill', 'check', 'testacc', 'e2e', 'fmt', 'install', 'compose2stack', 'parity'
            foreach ($t in ($_ -split ',')) {
                if ($t.Trim() -notin $valid) { throw "Unknown task '$t'. Valid tasks: $($valid -join ', ')." }
            }
            $true
        })]
    [string[]]$Task = 'build',

    [string]$In = 'docker-compose.yml',
    [string]$Out = 'stack.tf',
    [string]$Mode = 'swarm'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Task = @($Task | ForEach-Object { $_ -split ',' } | ForEach-Object { $_.Trim() } | Where-Object { $_ })

$Go = if ($env:GO) { $env:GO } else { 'go' }

# compose2stack paths are given relative to where the caller ran build.ps1, but the tasks run
# from the repository root; resolve them before Push-Location.
$InPath = [System.IO.Path]::GetFullPath($In, (Get-Location).Path)
$OutPath = [System.IO.Path]::GetFullPath($Out, (Get-Location).Path)
$TfPluginDocs = 'github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs'

# Run a native command; throw if it exits non-zero.
# Deliberately a simple (non-advanced) function using $args, so arguments such as -o and
# -count=1 reach the native command instead of binding to common parameters.
function Invoke-Native {
    $FilePath, $ArgumentList = $args
    $ArgumentList = @($ArgumentList | Where-Object { $null -ne $_ })
    Write-Host ">> $FilePath $($ArgumentList -join ' ')" -ForegroundColor Cyan
    & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "'$FilePath $($ArgumentList -join ' ')' exited with code $LASTEXITCODE"
    }
}

$script:Done = [System.Collections.Generic.HashSet[string]]::new()

function Invoke-Task {
    param([Parameter(Mandatory)][string]$Name)
    if (-not $script:Done.Add($Name)) { return }
    Write-Host "== $Name" -ForegroundColor Green
    switch ($Name) {
        'build' {
            Invoke-Native $Go build ./...
            Invoke-Native $Go build -o . .
        }
        'vet' { Invoke-Native $Go vet ./... }
        'test' { Invoke-Native $Go test ./... -count=1 -timeout 10m }
        'skill' { Invoke-Native $Go run ./tools/genskill }
        'docs' {
            # The skill is regenerated first so a release can never ship a stale skill.
            Invoke-Task skill
            Invoke-Native $Go run $TfPluginDocs generate --provider-name docker
            Invoke-Native $Go run $TfPluginDocs validate --provider-name docker
        }
        'check' { Invoke-Check }
        'testacc' {
            $global:LASTEXITCODE = 0
            # Pass 1: plain manager (image, network, volume, container, swarm_init, e2e).
            # Both passes always run; the task fails afterwards if either did.
            $plain = 0
            try { & "$PSScriptRoot/test/harness/Invoke-E2E.ps1" -Run TestAcc -Timeout 120m -Log test/harness/last-acc.log; $plain = $LASTEXITCODE }
            catch { Write-Warning "testacc plain pass: $_"; $plain = 1 }
            # Pass 2: harness manager swarm-initialised, for tests that skip unless DOCKER_HOST is a swarm manager.
            $global:LASTEXITCODE = 0
            $swarmPass = 0
            try { & "$PSScriptRoot/test/harness/Invoke-E2E.ps1" -SwarmInit -Run 'TestAcc(Service|Stack|Secret|DockerConfig|SwarmTokens|Node_|Nodes_|SwarmNode|ComposeSwarm|ListSecret|P3SwarmList|Action(SwarmRotateTokens|NodeDrainActivate|ServiceScaleForceUpdateRollback))' -Timeout 120m -Log test/harness/last-acc-swarm.log; $swarmPass = $LASTEXITCODE }
            catch { Write-Warning "testacc swarm pass: $_"; $swarmPass = 1 }
            if ($plain -or $swarmPass) { throw "testacc failed (plain pass exit $plain, swarm pass exit $swarmPass)" }
        }
        'e2e' {
            $global:LASTEXITCODE = 0
            & "$PSScriptRoot/test/harness/Invoke-E2E.ps1"
            if ($LASTEXITCODE) { throw "e2e failed with exit code $LASTEXITCODE" }
        }
        'fmt' { Invoke-Native gofmt -w . }
        'install' { Invoke-Native $Go install . }
        'compose2stack' { Invoke-Native $Go run ./tools/compose2stack -in $InPath -out $OutPath -mode $Mode }
        'parity' { Invoke-Native $Go run ./tools/parity }
        default { throw "unknown task '$Name'" }
    }
}

# check runs every step even when an earlier one fails, then reports all failures.
function Invoke-Check {
    $failed = [System.Collections.Generic.List[string]]::new()

    Write-Host '>> gofmt -l .' -ForegroundColor Cyan
    $unformatted = @(& gofmt -l . | Where-Object { $_ })
    if ($LASTEXITCODE -ne 0) {
        $failed.Add("gofmt exited with code $LASTEXITCODE")
    }
    elseif ($unformatted.Count -gt 0) {
        Write-Host 'gofmt needed (run ./build.ps1 -Task fmt):' -ForegroundColor Yellow
        $unformatted | ForEach-Object { Write-Host "  $_" }
        $failed.Add("gofmt: $($unformatted.Count) file(s) need formatting: $($unformatted -join ', ')")
    }

    $steps = @(
        @{ Name = 'go vet'; Args = @('vet', './...') }
        @{ Name = 'genskill -check (skill and README_resources.md up to date)'; Args = @('run', './tools/genskill', '-check')
            Hint = 'run ./build.ps1 -Task skill and commit the result'
        }
        @{ Name = 'genskill -strict (RequiresReplace attributes say "Forces replacement.")'; Args = @('run', './tools/genskill', '-strict')
            Hint = 'each attribute listed above has a RequiresReplace plan modifier but no replacement sentence; add "Forces replacement." to its MarkdownDescription in internal/provider/<type>_resource.go'
        }
        @{ Name = 'tfplugindocs validate'; Args = @('run', $TfPluginDocs, 'validate', '--provider-name', 'docker') }
    )
    foreach ($s in $steps) {
        Write-Host ">> $Go $($s.Args -join ' ')" -ForegroundColor Cyan
        & $Go @($s.Args)
        if ($LASTEXITCODE -ne 0) {
            $msg = "$($s.Name) failed (exit $LASTEXITCODE)"
            if ($s.ContainsKey('Hint')) { $msg += ": $($s.Hint)" }
            $failed.Add($msg)
        }
    }

    if ($failed.Count -gt 0) {
        Write-Host ''
        Write-Host "check: $($failed.Count) step(s) failed:" -ForegroundColor Red
        $failed | ForEach-Object { Write-Host "  - $_" -ForegroundColor Red }
        throw "check failed: $($failed -join '; ')"
    }
    Write-Host 'check: all steps passed' -ForegroundColor Green
}

Push-Location $PSScriptRoot
try {
    foreach ($t in $Task) { Invoke-Task $t }
}
finally {
    Pop-Location
}
