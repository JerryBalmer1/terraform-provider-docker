#Requires -Version 7.0
<#
.SYNOPSIS
  Bring up the dind harness (manager + 3 workers, registry:2, TLS daemon) and
  write test/harness/.env.

.DESCRIPTION
  PowerShell equivalent of harness-up.sh. Uses the same docker-compose.yml and
  writes test/harness/.env in the same `export NAME=value` format (so
  `source test/harness/.env` still works from bash). The variables are also
  set in the current PowerShell session ($env:), so dot-sourcing is not
  required. The docker CLI is used here, in the test harness only; the
  provider never shells out to it.

  Before `compose up` it generates a throwaway CA plus server and client
  certificates for the tlsnode daemon into test/harness/tls/ (.NET
  CertificateRequest; no openssl needed). It also installs Terraform
  $TerraformVersion (default 1.14.9, override with $env:HARNESS_TF_VERSION)
  into test/harness/.bin/ once (SHA256-checked against the release's
  SHA256SUMS) and exports TF_ACC_TERRAFORM_VERSION plus TF_ACC_TERRAFORM_PATH
  so terraform-plugin-testing does not download it again for every test. If
  the download fails only TF_ACC_TERRAFORM_VERSION is exported and
  terraform-plugin-testing installs that version itself.
#>
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$HarnessDir  = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$ComposeFile = Join-Path $HarnessDir 'docker-compose.yml'
$EnvFile     = Join-Path $HarnessDir '.env'
$TlsDir      = Join-Path $HarnessDir 'tls'
$BinDir      = Join-Path $HarnessDir '.bin'
$TerraformVersion = if ($env:HARNESS_TF_VERSION) { $env:HARNESS_TF_VERSION } else { '1.14.9' }

# Forward slashes: Go accepts them on Windows, and bash `source .env` does not
# eat them the way it eats backslashes.
function ConvertTo-SlashPath([string]$p) { $p -replace '\\', '/' }

