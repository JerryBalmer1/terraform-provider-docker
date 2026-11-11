<#
.SYNOPSIS
Applies every generated pairwise config of the given types on the harness (Pass 6b Lane W).

.DESCRIPTION
Run inside the dind harness through test/harness/Invoke-E2E.ps1 -Script (the PairwiseTest task does this): it
refuses to run unless DOCKER_HOST is the harness manager. For each config directory <OutDir>/<type>/<nn>:
terraform init, apply, terraform show -json (state.json), `pairwise -compare` (compare.json: the requested values
vs the daemon's inspect through the contract field maps), a second plan with -detailed-exitcode (plan.json and
`pairwise -plan-summary` when it is not empty), destroy. The outcome of each step goes to run.json; the terraform
output of each step to <step>.log in the config directory. Exit code 0 when every config ran (findings are judged
by `pairwise -report`, not here), 1 on a setup error or when any terraform output carries "Outputs contract value
dropped" (listed in <OutDir>/contract-drops.txt).

.PARAMETER OutDir
The `pairwise -gen -out` directory.
.PARAMETER Types
Comma-separated resource types to run.
.PARAMETER Pairwise
The pairwise tool binary.
.PARAMETER CliConfig
Terraform CLI config file that installs JerryBalmer1/docker from the local filesystem mirror (PairwiseTest writes it).
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$OutDir,
    [Parameter(Mandatory)][string]$Types,
    [Parameter(Mandatory)][string]$Pairwise,
    [Parameter(Mandatory)][string]$CliConfig
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($env:DOCKER_HOST -ne 'tcp://127.0.0.1:12375') {
    Write-Host "pairwise run: refusing to run: DOCKER_HOST is '$env:DOCKER_HOST', not the harness manager"
    exit 1
}
$tf = if ($env:TF_ACC_TERRAFORM_PATH -and (Test-Path -LiteralPath $env:TF_ACC_TERRAFORM_PATH)) { $env:TF_ACC_TERRAFORM_PATH } else { 'terraform' }
$env:TF_CLI_CONFIG_FILE = $CliConfig
$env:TF_IN_AUTOMATION = '1'
$env:TF_INPUT = '0'
$env:CHECKPOINT_DISABLE = '1'
# Images come from the harness registry copy (manager:12380/busybox:1.36) when harness-up seeded them.
$env:TF_VAR_pw_registry = if ($env:TF_ACC_REGISTRY_NODES) { ($env:TF_ACC_REGISTRY_NODES.TrimEnd('/')) + '/' } else { '' }

function Invoke-Tf {
    param([string]$Dir, [string]$Step, [string[]]$TfArgs)
    $log = Join-Path $Dir "$Step.log"
    $ErrorActionPreference = 'Continue'
    & $tf "-chdir=$Dir" @TfArgs *> $log
    $code = $LASTEXITCODE
    $ErrorActionPreference = 'Stop'
    return $code
}

function Get-ErrorText {
    param([string]$Dir, [string]$Step)
    $log = Join-Path $Dir "$Step.log"
    if (-not (Test-Path -LiteralPath $log)) { return '' }
    $lines = @(Get-Content -LiteralPath $log | Where-Object { $_ -match '\S' })
    $start = 0
    for ($i = 0; $i -lt $lines.Count; $i++) { if ($lines[$i] -match 'Error') { $start = $i; break } }
    return (($lines | Select-Object -Skip $start -First 25) -join "`n")
}

# Lane S's outputs contract writes this text when it drops a value; any terraform output carrying it fails the run.
$contractDrop = 'Outputs contract value dropped'
$drops = [System.Collections.Generic.List[string]]::new()

