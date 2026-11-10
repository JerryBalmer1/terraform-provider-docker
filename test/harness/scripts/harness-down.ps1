#Requires -Version 7.0
<#
.SYNOPSIS
  Tear down the dind harness, its volumes, test/harness/.env, test/harness/tls/
  and test/harness/.plugins/.

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
$HarnessVars = 'DOCKER_HOST', 'DOCKER_WORKER1_HOST', 'DOCKER_WORKER2_HOST', 'DOCKER_WORKER3_HOST', 'DOCKER_WORKER4_HOST', 'TF_ACC_CONTAINERD_HOST',
    'E2E_MANAGER_ADDR', 'E2E_ADVERTISE_ADDR',
    'TF_ACC_REGISTRY', 'TF_ACC_REGISTRY_NODES', 'TF_ACC_TLS_HOST', 'TF_ACC_TLS_CA', 'TF_ACC_TLS_CERT', 'TF_ACC_TLS_KEY',
    'TF_ACC_TLS_CERT_PATH', 'TF_ACC_GPU_NODE_LABEL', 'TF_ACC_HARNESS_COMPOSE', 'TF_ACC_COMPOSE_CLI', 'TF_ACC_TERRAFORM_VERSION', 'TF_ACC_TERRAFORM_PATH',
    'TF_ACC_SEEDED_IMAGES', 'TF_ACC_PLUGIN', 'TF_ACC_PLUGIN_DIR', 'TF_ACC_CSI_PLUGIN', 'TF_ACC_CSI_PLUGIN_DIR',
    'TPD_ACC_INTERNET', 'TF_ACC_SKIP_INTERNET'
$TlsDir      = Join-Path $HarnessDir 'tls'
$PluginsDir  = Join-Path $HarnessDir '.plugins'

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
    Remove-Item -LiteralPath $TlsDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $PluginsDir -Recurse -Force -ErrorAction SilentlyContinue
    foreach ($v in $HarnessVars) {
        Remove-Item -Path "Env:$v" -ErrorAction SilentlyContinue
    }
}

if ($composeExit -ne 0) {
    throw "harness-down: 'docker compose down' failed (exit $composeExit); .env and tls/ removed and harness variables cleared anyway."
}
Write-Host 'Harness removed.'
