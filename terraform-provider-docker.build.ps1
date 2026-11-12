<#
.SYNOPSIS
    Invoke-Build script for terraform-provider-docker.

.DESCRIPTION
    Run from the repository root with Invoke-Build (https://github.com/nightroman/Invoke-Build),
    which picks up this *.build.ps1 file automatically:

        Invoke-Build ?                  # list tasks with their synopses
        Invoke-Build                    # default task: Build, Vet, Test, GenSkillCheck, InventoryCheck, InventoryValidate, RegistryCheck, ContractCheck, DocCountsCheck, FieldCoverage, ExampleCoverage, ManualTaskCheck
        Invoke-Build Test
        Invoke-Build Build, Vet, GenSkillCheck
        Invoke-Build Compose2Stack -In docker-compose.yml -Out stack.tf -Mode swarm
        Invoke-Build PreRelease         # every pre-release gate, one summary table (build/prerelease.tasks.ps1)
        Invoke-Build DriftCheck         # upstream drift vs reference/SOURCES.md
        Invoke-Build ReleaseCheck       # release matrix + cross-compiles; Release: local goreleaser snapshot
        Invoke-Build ExampleTests       # every examples/ folder on the dind harness (build/examples.tasks.ps1)

    Manual tasks for a person at the keyboard (names start with '_', listed first by Invoke-Build ?; no gate
    may depend on them, ManualTaskCheck fails otherwise). Scenarios live in test/__manual/<name>/:

        Invoke-Build _List                          # scenarios and what each shows
        Invoke-Build _Up [-Swarm]                   # harness up (and swarm init)
        Invoke-Build _Init -Scenario container_full # then _Plan, _Apply, _Destroy with the same -Scenario
                                                    # (_Apply / _Destroy run unattended; add -Prompt to confirm)
        Invoke-Build _Down                          # harness down

    Install once: Install-Module InvokeBuild -Scope CurrentUser

    Tools are resolved from PATH when the script loads. A task failing with
    "Missing variable 'GoExe'" (or GitExe, GofmtExe, PythonExe) means that tool is not on PATH.

    This script is the only build entry point: CI (.github/workflows/ci.yml) installs InvokeBuild and runs
    these same tasks under pwsh on Linux, in this order: the default task, Check, ParityCheck + GuideCheck,
    ReleaseCheck, ApiWatchCheck, then E2E. There is no Makefile.

.PARAMETER Go
    Go command (default $env:GO, else 'go').

.PARAMETER Python
    Python 3 command for the Inventory tasks (default 'python', falling back to 'python3' when 'python' is not on PATH).

.PARAMETER In
    Compose2Stack: compose file to read, relative to the repo root (default docker-compose.yml).

.PARAMETER Out
    Compose2Stack: .tf file to write, relative to the repo root (default stack.tf).

.PARAMETER Mode
    Compose2Stack: conversion mode, swarm or local (default swarm).

.PARAMETER Scenario
    Manual tasks (_Init, _Plan, _Apply, _Destroy): the folder name under test/__manual (Invoke-Build _List).

.PARAMETER Swarm
    _Up: also run test/harness/scripts/harness-swarm-init.ps1 (the manager becomes a swarm manager, worker4 joins).

.PARAMETER Invoke
    _Apply: run only this action (terraform apply -invoke=action.<type>.<name>).

.PARAMETER Prompt
    _Apply, _Destroy: let Terraform ask for confirmation. Without it they run unattended (_Apply: terraform plan -out,
    a one-line summary of the plan, then terraform apply of that saved plan, so the full plan is not printed twice;
    _Destroy: terraform destroy -auto-approve).
#>
param(
    [string]$Go = $(if ($env:GO) { $env:GO } else { 'go' }),
    [string]$Python = 'python',
    [string]$In = 'docker-compose.yml',
    [string]$Out = 'stack.tf',
    [ValidateSet('swarm', 'local')]
    [string]$Mode = 'swarm',
    [string]$Scenario = '',
    [switch]$Swarm,
    [string]$Invoke = '',
    [switch]$Prompt,
    # ExampleTests / ExampleValidate: comma-separated wildcards on the example path (resources/docker_tag, actions/*)
    # or its folder name (docker_tag); overrides TPD_EXAMPLE_INCLUDE.
    [string]$Filter = '',
    # Gate ledger (build/ledger.tasks.ps1): ignore test/harness/.ledger and run every unit of Test, TestAcc,
    # ExampleTests and PairwiseTest (as TPD_LEDGER=off does; PreRelease always passes it).
    [switch]$Full
)

if ($MyInvocation.ScriptName -notlike '*Invoke-Build.ps1') {
    throw 'Run this file through Invoke-Build from the repository root, e.g. Invoke-Build Test (Invoke-Build ? lists tasks).'
}

Set-StrictMode -Version Latest

# ---------------------------------------------------------------------------- tools and paths

function Resolve-Tool([string]$Name) {
    $cmd = Get-Command $Name -CommandType Application -ErrorAction Ignore | Select-Object -First 1
    if ($cmd) { $cmd.Source }
}
$GoExe = Resolve-Tool $Go
$GofmtExe = Resolve-Tool 'gofmt'
$GitExe = Resolve-Tool 'git'
$PythonExe = Resolve-Tool $Python
# Linux runners (GitHub ubuntu images) ship python3 without a `python` alias.
if (-not $PythonExe -and $Python -eq 'python') { $PythonExe = Resolve-Tool 'python3' }

$TfPluginDocs = 'github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs'
$Harness = Join-Path $BuildRoot 'test/harness/Invoke-E2E.ps1'
# Every acceptance test that needs DOCKER_HOST to be a swarm manager: -Run of the swarm pass, -Skip of the plain one.
$SwarmAccRegex = 'TestAcc(OwnerLabelSwarm|Service|Stack|Secret|DockerConfig|SwarmTokens|SwarmDataSources|Node_|Nodes_|NodeResource|SwarmNode|ComposeSwarm|ListSecret|P3SwarmList|ObjClusterVolume|Action(SwarmRotateTokens|NodeDrainActivate|ServiceScaleForceUpdateRollback|NodePromoteDemote|SwarmRotateCA|SwarmUnlockKey))'

$RefDir = Join-Path $BuildRoot 'reference'
$InventoryFiles = 'reference/inventory.json', 'reference/INVENTORY.md'
# Scratch of the build tasks (git-ignored; never %TEMP%): ReferenceVerify's cache, self-test trees, cross-compiles,
# manual scenario working copies, the provider filesystem mirror and example copies.
$TpdWork = Join-Path $BuildRoot 'test/harness/.work'
$RefVerifyCache = Join-Path $TpdWork 'reference-verify'

# What ReferenceVerify checks. Every file under reference/upstream must be covered by exactly one entry.
# Repo trees: <Local>/<path> must equal blob <path> of <Url> at <Sha>, minus <Exclude>.
# Files: single local files mapped to a different upstream path.
# Sha256: files that came from a URL, not a git repo.
# Skip: locally generated content with no upstream.
# Each Sha must also appear in reference/SOURCES.md, so the two cannot drift apart.
$ReferenceSources = @(
    @{ Name = 'moby'; Url = 'https://github.com/moby/moby'; Sha = '14ebc602124e280ef4ae3e1a8e688dac1255baa6'; Local = 'engine-api'; Exclude = @('docs.docker.com-v1.56.yaml') }
    @{ Name = 'compose-spec'; Url = 'https://github.com/compose-spec/compose-spec'; Sha = '3a4a6995e4a971f6173dff9130cb3e38e6738651'; Local = 'compose-spec'; Exclude = @('compose-go-embedded.json') }
    @{ Name = 'compose-go'; Url = 'https://github.com/compose-spec/compose-go'; Sha = 'ddbe099a5404149d75fe774b30de7bd0befd3bd3'; Files = @{ 'compose-spec/compose-go-embedded.json' = 'schema/compose-spec.json' } }
    @{ Name = 'compose'; Url = 'https://github.com/docker/compose'; Sha = 'fb1fd129f86828359335477f82821525201d4a8c'; Local = 'compose-cli' }
    @{ Name = 'docker-cli'; Url = 'https://github.com/docker/cli'; Sha = '5457199aa57c45e990c59c89cab5f02b817238d7'; Local = 'docker-cli' }
    @{ Name = 'buildkit'; Url = 'https://github.com/moby/buildkit'; Sha = 'f55949dea4f03bb9d85b73e9afc9a609291289db'; Local = 'dockerfile' }
    @{ Name = 'buildx'; Url = 'https://github.com/docker/buildx'; Sha = '3c4cbd35852fc1a476923627a780a7836c28b654'; Local = 'buildx' }
)
$ReferenceSha256 = @{
    'engine-api/docs.docker.com-v1.56.yaml' = '00ccd7d2e6e0b7a943ac0067007d181d723b2513ef1f6af3b6b54145314a6fa7'
    'hub/docs.docker.com-hub-latest.yaml'   = '7c280db399f81e7eaaf40c86b7cdfd9317c819b7589353e3a26e78d6a5a8d373'
}
$ReferenceSkip = @('.gitattributes', 'help/')

# Runs go test with TF_ACC removed, restoring it afterwards.
function Invoke-GoTestNoAcc([string[]]$Packages) {
    $saved = $env:TF_ACC
    Remove-Item Env:TF_ACC -ErrorAction Ignore
    try { exec { & $GoExe test @Packages -short -count=1 -timeout 10m } }
    finally { if ($null -ne $saved) { $env:TF_ACC = $saved } }
}

# blob SHA by path for <Url> at <Sha>, from a shallow treeless fetch cached under $RefVerifyCache.
function Get-UpstreamTree([hashtable]$Src) {
    $treeFile = Join-Path $RefVerifyCache "$($Src.Name)-$($Src.Sha).tree"
    if (-not (Test-Path -LiteralPath $treeFile)) {
        $repo = Join-Path $RefVerifyCache $Src.Name
        if (-not (Test-Path -LiteralPath (Join-Path $repo '.git'))) {
            $null = exec { & $GitExe -c core.autocrlf=false init -q $repo }
            $null = exec { & $GitExe -C $repo remote add origin $Src.Url }
        }
        # Write-Host, not print: print can write to the output stream, which is this function's return value.
        Write-Host "fetching $($Src.Url) @ $($Src.Sha) (trees only)" -ForegroundColor Cyan
        $null = exec { & $GitExe -C $repo fetch -q --depth 1 --filter=blob:none origin $Src.Sha }
        $raw = exec { & $GitExe -C $repo ls-tree -r -z --full-tree $Src.Sha }
        [System.IO.File]::WriteAllText($treeFile, ($raw -join ''))
    }
    $map = @{}
    foreach ($e in ([System.IO.File]::ReadAllText($treeFile) -split "`0")) {
        if (-not $e) { continue }
        $meta, $path = $e -split "`t", 2
        $map[$path] = ($meta -split ' ')[2]
    }
    $map
}

# git blob SHAs of local files (raw bytes, no filters), in input order.
function Get-LocalBlobShas([string[]]$FullPaths) {
    if (-not $FullPaths) { return @() }
    @($FullPaths | & $GitExe hash-object --no-filters --stdin-paths)
    if ($LASTEXITCODE) { throw "git hash-object exited with code $LASTEXITCODE" }
}

# ---------------------------------------------------------------------------- manual tasks (a person at the keyboard)
# Defined first so `Invoke-Build ?` lists them at the top. They stream plain terraform output (no log capture) and are
# never part of a gate: ManualTaskCheck (default task) fails when the default task, PreRelease or CI reaches one.
# Each copies test/__manual/<Scenario> into test/harness/.work/manual/<Scenario> (git-ignored; state, .terraform and
# the lock file stay there, never in the repository or %TEMP%) and loads test/harness/.env. _Init builds the provider
# into a local filesystem mirror (test/harness/.work/manual/_mirror, TfDockerTest Initialize-TfProviderMirror) and runs
# a real terraform init from it (lock file written, checksum verified); _Plan, _Apply and _Destroy use what _Init
# installed. Terraform is the harness copy (TF_ACC_TERRAFORM_PATH from .env), else terraform on PATH. They refuse to
# run unless DOCKER_HOST is the harness manager.

$ManualDir = Join-Path $BuildRoot 'test/__manual'
$ManualStateRoot = Join-Path $TpdWork 'manual'
$ManualMirror = Join-Path $ManualStateRoot '_mirror'
$HarnessScripts = Join-Path $BuildRoot 'test/harness/scripts'

# Loads test/harness/.env into this process (export NAME=value lines), as Invoke-E2E.ps1 does.
function Import-HarnessEnv {
    $envFile = Join-Path $BuildRoot 'test/harness/.env'
    if (-not (Test-Path -LiteralPath $envFile)) { throw 'test/harness/.env is missing: run Invoke-Build _Up first.' }
    foreach ($line in [System.IO.File]::ReadAllLines($envFile)) {
        if ($line -match '^\s*export\s+([A-Za-z_][A-Za-z0-9_]*)=(.*)$') { Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2].Trim().Trim('"', "'") }
    }
    if ($env:DOCKER_HOST -ne 'tcp://127.0.0.1:12375') { throw "DOCKER_HOST is '$env:DOCKER_HOST', not the harness manager: refusing to run a manual scenario." }
}