$tfVersion = (& $tf version | Select-Object -First 1)
Write-Host "pairwise run: $tfVersion, DOCKER_HOST=$env:DOCKER_HOST, registry prefix '$env:TF_VAR_pw_registry'"
$total = 0
foreach ($type in ($Types -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })) {
    $typeDir = Join-Path $OutDir $type
    if (-not (Test-Path -LiteralPath $typeDir)) { Write-Host "pairwise run: no configs for $type"; exit 1 }
    foreach ($cfg in Get-ChildItem -LiteralPath $typeDir -Directory | Sort-Object Name) {
        $dir = $cfg.FullName
        $total++
        foreach ($f in 'run.json', 'compare.json', 'plan-summary.json', 'plan.json', 'state.json', 'terraform.tfstate', 'plan.tfplan') {
            Remove-Item -LiteralPath (Join-Path $dir $f) -ErrorAction SilentlyContinue
        }
        $run = [ordered]@{ init = $false; applied = $false; plan_exit_code = -1; destroyed = $false }
        $sw = [Diagnostics.Stopwatch]::StartNew()
        $run.init = (Invoke-Tf $dir 'init' @('init', '-input=false', '-no-color')) -eq 0
        $applyCode = Invoke-Tf $dir 'apply' @('apply', '-auto-approve', '-input=false', '-no-color')
        $run.applied = $applyCode -eq 0
        $run.apply_seconds = [int]$sw.Elapsed.TotalSeconds
        if (-not $run.applied) {
            $run.apply_error = Get-ErrorText $dir 'apply'
        } else {
            $ErrorActionPreference = 'Continue'
            & $tf "-chdir=$dir" show -json -no-color 2> (Join-Path $dir 'show.log') | Set-Content -LiteralPath (Join-Path $dir 'state.json') -Encoding utf8
            $cmpOut = & $Pairwise -compare -dir $dir -host $env:DOCKER_HOST 2>&1
            if ($LASTEXITCODE -ne 0) { $run.compare_error = ($cmpOut | Out-String).Trim() }
            $ErrorActionPreference = 'Stop'
            $run.plan_exit_code = Invoke-Tf $dir 'plan' @('plan', '-detailed-exitcode', '-input=false', '-no-color', '-out=plan.tfplan')
            if ($run.plan_exit_code -eq 2) {
                $ErrorActionPreference = 'Continue'
                & $tf "-chdir=$dir" show -json -no-color plan.tfplan 2>> (Join-Path $dir 'show.log') | Set-Content -LiteralPath (Join-Path $dir 'plan.json') -Encoding utf8
                $null = & $Pairwise -plan-summary -dir $dir 2>&1
                $ErrorActionPreference = 'Stop'
            } elseif ($run.plan_exit_code -ne 0) {
                $run.plan_error = Get-ErrorText $dir 'plan'
            }
        }
        $destroyCode = Invoke-Tf $dir 'destroy' @('destroy', '-auto-approve', '-input=false', '-no-color')
        $run.destroyed = $destroyCode -eq 0
        if (-not $run.destroyed) { $run.destroy_error = Get-ErrorText $dir 'destroy' }
        foreach ($l in Get-ChildItem -LiteralPath $dir -Filter '*.log' -File) {
            foreach ($hit in @(Select-String -LiteralPath $l.FullName -SimpleMatch $contractDrop)) { $drops.Add("$type/$($cfg.Name)/$($l.Name): $($hit.Line.Trim())") }
        }
        $run.contract_dropped = @($drops | Where-Object { $_.StartsWith("$type/$($cfg.Name)/") }).Count
        $run | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $dir 'run.json') -Encoding utf8
        $mm = 0
        $cmpFile = Join-Path $dir 'compare.json'
        if (Test-Path -LiteralPath $cmpFile) { $mm = @((Get-Content -LiteralPath $cmpFile -Raw | ConvertFrom-Json).mismatches).Count }
        Write-Host ("pairwise run: {0}/{1} applied={2} mismatches={3} plan={4} destroyed={5} ({6}s)" -f $type, $cfg.Name, $run.applied, $mm, $run.plan_exit_code, $run.destroyed, [int]$sw.Elapsed.TotalSeconds)
    }
}
Write-Host "pairwise run: $total configs done"
$dropFile = Join-Path $OutDir 'contract-drops.txt'
if ($drops.Count) {
    Set-Content -LiteralPath $dropFile -Value $drops
    Write-Host "pairwise run: FAILED: $($drops.Count) terraform output line(s) with '$contractDrop' (list: $dropFile)"
    $drops | Select-Object -First 10 | ForEach-Object { Write-Host "  $_" }
    exit 1
}
Remove-Item -LiteralPath $dropFile -ErrorAction SilentlyContinue
exit 0
