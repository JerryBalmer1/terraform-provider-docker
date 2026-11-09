<#
.SYNOPSIS
    Invoke-Build script for terraform-provider-docker.

.DESCRIPTION
    Run from the repository root with Invoke-Build (https://github.com/nightroman/Invoke-Build),
    which picks up this *.build.ps1 file automatically:

        Invoke-Build ?                  # list tasks with their synopses
        Invoke-Build                    # default task: Build, Vet, Test, GenSkillCheck, InventoryCheck, InventoryValidate, RegistryCheck, ContractCheck
        Invoke-Build Test
        Invoke-Build Build, Vet, GenSkillCheck
        Invoke-Build Compose2Stack -In docker-compose.yml -Out stack.tf -Mode swarm

    Install once: Install-Module InvokeBuild -Scope CurrentUser

    Tools are resolved from PATH when the script loads. A task failing with
    "Missing variable 'GoExe'" (or GitExe, GofmtExe, PythonExe) means that tool is not on PATH.

    The GNUmakefile mirrors these tasks for Linux/macOS/CI.

.PARAMETER Go
    Go command (default $env:GO, else 'go').

.PARAMETER Python
    Python 3 command for the Inventory task (default 'python').

.PARAMETER In
    Compose2Stack: compose file to read, relative to the repo root (default docker-compose.yml).

.PARAMETER Out
    Compose2Stack: .tf file to write, relative to the repo root (default stack.tf).

.PARAMETER Mode
    Compose2Stack: conversion mode, swarm or local (default swarm).
#>
param(
    [string]$Go = $(if ($env:GO) { $env:GO } else { 'go' }),
    [string]$Python = 'python',
    [string]$In = 'docker-compose.yml',
    [string]$Out = 'stack.tf',
    [ValidateSet('swarm', 'local')]
    [string]$Mode = 'swarm'
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

$TfPluginDocs = 'github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs'
$Harness = Join-Path $BuildRoot 'test/harness/Invoke-E2E.ps1'
$SwarmAccRegex = 'TestAcc(Service|Stack|Secret|DockerConfig|SwarmTokens|SwarmDataSources|Node_|Nodes_|NodeResource|SwarmNode|ComposeSwarm|ListSecret|P3SwarmList|ObjClusterVolume|Action(SwarmRotateTokens|NodeDrainActivate|ServiceScaleForceUpdateRollback|NodePromoteDemote|SwarmRotateCA|SwarmUnlockKey))'

$RefDir = Join-Path $BuildRoot 'reference'
$InventoryFiles = 'reference/inventory.json', 'reference/INVENTORY.md'
$RefVerifyCache = Join-Path ([System.IO.Path]::GetTempPath()) 'tpd-reference-verify'

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
    @{ Name = 'compose'; Url = 'https://github.com/docker/compose'; Sha = '4732a2ed20a49c43a7b44bb4928259162c3ae3be'; Local = 'compose-cli' }
    @{ Name = 'docker-cli'; Url = 'https://github.com/docker/cli'; Sha = 'd1c06ef6b41d88d76866aea43c246cd7c63d04fa'; Local = 'docker-cli' }
    @{ Name = 'buildkit'; Url = 'https://github.com/moby/buildkit'; Sha = 'f5b59a1f2fb25a0cb7072c398cab9aed7835da4e'; Local = 'dockerfile' }
    @{ Name = 'buildx'; Url = 'https://github.com/docker/buildx'; Sha = '3c4cbd35852fc1a476923627a780a7836c28b654'; Local = 'buildx' }
)
$ReferenceSha256 = @{ 'engine-api/docs.docker.com-v1.56.yaml' = '00ccd7d2e6e0b7a943ac0067007d181d723b2513ef1f6af3b6b54145314a6fa7' }
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

# Synopsis: gofmt -w .
task Fmt {
    requires -Variable GofmtExe
    exec { & $GofmtExe -w . }
}

# Synopsis: go install . (into GOBIN / GOPATH\bin, the dev_overrides path).
task Install {
    requires -Variable GoExe
    exec { & $GoExe install . }
}

# Synopsis: Remove the provider binary, harness run logs and the ReferenceVerify cache.
task Clean {
    remove terraform-provider-docker, terraform-provider-docker.exe, test/harness/last-*.log, $RefVerifyCache
}

# ---------------------------------------------------------------------------- tests

# Synopsis: Unit tests: go test ./... -short with TF_ACC unset (TestAcc* skip).
task Test {
    requires -Variable GoExe
    Invoke-GoTestNoAcc './...'
}

# Synopsis: Unit tests of tools/ only (parity, apiwatch, compose2stack, genskill).
task Tools {
    requires -Variable GoExe
    Invoke-GoTestNoAcc './tools/...'
}

# Synopsis: Acceptance tests (TF_ACC=1) on the dind harness: plain pass, then the -SwarmInit pass. Never in the default task.
task TestAcc {
    requires -Path $Harness
    $saved = $env:TF_ACC
    $env:TF_ACC = '1'
    try {
        # Both passes always run; the task fails afterwards if either did.
        $plain = 0
        try { $global:LASTEXITCODE = 0; & $Harness -Run TestAcc -Timeout 120m -Log test/harness/last-acc.log; $plain = $LASTEXITCODE }
        catch { Write-Warning "TestAcc plain pass: $_"; $plain = 1 }
        $swarm = 0
        try { $global:LASTEXITCODE = 0; & $Harness -SwarmInit -Run $SwarmAccRegex -Timeout 120m -Log test/harness/last-acc-swarm.log; $swarm = $LASTEXITCODE }
        catch { Write-Warning "TestAcc swarm pass: $_"; $swarm = 1 }
        assert (-not ($plain -or $swarm)) "TestAcc failed (plain pass exit $plain, swarm pass exit $swarm)"
    }
    finally {
        if ($null -ne $saved) { $env:TF_ACC = $saved } else { Remove-Item Env:TF_ACC -ErrorAction Ignore }
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
    $unformatted = @(exec { & $GofmtExe -l . } | Where-Object { $_ })
    assert ($unformatted.Count -eq 0) "gofmt needed (Invoke-Build Fmt): $($unformatted -join ', ')"
}

# Synopsis: Every static check (FmtCheck, Vet, GenSkillCheck, GenSkillStrict, DocsValidate); runs all, then lists failures.
task Check ?FmtCheck, ?Vet, ?GenSkillCheck, ?GenSkillStrict, ?DocsValidate, {
    $failed = @('FmtCheck', 'Vet', 'GenSkillCheck', 'GenSkillStrict', 'DocsValidate' | Where-Object { error $_ })
    assert ($failed.Count -eq 0) "Check failed: $($failed -join ', ')"
    print Green 'Check: all steps passed'
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

# Synopsis: Pinned Engine API spec vs the newest published (go run ./tools/apiwatch).
task ApiWatch {
    requires -Variable GoExe
    exec { & $GoExe run ./tools/apiwatch }
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
    print Cyan "Inventory: $ver ($PythonExe)"
    exec { & $PythonExe -I (Join-Path $RefDir '_gen/merge.py') } -ErrorMessage 'merge.py reported validation errors (ERR lines above).'
}

# Synopsis: Inventory, then fail if git diff of inventory.json / INVENTORY.md is not empty.
task InventoryCheck Inventory, {
    requires -Variable GitExe
    exec { & $GitExe diff --exit-code --stat -- @InventoryFiles } -ErrorMessage 'Generated inventory differs from git. Edit fragments, run Invoke-Build Inventory, stage both files.'
    print Green 'InventoryCheck: inventory.json and INVENTORY.md match the fragments'
}

# Synopsis: Inventory, then check the outputs contract (V1 identity+version, V2 raw+structured, V3 registered version names + uses_cli, V4 fields, V5 per-field sensitivity, V6 action result, L1 bounded logs; reference/_gen/SCHEMA.md); violations fail unless listed in reference/_gen/allowed-violations.txt.
task InventoryValidate Inventory, {
    requires -Variable PythonExe
    requires -Path (Join-Path $RefDir '_gen/validate.py')
    exec { & $PythonExe -I (Join-Path $RefDir '_gen/validate.py') } -ErrorMessage 'Outputs contract violations (VIOLATION / STALE lines above). Fix the outputs fragment or add a reasoned line to reference/_gen/allowed-violations.txt.'
}

# Synopsis: Re-hash reference/upstream against the pinned upstream commits in SOURCES.md (fetches trees once; cached in TEMP).
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
        print Gray ("{0,-13} {1} ok={2} of {3}" -f $src.Name, $src.Sha.Substring(0, 12), $ok, $pairs.Count)
    }

    foreach ($rel in $ReferenceSha256.Keys) {
        $want = $ReferenceSha256[$rel]
        if (-not $sources.Contains($want)) { $problems.Add("$rel`: sha256 $want is not in SOURCES.md") }
        $covered[$rel] = 'sha256'
        if (-not $all.ContainsKey($rel)) { $problems.Add("$rel`: missing"); continue }
        $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $all[$rel]).Hash.ToLowerInvariant()
        if ($got -ne $want) { $problems.Add("$rel`: sha256 MISMATCH (local $got, pinned $want)") }
        else { print Gray ("{0,-13} sha256 ok" -f $rel) }
    }

    foreach ($rel in @($all.Keys | Where-Object { -not $covered.ContainsKey($_) } | Sort-Object)) {
        $problems.Add("$rel`: not covered by any pinned source")
    }

    $skipped = @($covered.Values | Where-Object { $_ -eq 'skip' }).Count
    print Gray "files: $($all.Count) total, $($all.Count - $skipped) verified, $skipped skipped (local capture: $($ReferenceSkip -join ', '))"
    if ($problems.Count) { $problems | ForEach-Object { print Red "  $_" } }
    assert ($problems.Count -eq 0) "ReferenceVerify: $($problems.Count) problem(s)"
    print Green 'ReferenceVerify: every upstream file matches its pinned source'
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

# Synopsis: List the Pass 5 stubs and open markers left in internal/provider (STUB(pass5) lines), per file; prints the total.
task StubReport {
    $dir = Join-Path $BuildRoot 'internal/provider'
    $hits = @(Get-ChildItem -LiteralPath $dir -Filter *.go -File |
            Where-Object { $_.Name -notlike '*_test.go' } |
            Select-String -Pattern '//\s*STUB\(pass5\)\s+(\S+)(\s+partial:)?' -CaseSensitive)
    $stubs = @($hits | Where-Object { -not $_.Matches[0].Groups[2].Success })
    $partial = @($hits | Where-Object { $_.Matches[0].Groups[2].Success })
    foreach ($g in ($hits | Group-Object Filename | Sort-Object Name)) {
        print Cyan ("{0}: {1}" -f $g.Name, $g.Count)
        foreach ($h in $g.Group) { print Gray ("  " + $h.Line.Trim()) }
    }
    print $(if ($hits.Count) { 'Yellow' } else { 'Green' }) ("StubReport: {0} markers ({1} stubs, {2} partial)" -f $hits.Count, $stubs.Count, $partial.Count)
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

# Synopsis: Default: Build, Vet, Test, GenSkillCheck, InventoryCheck, InventoryValidate, RegistryCheck, ContractCheck.
task . Build, Vet, Test, GenSkillCheck, InventoryCheck, InventoryValidate, RegistryCheck, ContractCheck