# Prepares a manual scenario run and returns @{ Work; Terraform; Source }: harness env, the mirror CLI configuration
# (-Build: rebuild the provider mirror first, _Init only), a fresh copy of the scenario's files beside its kept state.
function Enter-ManualScenario([switch]$Build) {
    if (-not $Scenario) { throw 'Give -Scenario <name> (Invoke-Build _List shows them).' }
    $src = Join-Path $ManualDir $Scenario
    if (-not (Test-Path -LiteralPath (Join-Path $src 'main.tf'))) { throw "No scenario test/__manual/$Scenario (Invoke-Build _List shows them)." }
    Import-HarnessEnv
    Import-Module (Join-Path $BuildRoot 'test/TfDockerTest') -Force
    $work = Join-Path $ManualStateRoot $Scenario
    $cfg = Join-Path $ManualStateRoot 'mirror.tfrc'
    if ($Build) {
        $mirror = Initialize-TfProviderMirror -MirrorDir $ManualMirror -Force
        $null = New-TfMirrorConfig -ProvidersDir $mirror.Providers -Path $cfg
    } elseif (-not (Test-Path -LiteralPath (Join-Path $work '.terraform.lock.hcl')) -or -not (Test-Path -LiteralPath $cfg)) {
        throw "Scenario $Scenario is not initialised: run Invoke-Build _Init -Scenario $Scenario first."
    }
    $env:TF_CLI_CONFIG_FILE = $cfg
    $null = New-Item -ItemType Directory -Force $work
    Get-ChildItem -LiteralPath $src -Force | Where-Object { $_.Name -notin 'README.md', 'prepare.ps1' } | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $work -Recurse -Force
    }
    $tf = Get-TfTerraformPath
    # Write-Host, not print: print writes to the output stream, which is this function's return value.
    Write-Host -ForegroundColor Cyan "scenario $Scenario`: working copy $work (state kept there), provider mirror $ManualMirror, terraform $tf, DOCKER_HOST $env:DOCKER_HOST"
    @{ Work = $work; Terraform = $tf; Source = $src }
}

