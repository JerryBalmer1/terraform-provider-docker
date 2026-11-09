#Requires -Version 7.0
<#
.SYNOPSIS
  Turn the running harness into the swarm the -SwarmInit acceptance pass
  expects: manager initialised, worker4 joined and ready, CSI fixture enabled.

.DESCRIPTION
  PowerShell equivalent of harness-swarm-init.sh; Invoke-E2E.ps1 -SwarmInit
  runs it after harness-up. It reads the harness variables from the session
  or, when they are missing, from test/harness/.env.

  1. `docker swarm init --advertise-addr <E2E_ADVERTISE_ADDR>` on the harness
     manager (never on the host daemon).
  2. Joins worker4 (DOCKER_WORKER4_HOST) as a worker and waits until the
     manager reports it Ready. worker1-3 stay outside the swarm: the
     docker_swarm_node and TestAccE2E* tests join and remove them themselves.
  3. Creates and enables the tfacc CSI plugin (TF_ACC_CSI_PLUGIN from
     TF_ACC_CSI_PLUGIN_DIR, built by harness-up) on the manager, for the Swarm
     cluster volume test. Best effort: a failure is a warning and that test
     skips.
  The docker CLI is used here, in the test harness only.
#>
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$HarnessDir = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$EnvFile    = Join-Path $HarnessDir '.env'

if (Test-Path -LiteralPath $EnvFile) {
    foreach ($line in Get-Content -LiteralPath $EnvFile) {
        if ($line -match '^\s*export\s+([A-Za-z_][A-Za-z0-9_]*)=(.*)$' -and -not (Test-Path "Env:$($Matches[1])")) {
            Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2].Trim().Trim('"', "'")
        }
    }
}
$mgr = $env:DOCKER_HOST
if ($mgr -ne 'tcp://127.0.0.1:12375') {
    throw "harness-swarm-init: refusing to run: DOCKER_HOST is '$mgr', not the harness manager"
}
$adv = if ($env:E2E_ADVERTISE_ADDR) { $env:E2E_ADVERTISE_ADDR } else { 'eth0' }
$mgrAddr = if ($env:E2E_MANAGER_ADDR) { $env:E2E_MANAGER_ADDR } else { 'manager:2377' }
$worker = if ($env:DOCKER_WORKER4_HOST) { $env:DOCKER_WORKER4_HOST } else { 'tcp://127.0.0.1:12381' }

& docker -H $mgr swarm init --advertise-addr $adv | Out-Host
if ($LASTEXITCODE -ne 0) { throw "harness-swarm-init: swarm init on the harness manager failed (exit $LASTEXITCODE)" }

$token = (& docker -H $mgr swarm join-token -q worker | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or -not $token) { throw 'harness-swarm-init: cannot read the worker join token' }
& docker -H $worker swarm join --token $token --advertise-addr $adv $mgrAddr | Out-Host
if ($LASTEXITCODE -ne 0) { throw "harness-swarm-init: worker4 ($worker) could not join $mgrAddr (exit $LASTEXITCODE)" }
$ready = $false
for ($i = 1; $i -le 60; $i++) {
    $nodes = & docker -H $mgr node ls --filter role=worker --format '{{.Hostname}} {{.Status}} {{.Availability}}' 2>$null
    if ($nodes -match '^worker4 Ready Active$') { $ready = $true; break }
    Start-Sleep -Seconds 2
}
if (-not $ready) { throw 'harness-swarm-init: worker4 did not become a Ready, Active worker within 120s' }
Write-Host 'worker4 joined the harness swarm as a worker (Ready, Active).'

if ($env:TF_ACC_CSI_PLUGIN -and $env:TF_ACC_CSI_PLUGIN_DIR -and (Test-Path -LiteralPath $env:TF_ACC_CSI_PLUGIN_DIR)) {
    $out = & docker -H $mgr plugin create $env:TF_ACC_CSI_PLUGIN $env:TF_ACC_CSI_PLUGIN_DIR 2>&1
    if ($LASTEXITCODE -eq 0) { $out = & docker -H $mgr plugin enable $env:TF_ACC_CSI_PLUGIN 2>&1 }
    if ($LASTEXITCODE -eq 0) {
        Write-Host "CSI plugin $($env:TF_ACC_CSI_PLUGIN) enabled on the manager."
    } else {
        Write-Warning "harness-swarm-init: CSI plugin $($env:TF_ACC_CSI_PLUGIN) not enabled; TestAccObjClusterVolume will skip. $($out | Out-String)"
    }
} else {
    Write-Warning 'harness-swarm-init: TF_ACC_CSI_PLUGIN / TF_ACC_CSI_PLUGIN_DIR not set (harness-up could not build the plugin fixtures); TestAccObjClusterVolume will skip.'
}
