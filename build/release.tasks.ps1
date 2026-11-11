# Release tasks, dot-sourced by terraform-provider-docker.build.ps1 (Pass 6a Lane G).
#
#   Release       local goreleaser snapshot (never signs, never publishes), then checks dist/.
#   ReleaseCheck  no goreleaser needed: .goreleaser.yml matrix == $ReleaseTargets, and every target cross-compiles.
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

# Synopsis: Release config check without goreleaser: .goreleaser.yml matrix == $ReleaseTargets, every target cross-compiles (CGO_ENABLED=0, output in %TEMP%).
task ReleaseCheck {
    Assert-ReleaseMatrix
    if (-not $GoExe) { throw 'go not found on PATH.' }
    $out = Join-Path ([System.IO.Path]::GetTempPath()) 'tpd-release-check'
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
}
