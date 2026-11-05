#Requires -Version 7.0
<#
.SYNOPSIS
  Bring up the dind swarm harness (manager + 2 workers) and write test/harness/.env.

.DESCRIPTION
  PowerShell equivalent of harness-up.sh. Uses the same docker-compose.yml and
  writes test/harness/.env in the same `export NAME=value` format (so
  `source test/harness/.env` still works from bash). The variables are also
  set in the current PowerShell session ($env:), so dot-sourcing is not
  required. The docker CLI is used here, in the test harness only; the
  provider never shells out to it.
#>
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$HarnessDir  = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$ComposeFile = Join-Path $HarnessDir 'docker-compose.yml'
$EnvFile     = Join-Path $HarnessDir '.env'

# Same values, same order as harness-up.sh.
$HarnessEnv = [ordered]@{
    DOCKER_HOST         = 'tcp://127.0.0.1:12375'
    DOCKER_WORKER1_HOST = 'tcp://127.0.0.1:12376'
    DOCKER_WORKER2_HOST = 'tcp://127.0.0.1:12377'
    E2E_MANAGER_ADDR    = 'manager:2377'
    E2E_ADVERTISE_ADDR  = 'eth0'
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'harness-up: the docker CLI is not on PATH. Install Docker Desktop (or Docker Engine) first.'
}

# Fail fast if the HOST daemon (the one that runs the dind containers) is down.
# Check with the ambient context, not a stale DOCKER_HOST left by an earlier run.
$savedDockerHost = $env:DOCKER_HOST
if ($savedDockerHost -eq $HarnessEnv.DOCKER_HOST) { $env:DOCKER_HOST = $null }
$infoOut = & docker info --format '{{.ServerVersion}}' 2>&1
$infoExit = $LASTEXITCODE
if ($infoExit -ne 0) {
    $env:DOCKER_HOST = $savedDockerHost
    $msg = ($infoOut | Out-String).Trim()
    throw "harness-up: the Docker daemon is not reachable (docker info exit $infoExit). Start Docker Desktop / dockerd and retry.`n$msg"
}
Write-Host "Host Docker daemon $(($infoOut | Out-String).Trim()) reachable."

& docker compose -f $ComposeFile up -d --wait
if ($LASTEXITCODE -ne 0) {
    throw "harness-up: 'docker compose up -d --wait' failed (exit $LASTEXITCODE)."
}

# --wait relies on the healthchecks; additionally confirm each published
# endpoint answers from the host side (port publishing can lag).
function Wait-HarnessDaemon {
    param([string]$Name, [string]$DockerHost)
    for ($i = 1; $i -le 60; $i++) {
        & docker -H $DockerHost info *> $null
        if ($LASTEXITCODE -eq 0) {
            Write-Host "$Name ready at $DockerHost"
            return
        }
        Start-Sleep -Seconds 2
    }
    & docker compose -f $ComposeFile logs --tail=50 $Name 2>&1 | Write-Host
    throw "harness-up: $Name did not answer at $DockerHost within 120s"
}

Wait-HarnessDaemon -Name manager -DockerHost $HarnessEnv.DOCKER_HOST
Wait-HarnessDaemon -Name worker1 -DockerHost $HarnessEnv.DOCKER_WORKER1_HOST
Wait-HarnessDaemon -Name worker2 -DockerHost $HarnessEnv.DOCKER_WORKER2_HOST

# LF line endings and no BOM so bash `source` reads it cleanly.
$lines = foreach ($k in $HarnessEnv.Keys) { "export $k=$($HarnessEnv[$k])" }
[System.IO.File]::WriteAllText($EnvFile, (($lines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))

foreach ($k in $HarnessEnv.Keys) {
    Set-Item -Path "Env:$k" -Value $HarnessEnv[$k]
}

Write-Host "Wrote ${EnvFile}:"
Get-Content $EnvFile | Write-Host
Write-Host 'Harness variables are also set in this PowerShell session.'
