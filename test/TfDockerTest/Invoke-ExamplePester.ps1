#Requires -Version 7.0
<#
.SYNOPSIS
  Run the examples/**/<name>.tests.ps1 Pester tests (one harness pass). Started by Invoke-Build ExampleTests through
  test/harness/Invoke-E2E.ps1 -Script, so DOCKER_HOST, TF_ACC and the harness variables are already set.

.PARAMETER Tag
  Pester tags to run (the swarm pass: swarm).
.PARAMETER ExcludeTag
  Pester tags to leave out (the plain pass: swarm).
.PARAMETER OutputXml
  NUnit XML report path.
.PARAMETER LogDir
  Directory for one terraform log per example (TPD_EXAMPLE_LOG_DIR).
.PARAMETER Include
  Comma-separated wildcards on the example path relative to examples/ (e.g. 'resources/docker_tag,actions/*'); a
  pattern without '/' also matches the folder name alone (e.g. 'docker_tasks').
#>
[CmdletBinding()]
param(
    [string[]]$Tag,
    [string[]]$ExcludeTag,
    [string]$OutputXml,
    [string]$LogDir,
    [string]$Include
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$examples = Join-Path $repo 'examples'

# Pester 5 (newest installed 5.x; Pester 6 works too, used only when no 5.x is installed).
$pester = Get-Module -ListAvailable Pester | Where-Object { $_.Version.Major -eq 5 } | Sort-Object Version -Descending | Select-Object -First 1
if (-not $pester) { $pester = Get-Module -ListAvailable Pester | Where-Object { $_.Version.Major -ge 5 } | Sort-Object Version -Descending | Select-Object -First 1 }
if (-not $pester) { Write-Host 'Invoke-ExamplePester: Pester 5 or later is not installed (Install-Module Pester -MinimumVersion 5.0 -Scope CurrentUser).' -ForegroundColor Red; exit 1 }
Import-Module $pester.Path -Force
Import-Module (Join-Path $PSScriptRoot 'TfDockerTest.psd1') -Force

if ($LogDir) { $null = New-Item -ItemType Directory -Force $LogDir; $env:TPD_EXAMPLE_LOG_DIR = $LogDir }
# Build the provider mirror once for every example (reused through TPD_PROVIDER_MIRROR).
$mirror = Initialize-TfProviderMirror
Write-Host "Invoke-ExamplePester: provider mirror $($mirror.Dir) ($($mirror.Version) $($mirror.Platform), zip sha256 $($mirror.Sha256)), terraform $(Get-TfTerraformPath), DOCKER_HOST $env:DOCKER_HOST, Pester $($pester.Version)"

$files = @(Get-ChildItem -LiteralPath $examples -Recurse -File -Filter '*.tests.ps1' | Sort-Object FullName)
if ($Include) {
    $pats = $Include -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ }
    $files = @($files | Where-Object {
            $rel = [IO.Path]::GetRelativePath($examples, $_.DirectoryName).Replace('\', '/')
            $leaf = Split-Path -Leaf $rel
            $hit = $false; foreach ($p in $pats) { if ($rel -like $p -or ($p -notmatch '/' -and $leaf -like $p)) { $hit = $true } }; $hit
        })
}
if (-not $files.Count) { Write-Host 'Invoke-ExamplePester: no example tests selected.'; exit 0 }

$c = New-PesterConfiguration
$c.Run.Path = @($files.FullName)
$c.Run.PassThru = $true
$c.Output.Verbosity = 'Detailed'
if ($Tag) { $c.Filter.Tag = $Tag }
if ($ExcludeTag) { $c.Filter.ExcludeTag = $ExcludeTag }
if ($OutputXml) {
    $null = New-Item -ItemType Directory -Force (Split-Path -Parent $OutputXml)
    $c.TestResult.Enabled = $true
    $c.TestResult.OutputFormat = 'NUnitXml'
    $c.TestResult.OutputPath = $OutputXml
}
$r = Invoke-Pester -Configuration $c

Write-Host ''
Write-Host ('EXAMPLES passed={0} failed={1} skipped={2} notrun={3} total={4}' -f $r.PassedCount, $r.FailedCount, $r.SkippedCount, $r.NotRunCount, $r.TotalCount)
foreach ($t in $r.Failed) {
    $msg = (($t.ErrorRecord | Select-Object -First 1).Exception.Message -split "`r?`n")[0]
    Write-Host "FAILED $($t.ExpandedPath): $msg"
}
foreach ($t in $r.Skipped) { Write-Host "SKIPPED $($t.ExpandedPath)" }
if ($OutputXml) { Write-Host "NUnit XML: $OutputXml" }
if ($r.FailedCount -or $r.FailedBlocksCount -or $r.FailedContainersCount) { exit 1 }
exit 0