# Synopsis: List the manual scenarios (test/__manual/<name>) with the first line of each README.md.
task _List {
    foreach ($d in Get-ChildItem -LiteralPath $ManualDir -Directory | Sort-Object Name) {
        $readme = Join-Path $d.FullName 'README.md'
        $first = if (Test-Path -LiteralPath $readme) { ([System.IO.File]::ReadAllLines($readme) | Where-Object { $_.Trim() } | Select-Object -First 1) -replace '^#+\s*', '' } else { '(no README.md)' }
        Write-Build Cyan ('{0,-28} {1}' -f $d.Name, $first)
    }
    Write-Build Gray 'Invoke-Build _Up [-Swarm]; Invoke-Build _Init, _Plan, _Apply, _Destroy -Scenario <name>; Invoke-Build _Down'
}

# Synopsis: Bring the dind harness up (test/harness/scripts/harness-up.ps1); -Swarm also runs harness-swarm-init.ps1.
task _Up {
    & (Join-Path $HarnessScripts 'harness-up.ps1')
    if ($Swarm) { & (Join-Path $HarnessScripts 'harness-swarm-init.ps1') }
}

# Synopsis: terraform init for -Scenario <name> from a freshly built provider mirror (lock file rewritten for this build; runs the scenario's prepare.ps1 first when it has one and no state yet).
task _Init {
    $s = Enter-ManualScenario -Build
    $prepare = Join-Path $s.Source 'prepare.ps1'
    if ((Test-Path -LiteralPath $prepare) -and -not (Test-Path -LiteralPath (Join-Path $s.Work 'terraform.tfstate'))) {
        Write-Build Cyan "running $prepare"
        & $prepare -WorkDir $s.Work
    }
    # The mirror was just rebuilt under the same version: drop the old pin, so init records this build's checksums.
    Remove-Item -LiteralPath (Join-Path $s.Work '.terraform.lock.hcl'), (Join-Path $s.Work '.terraform/providers') -Recurse -Force -ErrorAction Ignore
    exec { & $s.Terraform "-chdir=$($s.Work)" init -input=false }
}

