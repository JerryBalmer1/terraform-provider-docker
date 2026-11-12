# Release tasks, dot-sourced by terraform-provider-docker.build.ps1 (Pass 6a Lane G).
#
#   Release       local goreleaser snapshot (never signs, never publishes), then checks dist/.
#   ReleaseCheck  no goreleaser needed: .goreleaser.yml matrix == $ReleaseTargets, every target cross-compiles, and the
#                 asset-name gate (tools/releaseassets): release.yml uploads nothing else to the release, and dist/
#                 (when a snapshot is there) would upload exactly the registry asset set.
#
# The real release runs in .github/workflows/release.yml on a pushed v* tag; see RELEASING.md.

# The release target matrix, <goos>_<goarch>. The single source the checks compare .goreleaser.yml against
# (goos x goarch minus ignore there). Every entry was verified to cross-compile with CGO_ENABLED=0 in Pass 6a.
$ReleaseTargets = @(
    'darwin_amd64'
    'darwin_arm64'
    'linux_386'
    'linux_amd64'
    'linux_arm'
    'linux_arm64'
    'windows_amd64'
    'windows_arm64'
)
$ReleaseProject = 'terraform-provider-docker'
$GoreleaserConfig = Join-Path $BuildRoot '.goreleaser.yml'
$ReleaseManifest = Join-Path $BuildRoot 'terraform-registry-manifest.json'
$ReleaseDist = Join-Path $BuildRoot 'dist'
$ReleaseWorkflow = Join-Path $BuildRoot '.github/workflows/release.yml'
$GoreleaserInstall = 'go install github.com/goreleaser/goreleaser/v2@latest   (or: winget install goreleaser.goreleaser)'

# Reads the target matrix of the single build in .goreleaser.yml (goos x goarch minus ignore) as sorted <goos>_<goarch>.
# A deliberately small line parser for the layout that file uses; it fails rather than guess on anything else.
function Get-GoreleaserTargets([string]$Path) {
    $goos = [System.Collections.Generic.List[string]]::new()
    $goarch = [System.Collections.Generic.List[string]]::new()
    $ignore = [System.Collections.Generic.List[string]]::new()
    $top = ''; $list = ''; $builds = 0; $pendingOs = $null
    foreach ($raw in [System.IO.File]::ReadAllLines($Path)) {
        $line = ($raw -replace '\s+#.*$', '').TrimEnd()
        if ($line -match '^\s*#' -or $line -eq '') { continue }
        if ($line -match '^([A-Za-z_]+):') { $top = $Matches[1]; $list = ''; continue }
        if ($top -ne 'builds') { continue }
        if ($line -match '^  - ') { $builds++ }
        if ($line -match '^\s{2,4}(?:- )?(goos|goarch|ignore|targets):\s*(.*)$') {
            $list = $Matches[1]
            if ($list -eq 'targets') { throw "${Path}: builds.targets is not supported by Get-GoreleaserTargets; use goos/goarch/ignore." }
            if ($Matches[2] -ne '') { throw "${Path}: inline list for builds.$list is not supported; use one '- item' per line." }
            continue
        }
        if ($line -match '^\s{4}[A-Za-z_]+:') { $list = ''; continue }
        $value = $null
        if ($line -match '^\s+-\s+"?([A-Za-z0-9]+)"?$') { $value = $Matches[1] }
        switch ($list) {
            'goos' { if ($value) { $goos.Add($value) } }
            'goarch' { if ($value) { $goarch.Add($value) } }
            'ignore' {
                if ($line -match '^\s+(?:- )?goos:\s*"?([A-Za-z0-9]+)"?$') { $pendingOs = $Matches[1] }
                elseif ($line -match '^\s+(?:- )?goarch:\s*"?([A-Za-z0-9]+)"?$') {
                    if (-not $pendingOs) { throw "${Path}: builds.ignore entry with goarch but no goos before it." }
                    $ignore.Add("${pendingOs}_$($Matches[1])"); $pendingOs = $null
                }
            }
        }
    }
    if ($builds -ne 1) { throw "${Path}: expected exactly one builds entry, found $builds." }
    if ($goos.Count -eq 0 -or $goarch.Count -eq 0) { throw "${Path}: builds.goos or builds.goarch is empty or unreadable." }
    $out = foreach ($o in $goos) { foreach ($a in $goarch) { if ("${o}_$a" -notin $ignore) { "${o}_$a" } } }
    @($out | Sort-Object -Unique)
}

