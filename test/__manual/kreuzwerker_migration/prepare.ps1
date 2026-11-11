#Requires -Version 7.0
<#
Run by Invoke-Build _Init -Scenario kreuzwerker_migration when the working copy has no state yet: creates on the
harness manager the objects kreuzwerker/docker v4.6.0 would have created (network kw-app, volume kw-data, image
busybox:1.36) and writes kreuzwerker-state.json.tmpl, filled with their IDs, as <WorkDir>/terraform.tfstate.
#>
param([Parameter(Mandatory)][string]$WorkDir)
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot '../../TfDockerTest') -Force

$net = Get-DockerObject -Kind network -Id 'kw-app'
if (-not $net) {
    $body = @{ Name = 'kw-app'; Driver = 'bridge'; Labels = @{ env = 'prod' }; IPAM = @{ Driver = 'default'; Config = @(@{ Subnet = '10.79.0.0/24'; Gateway = '10.79.0.1' }) } }
    $r = Invoke-DockerApi -Method POST -Path '/networks/create' -Body $body
    if ($r.StatusCode -ge 400) { throw "network create: $($r.Raw)" }
    $net = Get-DockerObject -Kind network -Id 'kw-app'
}
if (-not (Get-DockerObject -Kind volume -Id 'kw-data')) {
    $r = Invoke-DockerApi -Method POST -Path '/volumes/create' -Body @{ Name = 'kw-data'; Driver = 'local'; Labels = @{ 'com.example.team' = 'ops' } }
    if ($r.StatusCode -ge 400) { throw "volume create: $($r.Raw)" }
}
$img = Get-DockerObject -Kind image -Id 'busybox:1.36'
if (-not $img) {
    $r = Invoke-DockerApi -Method POST -Path '/images/create' -Query @{ fromImage = 'busybox'; tag = '1.36' }
    if ($r.StatusCode -ge 400) { throw "image pull: $($r.Raw)" }
    $img = Get-DockerObject -Kind image -Id 'busybox:1.36'
}
$digest = @($img.RepoDigests) | Where-Object { $_ } | Select-Object -First 1
$state = (Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'kreuzwerker-state.json.tmpl')).
    Replace('__LINEAGE__', [guid]::NewGuid().ToString()).
    Replace('__NETWORK_ID__', $net.Id).
    Replace('__IMAGE_ID__', $img.Id).
    Replace('__REPO_DIGEST__', [string]$digest)
$null = New-Item -ItemType Directory -Force $WorkDir
[IO.File]::WriteAllText((Join-Path $WorkDir 'terraform.tfstate'), $state)
Write-Host "kreuzwerker_migration: wrote a kreuzwerker/docker v4.6.0 state for network $($net.Id.Substring(0, 12)), volume kw-data, image $($img.Id.Substring(7, 12))"