# Synopsis: terraform plan for -Scenario <name>.
task _Plan {
    $s = Enter-ManualScenario
    exec { & $s.Terraform "-chdir=$($s.Work)" plan }
}

# Synopsis: terraform apply for -Scenario <name>, unattended (saved plan, one summary line); -Prompt asks for approval; -Invoke action.<type>.<name> runs only that action.
task _Apply {
    $s = Enter-ManualScenario
    $inv = @(if ($Invoke) { "-invoke=$Invoke" })
    if ($Prompt) {
        exec { & $s.Terraform "-chdir=$($s.Work)" apply @inv }
        return
    }
    # plan -out, then apply the saved plan: apply prints no plan for a saved one, so the plan is not shown twice.
    $planFile = Join-Path $s.Work 'manual.tfplan'
    $out = @(& $s.Terraform "-chdir=$($s.Work)" plan -input=false "-out=$planFile" @inv 2>&1 | ForEach-Object { "$_" })
    if ($LASTEXITCODE) { $out | ForEach-Object { Write-Host $_ }; throw "terraform plan failed (exit $LASTEXITCODE)" }
    $summary = @($out | Where-Object { $_ -match '^(Plan:|Changes to Outputs:|No changes\.|Actions:)' })
    Write-Build Cyan $(if ($summary) { $summary -join ' ' } else { 'plan saved' })
    try { exec { & $s.Terraform "-chdir=$($s.Work)" apply -input=false $planFile } }
    finally { Remove-Item -LiteralPath $planFile -ErrorAction Ignore }
}

# Synopsis: terraform destroy for -Scenario <name>, unattended (-auto-approve); -Prompt asks for approval.
task _Destroy {
    $s = Enter-ManualScenario
    if ($Prompt) { exec { & $s.Terraform "-chdir=$($s.Work)" destroy } }
    else { exec { & $s.Terraform "-chdir=$($s.Work)" destroy -auto-approve -input=false } }
}

# Synopsis: Tear the dind harness down (test/harness/scripts/harness-down.ps1) and forget every manual scenario's state.
task _Down {
    & (Join-Path $HarnessScripts 'harness-down.ps1')
    Get-ChildItem -LiteralPath $ManualStateRoot -Directory -ErrorAction Ignore | Where-Object Name -ne '_mirror' | Remove-Item -Recurse -Force -ErrorAction Ignore
}
# ---------------------------------------------------------------------------- build

# Synopsis: go build ./..., then the provider binary into the repo root.
task Build {
    requires -Variable GoExe
    exec { & $GoExe build ./... }
    exec { & $GoExe build -o . . }
}

# Synopsis: go vet ./...
task Vet {
    requires -Variable GoExe
    exec { & $GoExe vet ./... }
}

# The Go files gofmt checks: tracked and untracked-not-ignored ones only, so git-ignored scratch (test/harness/.work
# upstream clones and worktrees, .bin) is never walked. In batches, to stay under the Windows command-line limit.
function Get-RepoGoFileBatches {
    $files = @(exec { & $GitExe -C $BuildRoot ls-files -co --exclude-standard -- '*.go' } | Where-Object { $_ })
    for ($i = 0; $i -lt $files.Count; $i += 200) { ,@($files[$i..([Math]::Min($i + 199, $files.Count - 1))]) }
}

# Synopsis: gofmt -w on every Go file git tracks or would track (not git-ignored scratch).
task Fmt {
    requires -Variable GofmtExe, GitExe
    foreach ($b in Get-RepoGoFileBatches) { exec { & $GofmtExe -w @b } }
}

# Synopsis: go install . (into GOBIN / GOPATH\bin, the dev_overrides path).
task Install {
    requires -Variable GoExe
    exec { & $GoExe install . }
}

# Synopsis: Remove the provider binary, harness run logs and the ReferenceVerify cache.
task Clean {
    remove terraform-provider-docker, terraform-provider-docker.exe, test/harness/last-*.log, $RefVerifyCache
    # Build scratch that is cheap to recreate (never test/harness/.work/manual: it holds the manual scenarios' state).
    foreach ($d in 'ex', 'mirror', 'dev-provider', 'release-check') { remove (Join-Path $TpdWork $d) }
    # Everything this repo's tasks, tests and passes ever left in the system temp directory (all named tpd-*: tpd-p53*,
    # tpd-p6a*, tpd-p6b, tpd-pairwise, tpd-drift, tpd-manual, tpd-reference-verify, tpd-release-check, tpd-excov-<pid>,
    # tpd-mtc-<pid>.yml, tpd-deferred-base-<pid>.json, tpd-dev-provider, tpd-ex-*, tpd-example*, tpd-plugin-*,
    # tpd-acc*, tpd-config*, ...). Since Pass 6c nothing writes there any more.
    $removed = @(Remove-TpdTempItems)
    if ($removed.Count) { foreach ($r in $removed) { Write-Build Gray "removed $r" } }
    Write-Build Cyan "Clean: removed $($removed.Count) tpd-* item(s) from $([System.IO.Path]::GetTempPath())"
}

# Removes every tpd-* file or folder directly under the system temp directory (or -TempDir) and returns their paths.
function Remove-TpdTempItems([string]$TempDir = [System.IO.Path]::GetTempPath()) {
    foreach ($i in @(Get-ChildItem -LiteralPath $TempDir -Filter 'tpd-*' -Force -ErrorAction Ignore)) {
        Remove-Item -LiteralPath $i.FullName -Recurse -Force -ErrorAction SilentlyContinue
        if (Test-Path -LiteralPath $i.FullName) { Write-Warning "Clean: could not remove $($i.FullName) (in use?)" } else { $i.FullName }
    }
}

# ---------------------------------------------------------------------------- tests