# Same values, same order as harness-up.sh.
$HarnessEnv = [ordered]@{
    DOCKER_HOST              = 'tcp://127.0.0.1:12375'
    DOCKER_WORKER1_HOST      = 'tcp://127.0.0.1:12376'
    DOCKER_WORKER2_HOST      = 'tcp://127.0.0.1:12377'
    DOCKER_WORKER3_HOST      = 'tcp://127.0.0.1:12379'
    E2E_MANAGER_ADDR         = 'manager:2377'
    E2E_ADVERTISE_ADDR       = 'eth0'
    TF_ACC_REGISTRY          = '127.0.0.1:12380'
    TF_ACC_REGISTRY_NODES    = 'manager:12380'
    TF_ACC_TLS_HOST          = 'tcp://127.0.0.1:12378'
    TF_ACC_TLS_CA            = ConvertTo-SlashPath (Join-Path $TlsDir 'ca.pem')
    TF_ACC_TLS_CERT          = ConvertTo-SlashPath (Join-Path $TlsDir 'cert.pem')
    TF_ACC_TLS_KEY           = ConvertTo-SlashPath (Join-Path $TlsDir 'key.pem')
    TF_ACC_TLS_CERT_PATH     = ConvertTo-SlashPath $TlsDir
    TF_ACC_GPU_NODE_LABEL    = 'gpu=nvidia'
    TF_ACC_HARNESS_COMPOSE   = ConvertTo-SlashPath $ComposeFile
    TF_ACC_TERRAFORM_VERSION = $TerraformVersion
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

# ---------------------------------------------------------------------------
# TLS material for tlsnode: CA, server cert (SAN tlsnode, localhost,
# 127.0.0.1), client cert. PKCS#1 RSA keys, PEM, same file names as the bash
# script (ca.pem/cert.pem/key.pem match DOCKER_CERT_PATH conventions).
function New-HarnessTls {
    param([string]$Dir)
    Add-Type -AssemblyName System.Security
    $X = 'System.Security.Cryptography.X509Certificates'
    if (-not (Test-Path -LiteralPath $Dir)) { New-Item -ItemType Directory -Path $Dir -Force | Out-Null }
    $sha256 = [System.Security.Cryptography.HashAlgorithmName]::SHA256
    $pkcs1  = [System.Security.Cryptography.RSASignaturePadding]::Pkcs1
    $now    = [DateTimeOffset]::UtcNow.AddDays(-1)
    $until  = [DateTimeOffset]::UtcNow.AddDays(30)

    $caKey = [System.Security.Cryptography.RSA]::Create(2048)
    $caReq = New-Object "$X.CertificateRequest" 'CN=tfdocker-e2e harness CA', $caKey, $sha256, $pkcs1
    $caReq.CertificateExtensions.Add((New-Object "$X.X509BasicConstraintsExtension" $true, $false, 0, $true))
    $caReq.CertificateExtensions.Add((New-Object "$X.X509KeyUsageExtension" ([System.Security.Cryptography.X509Certificates.X509KeyUsageFlags]'KeyCertSign, CrlSign'), $true))
    $caReq.CertificateExtensions.Add((New-Object "$X.X509SubjectKeyIdentifierExtension" $caReq.PublicKey, $false))
    $ca = $caReq.CreateSelfSigned($now, $until)

    function New-Leaf([string]$cn, [string]$ekuOid, [bool]$server) {
        $key = [System.Security.Cryptography.RSA]::Create(2048)
        $req = New-Object "$X.CertificateRequest" "CN=$cn", $key, $sha256, $pkcs1
        $req.CertificateExtensions.Add((New-Object "$X.X509BasicConstraintsExtension" $false, $false, 0, $true))
        $req.CertificateExtensions.Add((New-Object "$X.X509KeyUsageExtension" ([System.Security.Cryptography.X509Certificates.X509KeyUsageFlags]'DigitalSignature, KeyEncipherment'), $true))
        $oids = New-Object System.Security.Cryptography.OidCollection
        [void]$oids.Add((New-Object System.Security.Cryptography.Oid $ekuOid))
        $req.CertificateExtensions.Add((New-Object "$X.X509EnhancedKeyUsageExtension" $oids, $false))
        if ($server) {
            $san = New-Object "$X.SubjectAlternativeNameBuilder"
            $san.AddDnsName('tlsnode')
            $san.AddDnsName('localhost')
            $san.AddIpAddress([System.Net.IPAddress]::Parse('127.0.0.1'))
            $req.CertificateExtensions.Add($san.Build())
        }
        $serial = [byte[]]::new(16)
        [System.Security.Cryptography.RandomNumberGenerator]::Fill($serial)
        $serial[0] = $serial[0] -band 0x7F
        $cert = $req.Create($ca, $now, $until, $serial)
        return @{ Cert = $cert; Key = $key }
    }

    $srv = New-Leaf 'tlsnode' '1.3.6.1.5.5.7.3.1' $true
    $cli = New-Leaf 'tfdocker-e2e client' '1.3.6.1.5.5.7.3.2' $false
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $files = [ordered]@{
        'ca.pem'          = $ca.ExportCertificatePem()
        'server-cert.pem' = $srv.Cert.ExportCertificatePem()
        'server-key.pem'  = $srv.Key.ExportRSAPrivateKeyPem()
        'cert.pem'        = $cli.Cert.ExportCertificatePem()
        'key.pem'         = $cli.Key.ExportRSAPrivateKeyPem()
    }
    foreach ($name in $files.Keys) {
        [System.IO.File]::WriteAllText((Join-Path $Dir $name), (($files[$name] -replace "`r`n", "`n") + "`n"), $utf8)
    }
    Write-Host "Generated TLS material in $Dir (CA, server cert for tlsnode/localhost/127.0.0.1, client cert)."
}

# Reuse existing material: a running tlsnode keeps the certs it started with
# (harness-down removes tls/, so a fresh harness always gets new ones).
$tlsFiles = "ca.pem", "cert.pem", "key.pem", "server-cert.pem", "server-key.pem"
if (@($tlsFiles | Where-Object { -not (Test-Path -LiteralPath (Join-Path $TlsDir $_)) }).Count -gt 0) {
    New-HarnessTls -Dir $TlsDir
} else {
    Write-Host "Reusing TLS material in $TlsDir."
}

# ---------------------------------------------------------------------------
# Terraform CLI for terraform-plugin-testing (one download, cached in .bin).
function Install-HarnessTerraform {
    param([string]$Version, [string]$Dir)
    $os = if ($IsWindows) { 'windows' } elseif ($IsMacOS) { 'darwin' } else { 'linux' }
    $arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        'Arm64' { 'arm64' } default { 'amd64' }
    }
    $exe = if ($IsWindows) { 'terraform.exe' } else { 'terraform' }
    $target = Join-Path $Dir "terraform_$Version"
    $bin = Join-Path $target $exe
    if (Test-Path -LiteralPath $bin) { return $bin }
    $zipName = "terraform_${Version}_${os}_${arch}.zip"
    $base = "https://releases.hashicorp.com/terraform/$Version"
    New-Item -ItemType Directory -Path $target -Force | Out-Null
    $zip = Join-Path $target $zipName
    Write-Host "Downloading $base/$zipName ..."
    Invoke-WebRequest -Uri "$base/$zipName" -OutFile $zip -UseBasicParsing
    $sums = (Invoke-WebRequest -Uri "$base/terraform_${Version}_SHA256SUMS" -UseBasicParsing).Content
    if ($sums -is [byte[]]) { $sums = [System.Text.Encoding]::ASCII.GetString($sums) }
    $want = ($sums -split "`n" | Where-Object { $_ -match "\s$([regex]::Escape($zipName))\s*$" } | Select-Object -First 1) -replace '\s.*$', ''
    $got = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    if (-not $want -or $got -ne $want.ToLowerInvariant()) {
        Remove-Item -LiteralPath $target -Recurse -Force -ErrorAction SilentlyContinue
        throw "SHA256 mismatch for $zipName (got $got, want $want)"
    }
    Expand-Archive -LiteralPath $zip -DestinationPath $target -Force
    Remove-Item -LiteralPath $zip -Force
    return $bin
}