# Fails when .goreleaser.yml and $ReleaseTargets disagree, naming each side's extras.
function Assert-ReleaseMatrix {
    $fromConfig = Get-GoreleaserTargets $GoreleaserConfig
    $expected = @($ReleaseTargets | Sort-Object -Unique)
    $onlyConfig = @($fromConfig | Where-Object { $_ -notin $expected })
    $onlyTasks = @($expected | Where-Object { $_ -notin $fromConfig })
    if ($onlyConfig.Count -or $onlyTasks.Count) {
        throw ("Release target matrix drift. Only in .goreleaser.yml: [{0}]. Only in `$ReleaseTargets (build/release.tasks.ps1): [{1}]." -f ($onlyConfig -join ', '), ($onlyTasks -join ', '))
    }
    Write-Build Green "Release matrix: $($expected.Count) targets agree (.goreleaser.yml == `$ReleaseTargets): $($expected -join ', ')"
}

# Checks dist/ after a snapshot: every <project>_<version>_<os>_<arch>.zip of the matrix, the SHA256SUMS, the
# manifest artifact, and that SHA256SUMS lists each of them with the right hash. Fails naming everything missing.
function Test-ReleaseDist([string]$Dist) {
    $metaPath = Join-Path $Dist 'metadata.json'
    if (-not (Test-Path $metaPath)) { throw "dist check: $metaPath not found (goreleaser did not finish?)." }
    $version = (Get-Content $metaPath -Raw | ConvertFrom-Json).version
    if (-not $version) { throw "dist check: no version in $metaPath." }
    if ($version -match '_') { throw "dist check: version '$version' contains '_', which breaks the <name>_<version>_<os>_<arch> asset pattern." }

    $prefix = "${ReleaseProject}_$version"
    $sumsName = "${prefix}_SHA256SUMS"
    $manifestName = "${prefix}_manifest.json"
    $zips = @($ReleaseTargets | ForEach-Object { "${prefix}_$_.zip" })

    # checksum.extra_files hashes the manifest under its asset name but does not copy it into dist/ (release.extra_files
    # uploads it straight from the repo on a real release). Put the same file next to the zips so dist/ mirrors the
    # release assets; its hash is then checked against the SHA256SUMS line like every other asset.
    $manifestPath = Join-Path $Dist $manifestName
    if (-not (Test-Path $manifestPath)) { Copy-Item -LiteralPath $ReleaseManifest -Destination $manifestPath }

    $problems = [System.Collections.Generic.List[string]]::new()
    $sumsPath = Join-Path $Dist $sumsName
    $sums = @{}
    if (Test-Path $sumsPath) {
        foreach ($l in Get-Content $sumsPath) {
            if ($l -match '^([0-9a-f]{64})\s+\*?(.+)$') { $sums[$Matches[2].Trim()] = $Matches[1] }
        }
    }
    else { $problems.Add("missing $sumsName") }

    foreach ($name in $zips + $manifestName) {
        $path = Join-Path $Dist $name
        if (-not (Test-Path $path)) { $problems.Add("missing $name"); continue }
        if (-not $sums.ContainsKey($name)) { $problems.Add("$sumsName does not list $name"); continue }
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        if ($hash -ne $sums[$name]) { $problems.Add("$name sha256 $hash != $sumsName entry $($sums[$name])") }
    }
    $extra = @($sums.Keys | Where-Object { $_ -notin ($zips + $manifestName) })
    if ($extra.Count) { $problems.Add("$sumsName lists unexpected files: $($extra -join ', ')") }

    if ($problems.Count) { throw ("dist check failed ($($problems.Count)):`n  " + ($problems -join "`n  ")) }
    Write-Build Green "dist check: version $version, $($zips.Count) zips + manifest, all listed in $sumsName with matching hashes."
}