# Synopsis: Unit tests: go test -short with TF_ACC unset (TestAcc* skip), one ledger unit per Go package: packages unchanged since their last pass are skipped (-Full runs all).
task Test {
    requires -Variable GoExe
    Invoke-LedgerGate -Gate unit -Run {
        param($toRun)
        $pkgs = @($toRun | ForEach-Object { if ($_.key -eq '.') { '.' } else { "./$($_.key)" } })
        $mod = (([IO.File]::ReadAllText((Join-Path $BuildRoot 'go.mod'))) -split "`n" | Where-Object { $_ -match '^module\s' } | Select-Object -First 1) -replace '^module\s+', ''
        $mod = $mod.Trim()
        $saved = $env:TF_ACC
        Remove-Item Env:TF_ACC -ErrorAction Ignore
        try {
            $global:LASTEXITCODE = 0
            $out = @(& $GoExe test @pkgs -short -count=1 -timeout 10m 2>&1 | ForEach-Object { "$_" } | Tee-Object -Variable lines | ForEach-Object { Write-Host $_ })
            $code = $LASTEXITCODE
        } finally { if ($null -ne $saved) { $env:TF_ACC = $saved } }
        # ok  <pkg> 1.2s | FAIL <pkg> 0.3s | FAIL <pkg> [build failed] | ?   <pkg> [no test files]
        $results = foreach ($l in $lines) {
            if ($l -match '^(ok|FAIL|\?)\s+(\S+)(?:\s+([\d.]+)s)?') {
                $ip = $Matches[2]
                $key = if ($ip -eq $mod) { '.' } elseif ($ip.StartsWith("$mod/")) { $ip.Substring($mod.Length + 1) } else { continue }
                @{ key = $key; status = $(if ($Matches[1] -eq 'FAIL') { 'fail' } else { 'pass' }); duration_ms = $(if ($Matches[3]) { [long]([double]$Matches[3] * 1000) } else { 0 }) }
            }
        }
        @{ Results = @($results); Error = $(if ($code) { "go test exit $code" } else { $null }) }
    }
}

# Synopsis: Unit tests of tools/ only (parity, apiwatch, compose2stack, genskill, doccounts, ...).
task Tools {
    requires -Variable GoExe
    Invoke-GoTestNoAcc './tools/...'
}

# Synopsis: Acceptance tests (TF_ACC=1) on the dind harness: plain pass, then the -SwarmInit pass; one ledger unit per TestAcc function, unchanged ones skipped (-Full runs all). Never in the default task.
task TestAcc {
    requires -Path $Harness
    Invoke-LedgerGate -Gate testacc -PlanArgs @('-swarm-regex', $SwarmAccRegex) -Run {
        param($toRun)
        $saved = $env:TF_ACC
        $env:TF_ACC = '1'
        $results = [System.Collections.Generic.List[object]]::new()
        $codes = [ordered]@{}
        try {
            # Both passes always run (when they have a unit to run); the gate fails afterwards if either did. Each
            # pass runs exactly the planned functions; swarm tests ($SwarmAccRegex) need DOCKER_HOST to be a swarm
            # manager and run once, in the swarm pass.
            foreach ($pass in 'plain', 'swarm') {
                $names = @($toRun | Where-Object { $_.extra.pass -eq $pass } | ForEach-Object key)
                if (-not $names.Count) { Write-Build Gray "TestAcc $pass pass: nothing to run"; continue }
                $log = if ($pass -eq 'swarm') { 'test/harness/last-acc-swarm.log' } else { 'test/harness/last-acc.log' }
                $run = '^(' + ($names -join '|') + ')$'
                Write-Build Cyan "TestAcc $pass pass: $($names.Count) function(s)"
                $code = 0
                try {
                    $global:LASTEXITCODE = 0
                    if ($pass -eq 'swarm') { & $Harness -SwarmInit -Run $run -Timeout 120m -Log $log } else { & $Harness -Run $run -Timeout 120m -Log $log }
                    $code = $LASTEXITCODE
                } catch { Write-Warning "TestAcc $pass pass: $_"; $code = 1 }
                $codes[$pass] = $code
                # Top-level results only (subtests are indented): --- PASS: TestAccX (12.34s)
                $logPath = Join-Path $BuildRoot $log
                if (Test-Path -LiteralPath $logPath) {
                    foreach ($l in [IO.File]::ReadAllLines($logPath)) {
                        if ($l -match '^--- (PASS|FAIL|SKIP): (TestAcc\w+) \(([\d.]+)s\)') {
                            $results.Add(@{ key = $Matches[2]; status = $Matches[1].ToLower(); duration_ms = [long]([double]$Matches[3] * 1000) })
                        }
                    }
                }
            }
        } finally {
            if ($null -ne $saved) { $env:TF_ACC = $saved } else { Remove-Item Env:TF_ACC -ErrorAction Ignore }
        }
        $bad = @($codes.Keys | Where-Object { $codes[$_] })
        @{ Results = @($results); Error = $(if ($bad.Count) { 'TestAcc failed (' + (($bad | ForEach-Object { "$_ pass exit $($codes[$_])" }) -join ', ') + ')' } else { $null }) }
    }
}

# Synopsis: test/harness/Invoke-E2E.ps1 (dind manager + 2 workers, TestAccE2E).
task E2E {
    requires -Path $Harness
    exec { & $Harness }
}

# ---------------------------------------------------------------------------- generated content

# Synopsis: Regenerate .claude/skills (terraform-provider-docker, docker-cli-map) and README_resources.md.
task GenSkill {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/genskill }
}

# Synopsis: genskill -check: fail if the skills or README_resources.md are stale.
task GenSkillCheck {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/genskill -check } -ErrorMessage 'Run Invoke-Build GenSkill and stage the result.'
}

# Synopsis: genskill -strict: fail on missing descriptions or RequiresReplace attributes without "Forces replacement.".
task GenSkillStrict {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/genskill -strict } -ErrorMessage 'Each attribute listed has a RequiresReplace plan modifier but no "Forces replacement." in its MarkdownDescription.'
}