try {
    $tf = Install-HarnessTerraform -Version $TerraformVersion -Dir $BinDir
    $HarnessEnv['TF_ACC_TERRAFORM_PATH'] = ConvertTo-SlashPath $tf
    Write-Host "Terraform $TerraformVersion at $tf"
} catch {
    Write-Warning "harness-up: could not install Terraform $TerraformVersion ($($_.Exception.Message)); exporting TF_ACC_TERRAFORM_VERSION only (terraform-plugin-testing installs it per test)."
}

# The Docker Hub pull-through cache keeps its data in an external volume so
# it survives harness-down (`compose down -v` leaves external volumes alone).
& docker volume inspect tfdocker-e2e-mirror-cache *> $null
if ($LASTEXITCODE -ne 0) {
    & docker volume create tfdocker-e2e-mirror-cache | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'harness-up: cannot create volume tfdocker-e2e-mirror-cache.' }
}

& docker compose -f $ComposeFile up -d --wait
if ($LASTEXITCODE -ne 0) {
    throw "harness-up: 'docker compose up -d --wait' failed (exit $LASTEXITCODE)."
}

# --wait relies on the healthchecks; additionally confirm each published
# endpoint answers from the host side (port publishing can lag).
function Wait-HarnessDaemon {
    param([string]$Name, [string[]]$DockerArgs)
    for ($i = 1; $i -le 60; $i++) {
        & docker @DockerArgs info *> $null
        if ($LASTEXITCODE -eq 0) {
            Write-Host "$Name ready at $($DockerArgs[1])"
            return
        }
        Start-Sleep -Seconds 2
    }
    & docker compose -f $ComposeFile logs --tail=50 $Name 2>&1 | Write-Host
    throw "harness-up: $Name did not answer at $($DockerArgs[1]) within 120s"
}
function Wait-HarnessHttp {
    param([string]$Name, [string]$Url)
    for ($i = 1; $i -le 60; $i++) {
        try {
            if ((Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 5).StatusCode -eq 200) {
                Write-Host "$Name ready at $Url"
                return
            }
        } catch {
            Write-Verbose "harness-up: ${Name}: $($_.Exception.Message)"
        }
        Start-Sleep -Seconds 2
    }
    & docker compose -f $ComposeFile logs --tail=50 $Name 2>&1 | Write-Host
    throw "harness-up: $Name did not answer at $Url within 120s"
}

Wait-HarnessDaemon -Name manager -DockerArgs @('-H', $HarnessEnv.DOCKER_HOST)
Wait-HarnessDaemon -Name worker1 -DockerArgs @('-H', $HarnessEnv.DOCKER_WORKER1_HOST)
Wait-HarnessDaemon -Name worker2 -DockerArgs @('-H', $HarnessEnv.DOCKER_WORKER2_HOST)
Wait-HarnessDaemon -Name worker3 -DockerArgs @('-H', $HarnessEnv.DOCKER_WORKER3_HOST)
Wait-HarnessDaemon -Name tlsnode -DockerArgs @('-H', $HarnessEnv.TF_ACC_TLS_HOST, '--tlsverify',
    '--tlscacert', $HarnessEnv.TF_ACC_TLS_CA, '--tlscert', $HarnessEnv.TF_ACC_TLS_CERT, '--tlskey', $HarnessEnv.TF_ACC_TLS_KEY)
Wait-HarnessHttp -Name registry -Url "http://$($HarnessEnv.TF_ACC_REGISTRY)/v2/"

# LF line endings and no BOM so bash `source` reads it cleanly.
$lines = foreach ($k in $HarnessEnv.Keys) { "export $k=$($HarnessEnv[$k])" }
[System.IO.File]::WriteAllText($EnvFile, (($lines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))

foreach ($k in $HarnessEnv.Keys) {
    Set-Item -Path "Env:$k" -Value $HarnessEnv[$k]
}

Write-Host "Wrote ${EnvFile}:"
Get-Content $EnvFile | Write-Host
Write-Host 'Harness variables are also set in this PowerShell session.'