# The asset-name gate (tools/releaseassets). The Terraform Registry rejects a release carrying anything besides the zips,
# <name>_<version>_manifest.json, _SHA256SUMS and _SHA256SUMS.sig (v0.1.0: the cosign .sigstore.json). -Dist: the files a
# real release of that snapshot would upload must be exactly one zip per $ReleaseTargets entry, the manifest, the
# SHA256SUMS and its .sig. Always: no release.yml step uploads any other file to the release.
function Assert-ReleaseAssets([string]$Dist) {
    $gateArgs = @('-workflow', $ReleaseWorkflow, '-goreleaser', $GoreleaserConfig)
    if ($Dist) { $gateArgs += @('-dist', $Dist, '-targets', ($ReleaseTargets -join ',')) }
    exec { & $GoExe run ./tools/releaseassets @gateArgs } -ErrorMessage 'Release asset gate failed (ASSET lines above): the Terraform Registry would reject the release.'
}

# Synopsis: Release config check without goreleaser: .goreleaser.yml matrix == $ReleaseTargets, the asset-name gate (release.yml uploads only registry assets; dist/ when a snapshot is there), every target cross-compiles (CGO_ENABLED=0, output in test/harness/.work/release-check).
task ReleaseCheck {
    Assert-ReleaseMatrix
    if (-not $GoExe) { throw 'go not found on PATH.' }
    if (Test-Path (Join-Path $ReleaseDist 'artifacts.json')) { Assert-ReleaseAssets $ReleaseDist }
    else { Write-Build Yellow 'asset gate: no goreleaser snapshot in dist/ (Invoke-Build Release makes one); checking release.yml only.'; Assert-ReleaseAssets '' }
    $out = Join-Path $TpdWork 'release-check'
    New-Item -ItemType Directory -Force $out | Out-Null
    $saved = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
    $failed = [System.Collections.Generic.List[string]]::new()
    try {
        $env:CGO_ENABLED = '0'
        foreach ($t in $ReleaseTargets) {
            $env:GOOS, $env:GOARCH = $t.Split('_')
            & $GoExe build -trimpath -ldflags '-s -w -X main.version=0.0.0-releasecheck' -o (Join-Path $out "${ReleaseProject}_$t") .
            if ($LASTEXITCODE) { $failed.Add($t) } else { Write-Build Green "cross-compile ok: $t" }
        }
    }
    finally {
        foreach ($k in $saved.Keys) {
            if ($null -eq $saved[$k]) { Remove-Item "env:$k" -ErrorAction Ignore } else { Set-Item "env:$k" -Value $saved[$k] }
        }
        Remove-Item -Recurse -Force $out -ErrorAction Ignore
    }
    if ($failed.Count) { throw "cross-compile failed for: $($failed -join ', ')" }
}

# Synopsis: Local goreleaser snapshot into dist/ (--snapshot --clean --skip=publish,sign; never signs or publishes), then check every expected asset.
task Release {
    $goreleaser = Resolve-Tool 'goreleaser'
    if (-not $goreleaser) {
        throw "goreleaser not found on PATH. Install GoReleaser v2: $GoreleaserInstall"
    }
    Assert-ReleaseMatrix
    exec { & $goreleaser release --snapshot --clean --skip=publish,sign }
    Test-ReleaseDist $ReleaseDist
    Assert-ReleaseAssets $ReleaseDist
}

# ---------------------------------------------------------------------------- after publishing

# The GPG key the registry knows for jerrybalmer1/docker (RELEASING.md, One-time setup 4): terraform init must report it.
$RegistrySigningKeyId = 'A18B1B39E1DFDD4C'
$RegistryProviderSource = 'jerrybalmer1/docker'