# Synopsis: GenSkill, then tfplugindocs generate + validate (docs/ is generated).
task Docs GenSkill, {
    requires -Variable GoExe
    exec { & $GoExe run $TfPluginDocs generate --provider-name docker }
    exec { & $GoExe run $TfPluginDocs validate --provider-name docker }
}

# Synopsis: tfplugindocs validate.
task DocsValidate {
    requires -Variable GoExe
    exec { & $GoExe run $TfPluginDocs validate --provider-name docker }
}

# Synopsis: gofmt -l: fail if any Go file needs formatting.
task FmtCheck {
    requires -Variable GofmtExe
    requires -Variable GitExe
    $unformatted = @(foreach ($b in Get-RepoGoFileBatches) { exec { & $GofmtExe -l @b } | Where-Object { $_ } })
    assert ($unformatted.Count -eq 0) "gofmt needed (Invoke-Build Fmt): $($unformatted -join ', ')"
}

# Synopsis: Every static check (FmtCheck, Vet, GenSkillCheck, GenSkillStrict, DocsValidate); runs all, then lists failures.
task Check ?FmtCheck, ?Vet, ?GenSkillCheck, ?GenSkillStrict, ?DocsValidate, {
    $failed = @('FmtCheck', 'Vet', 'GenSkillCheck', 'GenSkillStrict', 'DocsValidate' | Where-Object { error $_ })
    assert ($failed.Count -eq 0) "Check failed: $($failed -join ', ')"
    Write-Build Green 'Check: all steps passed'
}

# ---------------------------------------------------------------------------- tools

# Synopsis: Regenerate templates/guides/migrating-from-kreuzwerker.md.tmpl from reference/_gen/renames.json (then Docs).
task Guide {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/parity -guide templates/guides/migrating-from-kreuzwerker.md.tmpl }
}

# Synopsis: Fail if the migration guide template is stale against reference/_gen/renames.json.
task GuideCheck {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/parity -guide-check templates/guides/migrating-from-kreuzwerker.md.tmpl } -ErrorMessage 'Run Invoke-Build Guide, Docs.'
}

# Synopsis: kreuzwerker/docker v4.6.0 parity: every type, and every attribute against reference/_gen/renames.json (a gap: row fails).
task Parity {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/parity }
}

# Synopsis: kreuzwerker parity as CI runs it: Parity, plus fail when a type marked missing has no [BLOCKED] line in BUILD_REPORT.md.
task ParityCheck {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/parity -check BUILD_REPORT.md } -ErrorMessage 'Parity gap (gap: rows above), or a missing type without a [BLOCKED] line in BUILD_REPORT.md.'
}

# Synopsis: Pinned Engine API spec vs the newest published (go run ./tools/apiwatch).
task ApiWatch {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/apiwatch }
}

# Synopsis: apiwatch -check (CI): fail when a newer Engine API spec is published than tools/apiwatch/pinned.json (needs the network).
task ApiWatchCheck {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/apiwatch -check } -ErrorMessage 'A newer Engine API spec is published (or the probe failed): see .claude/skills/engine-api-watch/SKILL.md.'
}

# Synopsis: Rewrite the generated type-count blocks in CLAUDE.md and README.md from internal/provider/registry_gen.go, and README.md's evidence numbers (fieldmap, TestAcc, compose parity fixtures, example tests, pairwise configs) (tools/doccounts).
task DocCounts {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/doccounts -write }
}

# Synopsis: Fail if a type count, a short type list or a backticked docker_* name in CLAUDE.md / README.md disagrees with registry_gen.go, or README.md's evidence numbers (fieldmap, TestAcc, compose parity fixtures, example tests, pairwise configs) are stale.
task DocCountsCheck {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/doccounts } -ErrorMessage 'CLAUDE.md / README.md disagree with internal/provider/registry_gen.go. Run Invoke-Build DocCounts (after RegistryGen; also for stale evidence numbers) and fix any hand-written count or name it lists.'
}

# Synopsis: Convert a compose file to HCL once: -In <file> -Out <file> -Mode swarm|local.
task Compose2Stack {
    requires -Variable GoExe
    $inPath = [System.IO.Path]::GetFullPath($In, $BuildRoot)
    $outPath = [System.IO.Path]::GetFullPath($Out, $BuildRoot)
    requires -Path $inPath
    exec { & $GoExe run ./tools/compose2stack -in $inPath -out $outPath -mode $Mode }
}

# ---------------------------------------------------------------------------- reference/

# Synopsis: Rebuild reference/inventory.json + INVENTORY.md from reference/_gen/fragments (merge.py).
task Inventory {
    requires -Variable PythonExe
    requires -Path (Join-Path $RefDir '_gen/merge.py')
    $ver = exec { & $PythonExe -I --version }
    Write-Build Cyan "Inventory: $ver ($PythonExe)"
    exec { & $PythonExe -I (Join-Path $RefDir '_gen/merge.py') } -ErrorMessage 'merge.py reported validation errors (ERR lines above).'
}

# Synopsis: Pin consistency: every pinned version string and hash in the generators, build scripts, tools/apiwatch/pinned.json, go.mod and the dind harness, and the generated pins (inventory.json, contract_gen.go), equal reference/SOURCES.md (reference/_gen/pins.py).
task PinCheck {
    requires -Variable PythonExe
    exec { & $PythonExe -I (Join-Path $RefDir '_gen/pins.py') $BuildRoot } -ErrorMessage 'Pins differ from reference/SOURCES.md (MISMATCH lines above). SOURCES.md is the single source: fix the copy, or run Invoke-Build Inventory, ContractGen for the generated ones.'
}

# Synopsis: Inventory and PinCheck, then fail if git diff of inventory.json / INVENTORY.md is not empty.
task InventoryCheck Inventory, PinCheck, {
    requires -Variable GitExe
    exec { & $GitExe diff --exit-code --stat -- @InventoryFiles } -ErrorMessage 'Generated inventory differs from git. Edit fragments, run Invoke-Build Inventory, stage both files.'
    Write-Build Green 'InventoryCheck: inventory.json and INVENTORY.md match the fragments'
}

