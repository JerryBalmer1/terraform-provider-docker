#Requires -Version 7.0
<#
.SYNOPSIS
  Run the provider's acceptance tests against the dind swarm harness.

.DESCRIPTION
  harness-up -> TF_ACC=1 + hosts from test/harness/.env ->
  `go test ./internal/provider/ -run <Run> -count=1 -v -timeout <Timeout>`
  from the repository root, full output tee'd to -Log -> harness-down (in a
  finally block, unless -KeepUp). Exits with go test's exit code (1 if the
  harness could not be brought up).

.PARAMETER Run
  go test -run regex. Default 'TestAccE2E' (the image -> service chain).
  Use 'TestAcc' for every acceptance test.

.PARAMETER Timeout
  go test -timeout. Default '30m'.

.PARAMETER Log
  File receiving the full test output. Relative paths are resolved against
  the repository root. Default test/harness/last-e2e.log.

.PARAMETER SwarmInit
  After harness-up, run `docker swarm init --advertise-addr <E2E_ADVERTISE_ADDR>` on the
  harness manager (never on the host daemon). Needed by the tests that expect
  DOCKER_HOST to already be a swarm manager (service, stack, secret, config,
  swarm_node, node/nodes/swarm_tokens data sources). Leave it off for
  TestAccE2E and TestAccSwarmInit, which need a manager outside any swarm.

.PARAMETER KeepUp
  Leave the harness running afterwards (tear it down later with
  test/harness/scripts/harness-down.ps1).

.EXAMPLE
  pwsh -NoProfile -File test/harness/Invoke-E2E.ps1
.EXAMPLE
  pwsh -NoProfile -File test/harness/Invoke-E2E.ps1 -Run TestAcc -Timeout 60m -Log test/harness/last-acc.log
.EXAMPLE
  pwsh -NoProfile -File test/harness/Invoke-E2E.ps1 -SwarmInit -Run 'TestAcc(Service|Stack|Secret|DockerConfig|SwarmTokens|Node_|Nodes_|SwarmNode)' -Log test/harness/last-acc-swarm.log
#>
[CmdletBinding()]
param(
    [string]$Run = 'TestAccE2E',
    [string]$Timeout = '30m',
    [string]$Log = 'test/harness/last-e2e.log',
    [switch]$KeepUp,
    [switch]$SwarmInit
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$HarnessDir = $PSScriptRoot
$RepoRoot   = (Resolve-Path (Join-Path $HarnessDir '../..')).Path
$EnvFile    = Join-Path $HarnessDir '.env'
$UpScript   = Join-Path $HarnessDir 'scripts/harness-up.ps1'
$DownScript = Join-Path $HarnessDir 'scripts/harness-down.ps1'
$LogPath    = if ([System.IO.Path]::IsPathRooted($Log)) { $Log } else { Join-Path $RepoRoot $Log }

# Pre-flight: fail fast, before touching compose or .env, if the host daemon is down.
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Error 'Invoke-E2E: the docker CLI is not on PATH.' -ErrorAction Continue
    exit 1
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error 'Invoke-E2E: go is not on PATH.' -ErrorAction Continue
    exit 1
}
$savedDockerHost = $env:DOCKER_HOST
if ($savedDockerHost -eq 'tcp://127.0.0.1:12375') { $env:DOCKER_HOST = $null }  # stale harness value
$null = & docker info --format '{{.ServerVersion}}' 2>&1
if ($LASTEXITCODE -ne 0) {
    $env:DOCKER_HOST = $savedDockerHost
    Write-Host 'Invoke-E2E: the Docker daemon is not reachable (`docker info` failed). Start Docker Desktop / dockerd and retry. Nothing was started.' -ForegroundColor Red
    exit 1
}

$testExit = 1
$upAttempted = $false
try {
    $upAttempted = $true
    & $UpScript

    # Load hosts from .env (export NAME=value lines) into this process.
    if (-not (Test-Path -LiteralPath $EnvFile)) {
        throw "Invoke-E2E: harness-up did not write $EnvFile"
    }
    foreach ($line in Get-Content -LiteralPath $EnvFile) {
        if ($line -match '^\s*export\s+([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
            Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2].Trim().Trim('"', "'")
        }
    }
    # Safety: acceptance tests create containers and swarms; only ever run
    # them against the disposable harness manager, never the host daemon.
    if ($env:DOCKER_HOST -ne 'tcp://127.0.0.1:12375') {
        throw "Invoke-E2E: refusing to run: DOCKER_HOST is '$env:DOCKER_HOST', not the harness manager"
    }
    if ($SwarmInit) {
        $adv = if ($env:E2E_ADVERTISE_ADDR) { $env:E2E_ADVERTISE_ADDR } else { 'eth0' }
        & docker -H $env:DOCKER_HOST swarm init --advertise-addr $adv | Out-Host
        if ($LASTEXITCODE -ne 0) { throw "Invoke-E2E: swarm init on the harness manager failed (exit $LASTEXITCODE)" }
    }
    $env:TF_ACC = '1'

    $logDir = Split-Path -Parent $LogPath
    if ($logDir -and -not (Test-Path -LiteralPath $logDir)) { New-Item -ItemType Directory -Path $logDir -Force | Out-Null }

    Write-Host "Invoke-E2E: go test ./internal/provider/ -run $Run -count=1 -v -timeout $Timeout  (log: $LogPath)"
    Push-Location $RepoRoot
    try {
        # go writes to stderr too; do not let that trip ErrorActionPreference=Stop.
        $ErrorActionPreference = 'Continue'
        & go test ./internal/provider/ -run $Run -count=1 -v -timeout $Timeout 2>&1 |
            ForEach-Object { "$_" } |
            Tee-Object -FilePath $LogPath
        $testExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = 'Stop'
        Pop-Location
    }
    Write-Host "Invoke-E2E: go test exit code $testExit"
} catch {
    Write-Host "Invoke-E2E: $($_.Exception.Message)" -ForegroundColor Red
    $testExit = 1
} finally {
    if ($upAttempted -and -not $KeepUp) {
        try {
            & $DownScript
        } catch {
            Write-Warning "Invoke-E2E: harness-down: $($_.Exception.Message)"
        }
    } elseif ($KeepUp) {
        Write-Host "Invoke-E2E: -KeepUp set; harness left running. Tear down with: pwsh -NoProfile -File `"$DownScript`""
    }
}
exit $testExit
