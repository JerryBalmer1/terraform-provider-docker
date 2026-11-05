#Requires -Version 7.0
<#
.SYNOPSIS
  Tear down the dind swarm harness, its volumes, and test/harness/.env.

.DESCRIPTION
  PowerShell equivalent of harness-down.sh. Also clears the harness variables
  from the current session. Safe to run repeatedly. .env is removed and the
  variables are cleared even if `docker compose down` fails (for example when
  the Docker daemon is not running); the script then exits non-zero.
#>
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$HarnessDir  = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$ComposeFile = Join-Path $HarnessDir 'docker-compose.yml'
$EnvFile     = Join-Path $HarnessDir '.env'
$HarnessVars = 'DOCKER_HOST', 'DOCKER_WORKER1_HOST', 'DOCKER_WORKER2_HOST', 'E2E_MANAGER_ADDR', 'E2E_ADVERTISE_ADDR'

# compose must talk to the HOST daemon, not the harness manager.
if ($env:DOCKER_HOST -eq 'tcp://127.0.0.1:12375') { $env:DOCKER_HOST = $null }

$composeExit = 0
try {
    & docker compose -f $ComposeFile down -v --remove-orphans
    $composeExit = $LASTEXITCODE
} catch {
    Write-Warning "harness-down: $($_.Exception.Message)"
    $composeExit = 1
} finally {
    Remove-Item -LiteralPath $EnvFile -Force -ErrorAction SilentlyContinue
    foreach ($v in $HarnessVars) {
        Remove-Item -Path "Env:$v" -ErrorAction SilentlyContinue
    }
}

if ($composeExit -ne 0) {
    throw "harness-down: 'docker compose down' failed (exit $composeExit); .env removed and harness variables cleared anyway."
}
Write-Host 'Harness removed.'