# Synopsis: Inventory, then check the outputs contract (V1 identity+version, V2 raw+structured, V3 registered version names + uses_cli, V4 fields, V5 per-field sensitivity, V6 action result, L1 bounded logs; reference/_gen/SCHEMA.md); violations fail unless listed in reference/_gen/allowed-violations.txt.
task InventoryValidate Inventory, {
    requires -Variable PythonExe
    requires -Path (Join-Path $RefDir '_gen/validate.py')
    exec { & $PythonExe -I (Join-Path $RefDir '_gen/validate.py') } -ErrorMessage 'Outputs contract violations (VIOLATION / STALE lines above). Fix the outputs fragment or add a reasoned line to reference/_gen/allowed-violations.txt.'
}

# Synopsis: Re-hash reference/upstream against the pinned upstream commits in SOURCES.md (fetches trees once; cached in test/harness/.work/reference-verify).
task ReferenceVerify {
    requires -Variable GitExe
    $up = Join-Path $RefDir 'upstream'
    $sources = [System.IO.File]::ReadAllText((Join-Path $RefDir 'SOURCES.md'))
    $null = New-Item -ItemType Directory -Force $RefVerifyCache

    $all = @{}
    Get-ChildItem -LiteralPath $up -Recurse -File -Force | ForEach-Object {
        $all[[System.IO.Path]::GetRelativePath($up, $_.FullName).Replace('\', '/')] = $_.FullName
    }
    $covered = @{}
    $problems = [System.Collections.Generic.List[string]]::new()

    foreach ($rel in @($all.Keys)) {
        foreach ($s in $ReferenceSkip) {
            if ($rel -eq $s -or ($s.EndsWith('/') -and $rel.StartsWith($s))) { $covered[$rel] = 'skip' }
        }
    }

    foreach ($src in $ReferenceSources) {
        if (-not $sources.Contains($src.Sha)) { $problems.Add("$($src.Name): SHA $($src.Sha) is not in SOURCES.md") }
        $tree = Get-UpstreamTree $src
        $pairs = [System.Collections.Generic.List[object]]::new()
        if ($src.ContainsKey('Local')) {
            $prefix = "$($src.Local)/"
            foreach ($rel in @($all.Keys | Where-Object { $_.StartsWith($prefix) } | Sort-Object)) {
                $sub = $rel.Substring($prefix.Length)
                if ($src.ContainsKey('Exclude') -and $sub -in $src.Exclude) { continue }
                $pairs.Add(@($rel, $sub))
            }
        }
        if ($src.ContainsKey('Files')) {
            foreach ($k in $src.Files.Keys) { $pairs.Add(@($k, $src.Files[$k])) }
        }
        $shas = @(Get-LocalBlobShas @($pairs | ForEach-Object { $all[$_[0]] }))
        $ok = 0
        for ($i = 0; $i -lt $pairs.Count; $i++) {
            $rel, $upPath = $pairs[$i]
            $covered[$rel] = $src.Name
            $want = $tree[$upPath]
            if (-not $want) { $problems.Add("$($src.Name): $rel is not in upstream at $upPath") }
            elseif ($want -ne $shas[$i]) { $problems.Add("$($src.Name): $rel MISMATCH (local $($shas[$i]), upstream $want)") }
            else { $ok++ }
        }
        Write-Build Gray ("{0,-13} {1} ok={2} of {3}" -f $src.Name, $src.Sha.Substring(0, 12), $ok, $pairs.Count)
    }

    foreach ($rel in $ReferenceSha256.Keys) {
        $want = $ReferenceSha256[$rel]
        if (-not $sources.Contains($want)) { $problems.Add("$rel`: sha256 $want is not in SOURCES.md") }
        $covered[$rel] = 'sha256'
        if (-not $all.ContainsKey($rel)) { $problems.Add("$rel`: missing"); continue }
        $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $all[$rel]).Hash.ToLowerInvariant()
        if ($got -ne $want) { $problems.Add("$rel`: sha256 MISMATCH (local $got, pinned $want)") }
        else { Write-Build Gray ("{0,-13} sha256 ok" -f $rel) }
    }

    foreach ($rel in @($all.Keys | Where-Object { -not $covered.ContainsKey($_) } | Sort-Object)) {
        $problems.Add("$rel`: not covered by any pinned source")
    }

    $skipped = @($covered.Values | Where-Object { $_ -eq 'skip' }).Count
    Write-Build Gray "files: $($all.Count) total, $($all.Count - $skipped) verified, $skipped skipped (local capture: $($ReferenceSkip -join ', '))"
    if ($problems.Count) { $problems | ForEach-Object { Write-Build Red "  $_" } }
    assert ($problems.Count -eq 0) "ReferenceVerify: $($problems.Count) problem(s)"
    Write-Build Green 'ReferenceVerify: every upstream file matches its pinned source'
}

# ---------------------------------------------------------------------------- registration (Pass 5)

# Synopsis: Regenerate internal/provider/registry_gen.go (+ its test) from reference/inventory.json (tools/registrygen).
task RegistryGen {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/registrygen }
}

# Synopsis: Fail if registry_gen.go differs from the inventory, or a constructor reports a type name the inventory does not have (TestRegistryGen).
task RegistryCheck {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/registrygen -check } -ErrorMessage 'Registration differs from reference/inventory.json. Run Invoke-Build Inventory, RegistryGen.'
    $saved = $env:TF_ACC
    Remove-Item Env:TF_ACC -ErrorAction Ignore
    try { exec { & $GoExe test ./internal/provider/ -run '^TestRegistryGen$' -count=1 } }
    finally { if ($null -ne $saved) { $env:TF_ACC = $saved } }
}