# The version of the top released heading in CHANGELOG.md ("## [X.Y.Z] (<date>)"; [Unreleased] is skipped).
function Get-ChangelogReleasedVersion {
    foreach ($l in [System.IO.File]::ReadAllLines((Join-Path $BuildRoot 'CHANGELOG.md'))) {
        if ($l -match '^## \[(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\]') { return $Matches[1] }
    }
    throw 'CHANGELOG.md: no released "## [X.Y.Z]" heading.'
}

# Synopsis: Post-release smoke (after publishing; not in PreRelease): terraform init of jerrybalmer1/docker at CHANGELOG's top released version from the public registry, in test/harness/.work/registry-smoke; asserts it installed and was signed by key A18B1B39E1DFDD4C.
task RegistrySmoke {
    $tf = Resolve-Tool 'terraform'
    if (-not $tf) {
        $tf = Get-ChildItem (Join-Path $BuildRoot 'test/harness/.bin') -Recurse -Include 'terraform', 'terraform.exe' -File -ErrorAction Ignore |
            Sort-Object FullName -Descending | Select-Object -First 1 -ExpandProperty FullName
    }
    if (-not $tf) { throw 'terraform not found on PATH or under test/harness/.bin (harness-up installs it).' }
    $version = Get-ChangelogReleasedVersion
    $dir = Join-Path $TpdWork "registry-smoke/$version"
    Remove-Item -Recurse -Force $dir -ErrorAction Ignore
    $null = New-Item -ItemType Directory -Force $dir
    [System.IO.File]::WriteAllText((Join-Path $dir 'main.tf'), @"
terraform {
  required_providers {
    docker = {
      source  = "$RegistryProviderSource"
      version = "= $version"
    }
  }
}
"@)
    # An empty CLI config: no dev_overrides, filesystem mirror or plugin cache of this machine may stand in for the registry.
    $cliConfig = Join-Path $dir 'empty.tfrc'
    [System.IO.File]::WriteAllText($cliConfig, '')
    $saved = @{ TF_CLI_CONFIG_FILE = $env:TF_CLI_CONFIG_FILE; TF_PLUGIN_CACHE_DIR = $env:TF_PLUGIN_CACHE_DIR }
    try {
        $env:TF_CLI_CONFIG_FILE = $cliConfig
        Remove-Item Env:TF_PLUGIN_CACHE_DIR -ErrorAction Ignore
        Write-Build Cyan "RegistrySmoke: terraform init of $RegistryProviderSource $version in $dir ($tf)"
        $out = & $tf "-chdir=$dir" init -no-color -input=false 2>&1 | Out-String
        $code = $LASTEXITCODE
    }
    finally {
        foreach ($k in $saved.Keys) {
            if ($null -eq $saved[$k]) { Remove-Item "env:$k" -ErrorAction Ignore } else { Set-Item "env:$k" -Value $saved[$k] }
        }
    }
    $out.TrimEnd() -split "`r?`n" | ForEach-Object { print Gray $_ }
    if ($code) { throw "RegistrySmoke: terraform init exited $code." }
    $installed = [regex]::Escape("Installed $RegistryProviderSource v$version (")
    $line = ($out -split "`r?`n") | Where-Object { $_ -match $installed } | Select-Object -First 1
    if (-not $line) { throw "RegistrySmoke: terraform init did not report 'Installed $RegistryProviderSource v$version'." }
    if ($line -notmatch "key ID $RegistrySigningKeyId\)") { throw "RegistrySmoke: '$($line.Trim())' is not signed by key ID $RegistrySigningKeyId." }
    $lock = Join-Path $dir '.terraform.lock.hcl'
    if (-not (Select-String -LiteralPath $lock -SimpleMatch "registry.terraform.io/$RegistryProviderSource" -Quiet)) { throw "RegistrySmoke: $lock has no entry for $RegistryProviderSource." }
    Write-Build Green "RegistrySmoke: $($line.Trim().TrimStart('- '))"
}