# Synopsis: List status markers in internal/provider: STUB(pass5) placeholders (fail) and PARTIAL(pass5) sub-feature gaps (listed only).
task StubReport {
    $dir = Join-Path $BuildRoot 'internal/provider'
    $hits = @(Get-ChildItem -LiteralPath $dir -Filter *.go -File |
            Where-Object { $_.Name -notlike '*_test.go' } |
            Select-String -Pattern '//[ \t]*(STUB|PARTIAL)\(pass5\)[ \t]+(\S+)[ \t]*(.*)$' -CaseSensitive)
    $stubs = @($hits | Where-Object { $_.Matches[0].Groups[1].Value -eq 'STUB' })
    $partials = @($hits | Where-Object { $_.Matches[0].Groups[1].Value -eq 'PARTIAL' })
    Write-Build Cyan ("Stubs ({0}):" -f $stubs.Count)
    foreach ($h in ($stubs | Sort-Object Filename, LineNumber)) { Write-Build Gray ("  {0}  ({1}:{2})" -f $h.Matches[0].Groups[2].Value, $h.Filename, $h.LineNumber) }
    Write-Build Cyan ("Partials ({0}):" -f $partials.Count)
    foreach ($h in ($partials | Sort-Object { $_.Matches[0].Groups[2].Value }, Filename, LineNumber)) { $m = $h.Matches[0]; Write-Build Gray ("  {0}  {1}  ({2}:{3})" -f $m.Groups[2].Value, $m.Groups[3].Value.Trim(), $h.Filename, $h.LineNumber) }
    $msg = "StubReport: {0} stubs, {1} partials" -f $stubs.Count, $partials.Count
    if ($stubs.Count) { throw "$msg. Stubs fail: implement each one (reference/inventory.json shows them as missing)." }
    Write-Build $(if ($partials.Count) { 'Yellow' } else { 'Green' }) $msg
}

# Synopsis: Regenerate internal/provider/contract_gen.go (outputs-contract tables + per-item model structs) from reference/inventory.json (tools/contractgen).
task ContractGen {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/contractgen }
}

# Synopsis: Fail if contract_gen.go differs from reference/inventory.json.
task ContractCheck {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/contractgen -check } -ErrorMessage 'contract_gen.go is stale. Run Invoke-Build Inventory, ContractGen.'
}

# FieldCoverage mode: 'report' lists the unmapped upstream fields and exits 0; 'enforce' fails on any gap,
# invalid reference/_gen/fieldmap.d entry or stale reference/_gen/fieldmap.json.
$FieldCoverageMode = 'enforce'

# Synopsis: Field-level coverage: every writable Engine API / compose-spec / bake field is mapped, skipped or partial (go run ./tools/fieldcov).
task FieldCoverage {
    requires -Variable GoExe
    if ($FieldCoverageMode -eq 'enforce') {
        exec { & $GoExe run ./tools/fieldcov -enforce } -ErrorMessage 'fieldcov: unmapped fields, a bad fieldmap.d entry, or a stale fieldmap.json (go run ./tools/fieldcov -write).'
    } else {
        exec { & $GoExe run ./tools/fieldcov }
    }
}

# Synopsis: Regenerate reference/_gen/fieldmap.json from the upstream specs, the provider and reference/_gen/fieldmap.d.
task FieldMap {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/fieldcov -write }
}

# Synopsis: Regenerate reference/_gen/deferred.json (everything knowingly not done, plus the release's known issues from reference/_gen/deferred.d/known_issues.json) and print totals by kind plus the items added or removed since the committed version.
task DeferredReport {
    requires -Variable GoExe, GitExe
    $null = New-Item -ItemType Directory -Force $TpdWork
    $base = Join-Path $TpdWork "deferred-base-$PID.json"
    # The committed version is the baseline; before the first commit of deferred.json there is none (empty file).
    $old = & $GitExe show 'HEAD:reference/_gen/deferred.json' 2>$null
    if ($LASTEXITCODE) { $old = '' }
    [System.IO.File]::WriteAllText($base, ($old -join "`n"))
    $global:LASTEXITCODE = 0
    try { exec { & $GoExe run ./tools/deferred -write -base $base } }
    finally { Remove-Item -LiteralPath $base -ErrorAction Ignore }
}

# Synopsis: Fail if reference/_gen/deferred.json is stale (go run ./tools/deferred).
task DeferredCheck {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/deferred } -ErrorMessage 'reference/_gen/deferred.json is stale. Run Invoke-Build DeferredReport.'
}

# ---------------------------------------------------------------------------- task files
# The gate ledger (Invoke-LedgerGate, used by Test, TestAcc, ExampleTests, PairwiseTest), release tasks (Release), pre-release tasks (DriftCheck, PreRelease), example tests (ExampleTests, ExampleCoverage) and
# pairwise tests (PairwiseTest) live in their own files, dot-sourced here so they share this script's variables and
# helpers.
foreach ($taskFile in 'build/ledger.tasks.ps1', 'build/release.tasks.ps1', 'build/prerelease.tasks.ps1', 'build/examples.tasks.ps1', 'build/pairwise.tasks.ps1') {
    . (Join-Path $BuildRoot $taskFile)
}

# Synopsis: Default (and CI's first step): Build, Vet, Test, GenSkillCheck, InventoryCheck, InventoryValidate, RegistryCheck, ContractCheck, DocCountsCheck, FieldCoverage, ExampleCoverage, ManualTaskCheck.
# FieldCoverage runs in enforce mode (Pass 5.3): any unmapped writable upstream field fails the default task.
# ExampleCoverage and ManualTaskCheck (Pass 6b) need no harness: every registered type has a tested example, and no gate
# reaches an underscore (manual) task.
task . Build, Vet, Test, GenSkillCheck, InventoryCheck, InventoryValidate, RegistryCheck, ContractCheck, DocCountsCheck, FieldCoverage, ExampleCoverage, ManualTaskCheck
