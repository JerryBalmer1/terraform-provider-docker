#Requires -Version 7.0
<#
TfDockerTest: run the examples/ folders of terraform-provider-docker on the dind harness.

Invoke-TfExample copies one example folder into a temporary directory, applies optional rewrites (Replace,
RemoveBlock, Files), and runs terraform init, apply, a second plan that must be empty, and destroy, with a generated
dev_overrides CLI configuration (TF_CLI_CONFIG_FILE) pointing at the locally built provider. Assert-TfState compares
`terraform show -json` with what the Engine API reports. Every run takes a snapshot of the daemon first and removes
whatever is new afterwards (Clear-DockerSnapshotDiff), so a half-applied example leaves nothing behind.

Safety: anything that talks to a daemon refuses unless DOCKER_HOST is a harness endpoint (tcp://127.0.0.1:12375-12383)
or TPD_EXAMPLE_ALLOW_HOST=1 is set. Nothing here ever targets Docker Desktop.
#>
Set-StrictMode -Version Latest

$script:RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$script:ProviderAddress = 'JerryBalmer1/docker'
$script:ApiVersion = 'v1.56'
$script:HarnessHostPattern = '^tcp://127\.0\.0\.1:123(7[5-9]|8[0-3])/?$'
$script:SwarmActiveCache = @{}

# ---------------------------------------------------------------------------- environment

# Synopsis: true when DOCKER_HOST is the harness manager and TF_ACC is set (the example tests run only then).
function Test-TfHarness {
    [CmdletBinding()]
    param(
        # Also require the manager to be an active swarm manager.
        [switch]$Swarm,
        # Also require Docker Hub to be reachable (harness-up exports TF_ACC_SKIP_INTERNET when it is not).
        [switch]$Internet
    )
    # Dry run (Invoke-Build ExampleValidate): every example test "runs", as init + validate of its edited copy.
    if ($env:TPD_EXAMPLE_DRYRUN -eq '1') { return $true }
    if (-not $env:TF_ACC) { return $false }
    if ("$env:DOCKER_HOST" -notmatch $script:HarnessHostPattern) { return $false }
    if ($Internet -and $env:TF_ACC_SKIP_INTERNET) { return $false }
    if ($Swarm) {
        $key = $env:DOCKER_HOST
        if (-not $script:SwarmActiveCache.ContainsKey($key)) {
            $state = $null
            try { $state = (Invoke-DockerApi -Path '/info').Content.Swarm.LocalNodeState } catch { $state = $null }
            $script:SwarmActiveCache[$key] = ($state -eq 'active')
        }
        return $script:SwarmActiveCache[$key]
    }
    $true
}

function Assert-HarnessHost([string]$DockerHost) {
    if ($env:TPD_EXAMPLE_ALLOW_HOST -eq '1') { return }
    if ("$DockerHost" -notmatch $script:HarnessHostPattern) {
        throw "TfDockerTest: refusing to touch DOCKER_HOST '$DockerHost': not a dind harness endpoint (tcp://127.0.0.1:12375-12383). Run through test/harness/Invoke-E2E.ps1, or set TPD_EXAMPLE_ALLOW_HOST=1 for a disposable daemon."
    }
}

# Synopsis: the Terraform CLI: TF_ACC_TERRAFORM_PATH (the harness copy) when set, else terraform on PATH.
function Get-TfTerraformPath {
    [CmdletBinding()]
    param()
    if ($env:TF_ACC_TERRAFORM_PATH -and (Test-Path -LiteralPath $env:TF_ACC_TERRAFORM_PATH)) { return $env:TF_ACC_TERRAFORM_PATH }
    $cmd = Get-Command terraform -CommandType Application -ErrorAction Ignore | Select-Object -First 1
    if (-not $cmd) { throw 'TfDockerTest: terraform is not on PATH and TF_ACC_TERRAFORM_PATH is not set.' }
    $cmd.Source
}

# Synopsis: build the provider once (go build) into a directory and return that directory (the dev_overrides path).
function Initialize-TfDevProvider {
    [CmdletBinding()]
    param(
        # Where to put terraform-provider-docker[.exe]. Default: TPD_PROVIDER_DIR, else <temp>/tpd-dev-provider.
        [string]$ProviderDir,
        # Rebuild even when the binary exists (otherwise TPD_PROVIDER_DIR with a binary is reused as is).
        [switch]$Force
    )
    $exe = if ($IsWindows) { 'terraform-provider-docker.exe' } else { 'terraform-provider-docker' }
    if (-not $ProviderDir) {
        if ($env:TPD_PROVIDER_DIR -and -not $Force -and (Test-Path -LiteralPath (Join-Path $env:TPD_PROVIDER_DIR $exe))) {
            return $env:TPD_PROVIDER_DIR
        }
        $ProviderDir = if ($env:TPD_PROVIDER_DIR) { $env:TPD_PROVIDER_DIR } else { Join-Path ([IO.Path]::GetTempPath()) 'tpd-dev-provider' }
    }
    $bin = Join-Path $ProviderDir $exe
    if ($Force -or -not (Test-Path -LiteralPath $bin)) {
        $null = New-Item -ItemType Directory -Force -Path $ProviderDir
        $go = Get-Command go -CommandType Application -ErrorAction Ignore | Select-Object -First 1
        if (-not $go) { throw 'TfDockerTest: go is not on PATH; cannot build the provider.' }
        Push-Location $script:RepoRoot
        try {
            $out = & $go.Source build -o $bin . 2>&1 | ForEach-Object { "$_" }
            if ($LASTEXITCODE) { throw "TfDockerTest: go build failed: $($out -join "`n")" }
        } finally { Pop-Location }
    }
    # Child processes (and later calls) reuse this binary.
    $env:TPD_PROVIDER_DIR = $ProviderDir
    $ProviderDir
}

# Synopsis: write a Terraform CLI configuration with dev_overrides for JerryBalmer1/docker; returns its path.
function New-TfDevOverrideConfig {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$ProviderDir,
        [Parameter(Mandatory)][string]$Path
    )
    $dir = ($ProviderDir -replace '\\', '/')
    $text = @"
# Generated by TfDockerTest: run the locally built provider instead of the registry release.
provider_installation {
  dev_overrides {
    "$($script:ProviderAddress)" = "$dir"
  }
  direct {}
}
"@
    $parent = Split-Path -Parent $Path
    if ($parent) { $null = New-Item -ItemType Directory -Force -Path $parent }
    [IO.File]::WriteAllText($Path, $text + "`n")
    $Path
}

# Deadline of the running example (Invoke-TfExample sets it; $null = none). Invoke-TfCli kills terraform when it passes.
$script:TfDeadline = $null

# Synopsis: run terraform -chdir=<dir> <args>; returns ExitCode and Output (the lines), and appends them to LogFile.
# When $script:TfDeadline is set and terraform is still running at that time, its process tree is killed and the
# result is exit code 124 with a TIMEOUT line, so one hung example cannot stall the whole run.
function Invoke-TfCli {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$WorkingDir,
        [Parameter(Mandatory)][string[]]$Arguments,
        [string]$LogFile
    )
    $tf = Get-TfTerraformPath
    # One argument per ArgumentList entry: a bare -invoke=action.x.y must reach terraform as one string.
    $psi = [System.Diagnostics.ProcessStartInfo]::new($tf)
    $psi.ArgumentList.Add("-chdir=$WorkingDir")
    foreach ($a in $Arguments) { $psi.ArgumentList.Add($a) }
    $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $proc = [System.Diagnostics.Process]::Start($psi)
    $so = $proc.StandardOutput.ReadToEndAsync()
    $se = $proc.StandardError.ReadToEndAsync()
    $waitMs = -1
    if ($script:TfDeadline) { $waitMs = [int][Math]::Max(1000, ($script:TfDeadline - (Get-Date)).TotalMilliseconds) }
    $timedOut = -not $proc.WaitForExit($waitMs)
    if ($timedOut) {
        try { $proc.Kill($true) } catch {}
        $null = $proc.WaitForExit(30000)
    } else { $proc.WaitForExit() }
    $out = @(($so.Result + $se.Result) -split "`r?`n" | Where-Object { $_ -ne '' })
    $code = if ($timedOut) { 124 } else { $proc.ExitCode }
    if ($timedOut) { $out += "TIMEOUT: terraform $($Arguments -join ' ') killed at the example deadline ($('{0:HH:mm:ss}' -f $script:TfDeadline))" }
    if ($LogFile) {
        $hdr = "### terraform $($Arguments -join ' ')  (dir $WorkingDir, exit $code)"
        Add-Content -LiteralPath $LogFile -Value (@($hdr) + $out)
    }
    [pscustomobject]@{ ExitCode = $code; Output = $out }
}

# ---------------------------------------------------------------------------- Engine API

function ConvertTo-DockerBaseUri([string]$DockerHost) {
    if (-not $DockerHost) { throw 'TfDockerTest: DOCKER_HOST is not set.' }
    if ($DockerHost -match '^tcp://(.+?)/?$') { return "http://$($Matches[1])" }
    if ($DockerHost -match '^https?://') { return $DockerHost.TrimEnd('/') }
    throw "TfDockerTest: DOCKER_HOST '$DockerHost' is not a tcp:// endpoint (only the harness's plain TCP daemons are supported)."
}

# Synopsis: one Engine API call on DOCKER_HOST (plain HTTP); returns StatusCode, Content (parsed JSON or text) and Raw.
function Invoke-DockerApi {
    [CmdletBinding()]
    param(
        [string]$Method = 'GET',
        [Parameter(Mandatory)][string]$Path,
        [System.Collections.IDictionary]$Query,
        # Sent as JSON (unless it is already a string).
        $Body,
        [string]$InFile,
        [string]$OutFile,
        [string]$ContentType = 'application/json',
        [string]$DockerHost = $env:DOCKER_HOST,
        [int]$TimeoutSec = 600
    )
    Assert-HarnessHost $DockerHost
    $uri = (ConvertTo-DockerBaseUri $DockerHost) + '/' + $script:ApiVersion + $Path
    if ($Query -and $Query.Count) {
        $pairs = foreach ($k in $Query.Keys) { [uri]::EscapeDataString([string]$k) + '=' + [uri]::EscapeDataString([string]$Query[$k]) }
        $uri += '?' + ($pairs -join '&')
    }
    $p = @{ Method = $Method; Uri = $uri; SkipHttpErrorCheck = $true; TimeoutSec = $TimeoutSec; ErrorAction = 'Stop' }
    if ($null -ne $Body) {
        $p.Body = if ($Body -is [string]) { $Body } else { $Body | ConvertTo-Json -Depth 50 -Compress }
        $p.ContentType = $ContentType
    }
    if ($InFile) { $p.InFile = $InFile; $p.ContentType = $ContentType }
    if ($OutFile) { $p.OutFile = $OutFile; $p.PassThru = $true }
    $r = Invoke-WebRequest @p
    $raw = if ($OutFile) { '' } else { [string]$r.Content }
    $content = $raw
    $ct = "$($r.Headers['Content-Type'])"
    if ($raw -and ($ct -like '*json*' -or $raw.TrimStart().StartsWith('{') -or $raw.TrimStart().StartsWith('['))) {
        try { $content = $raw | ConvertFrom-Json -Depth 100 } catch { $content = $raw }
    }
    [pscustomobject]@{ StatusCode = [int]$r.StatusCode; Content = $content; Raw = $raw }
}

$script:KindPaths = @{
    container = '/containers/{0}/json'
    network   = '/networks/{0}'
    volume    = '/volumes/{0}'
    image     = '/images/{0}/json'
    service   = '/services/{0}'
    secret    = '/secrets/{0}'
    config    = '/configs/{0}'
    node      = '/nodes/{0}'
    plugin    = '/plugins/{0}/json'
    task      = '/tasks/{0}'
}

# Synopsis: inspect one object on DOCKER_HOST (container, network, volume, image, service, secret, config, node, plugin, task, swarm); $null when it does not exist.
function Get-DockerObject {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][ValidateSet('container', 'network', 'volume', 'image', 'service', 'secret', 'config', 'node', 'plugin', 'task', 'swarm')]
        [string]$Kind,
        # ID or name (not needed for swarm).
        [string]$Id
    )
    if ($Kind -eq 'swarm') {
        $r = Invoke-DockerApi -Path '/swarm'
    } else {
        if (-not $Id) { throw "Get-DockerObject: -Id is required for $Kind" }
        $r = Invoke-DockerApi -Path ($script:KindPaths[$Kind] -f $Id)
    }
    if ($r.StatusCode -eq 404 -or $r.StatusCode -eq 503) { return $null }
    if ($r.StatusCode -ge 400) { throw "Get-DockerObject $Kind '$Id': HTTP $($r.StatusCode): $($r.Raw)" }
    $r.Content
}

function Get-Prop($Object, [string[]]$Path) {
    $cur = $Object
    foreach ($p in $Path) {
        if ($null -eq $cur) { return $null }
        if ($cur -is [System.Collections.IDictionary]) { $cur = $cur[$p]; continue }
        $prop = $cur.PSObject.Properties[$p]
        $cur = if ($prop) { $prop.Value } else { $null }
    }
    $cur
}

function ConvertTo-Map($Object) {
    $h = @{}
    if ($null -eq $Object) { return $h }
    if ($Object -is [System.Collections.IDictionary]) { foreach ($k in $Object.Keys) { $h[[string]$k] = [string]$Object[$k] }; return $h }
    foreach ($p in $Object.PSObject.Properties) { $h[$p.Name] = [string]$p.Value }
    $h
}

# How Assert-TfState checks each managed resource type: the daemon object kind, the state attribute holding its
# ID or name, and where name and labels live on the daemon object.
$script:StateChecks = @{
    docker_container = @{ Kind = 'container'; Key = 'id'; DaemonName = { param($o) "$($o.Name)".TrimStart('/') }; DaemonLabels = { param($o) Get-Prop $o 'Config', 'Labels' } }
    docker_network   = @{ Kind = 'network'; Key = 'id'; DaemonName = { param($o) $o.Name }; DaemonLabels = { param($o) $o.Labels } }
    docker_volume    = @{ Kind = 'volume'; Key = 'name'; DaemonName = { param($o) $o.Name }; DaemonLabels = { param($o) $o.Labels } }
    docker_image     = @{ Kind = 'image'; Key = 'name' }
    docker_tag       = @{ Kind = 'image'; Key = 'target_image' }
    docker_service   = @{ Kind = 'service'; Key = 'id'; DaemonName = { param($o) Get-Prop $o 'Spec', 'Name' }; DaemonLabels = { param($o) Get-Prop $o 'Spec', 'Labels' } }
    docker_secret    = @{ Kind = 'secret'; Key = 'id'; DaemonName = { param($o) Get-Prop $o 'Spec', 'Name' }; DaemonLabels = { param($o) Get-Prop $o 'Spec', 'Labels' } }
    docker_config    = @{ Kind = 'config'; Key = 'id'; DaemonName = { param($o) Get-Prop $o 'Spec', 'Name' }; DaemonLabels = { param($o) Get-Prop $o 'Spec', 'Labels' } }
    docker_node      = @{ Kind = 'node'; Key = 'id' }
    docker_plugin    = @{ Kind = 'plugin'; Key = 'id' }
    docker_swarm_init = @{ Kind = 'swarm'; Key = 'id' }
}

# Synopsis: compare terraform state (terraform show -json) with the daemon: every managed object exists, and id, name and labels match.
function Assert-TfState {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$WorkingDir,
        [string]$LogFile,
        # Pre-parsed `terraform show -json` (tests); default: run it.
        $State
    )
    if ($null -eq $State) {
        $r = Invoke-TfCli -WorkingDir $WorkingDir -Arguments @('show', '-json', '-no-color') -LogFile $LogFile
        if ($r.ExitCode) { throw "Assert-TfState: terraform show -json failed (exit $($r.ExitCode)): $($r.Output -join ' ')" }
        $State = ($r.Output -join "`n") | ConvertFrom-Json -Depth 100
    }
    $problems = [System.Collections.Generic.List[string]]::new()
    $checked = 0
    $modules = [System.Collections.Generic.List[object]]::new()
    $root = Get-Prop $State 'values', 'root_module'
    if ($root) { $modules.Add($root) }
    for ($i = 0; $i -lt $modules.Count; $i++) {
        foreach ($c in @(Get-Prop $modules[$i] 'child_modules')) { if ($c) { $modules.Add($c) } }
    }
    foreach ($m in $modules) {
        foreach ($res in @(Get-Prop $m 'resources')) {
            if (-not $res -or $res.mode -ne 'managed') { continue }
            $spec = $script:StateChecks[$res.type]
            if (-not $spec) {
                $checked += Assert-TfStateComposite $res $problems
                continue
            }
            $v = $res.values
            $key = [string](Get-Prop $v $spec.Key)
            if (-not $key) { $problems.Add("$($res.address): no $($spec.Key) in state"); continue }
            $obj = Get-DockerObject -Kind $spec.Kind -Id $key
            $checked++
            if ($null -eq $obj) { $problems.Add("$($res.address): $($spec.Kind) '$key' not found on the daemon"); continue }
            $daemonId = [string](Get-Prop $obj 'Id')
            if (-not $daemonId) { $daemonId = [string](Get-Prop $obj 'ID') }
            $stateId = [string](Get-Prop $v 'id')
            switch ($spec.Kind) {
                'image' {
                    $sid = [string](Get-Prop $v 'image_id')
                    if (-not $sid) { $sid = [string](Get-Prop $v 'source_image_id') }
                    if (-not $sid -and $stateId -like 'sha256:*') { $sid = $stateId }
                    if ($sid -and $sid -like 'sha256:*' -and $sid -ne $daemonId) { $problems.Add("$($res.address): image id $sid in state, $daemonId on the daemon") }
                }
                'plugin' {
                    if ($stateId -and $daemonId -and $stateId -ne $daemonId) { $problems.Add("$($res.address): plugin id $stateId in state, $daemonId on the daemon") }
                }
                'volume' { }
                default {
                    if ($stateId -and $daemonId -and $stateId -ne $daemonId) { $problems.Add("$($res.address): id $stateId in state, $daemonId on the daemon") }
                }
            }
            if ($spec.ContainsKey('DaemonName')) {
                $sn = [string](Get-Prop $v 'name')
                $dn = [string](& $spec.DaemonName $obj)
                if ($sn -and $sn -ne $dn) { $problems.Add("$($res.address): name '$sn' in state, '$dn' on the daemon") }
            }
            if ($spec.ContainsKey('DaemonLabels')) {
                $sl = ConvertTo-Map (Get-Prop $v 'labels')
                $dl = ConvertTo-Map (& $spec.DaemonLabels $obj)
                foreach ($k in $sl.Keys) {
                    if (-not $dl.ContainsKey($k)) { $problems.Add("$($res.address): label '$k' in state, missing on the daemon") }
                    elseif ($dl[$k] -ne $sl[$k]) { $problems.Add("$($res.address): label '$k' = '$($sl[$k])' in state, '$($dl[$k])' on the daemon") }
                }
            }
        }
    }
    if ($problems.Count) { throw "Assert-TfState: $($problems.Count) mismatch(es):`n  " + ($problems -join "`n  ") }
    $checked
}

# docker_compose / docker_stack / docker_buildx_builder: the objects they own exist (by label or container name).
function Assert-TfStateComposite($res, $problems) {
    $v = $res.values
    switch ($res.type) {
        'docker_compose' {
            $name = [string](Get-Prop $v 'name'); $mode = [string](Get-Prop $v 'mode')
            if ($mode -eq 'swarm') {
                $f = @{ label = @("com.docker.stack.namespace=$name") } | ConvertTo-Json -Compress
                $r = Invoke-DockerApi -Path '/services' -Query @{ filters = $f }
            } else {
                $f = @{ label = @("com.docker.compose.project=$name") } | ConvertTo-Json -Compress
                $r = Invoke-DockerApi -Path '/containers/json' -Query @{ all = '1'; filters = $f }
            }
            if (@($r.Content).Count -lt 1) { $problems.Add("$($res.address): no $(if ($mode -eq 'swarm') { 'services' } else { 'containers' }) labelled for project '$name'") }
            return 1
        }
        'docker_stack' {
            $name = [string](Get-Prop $v 'name')
            $f = @{ label = @("com.docker.stack.namespace=$name") } | ConvertTo-Json -Compress
            $r = Invoke-DockerApi -Path '/services' -Query @{ filters = $f }
            if (@($r.Content).Count -lt 1) { $problems.Add("$($res.address): no services labelled com.docker.stack.namespace=$name") }
            return 1
        }
        'docker_buildx_builder' {
            $name = [string](Get-Prop $v 'name'); $driver = [string](Get-Prop $v 'driver')
            if ($driver -and $driver -ne 'docker-container') { return 0 }
            if ($null -eq (Get-DockerObject -Kind container -Id "buildx_buildkit_${name}0")) { $problems.Add("$($res.address): container buildx_buildkit_${name}0 not found") }
            return 1
        }
    }
    0
}

# ---------------------------------------------------------------------------- snapshot / sweep

function Get-ListIds([string]$Path, [scriptblock]$Select) {
    $r = Invoke-DockerApi -Path $Path
    if ($r.StatusCode -ge 400) { return @() }
    @(@($r.Content) | Where-Object { $_ } | ForEach-Object { & $Select $_ })
}

function Get-SwarmState([string]$DockerHost) {
    try { [string](Invoke-DockerApi -Path '/info' -DockerHost $DockerHost -TimeoutSec 20).Content.Swarm.LocalNodeState } catch { 'unreachable' }
}

# Synopsis: IDs of every container, network, volume, image, service, secret, config and plugin on DOCKER_HOST, plus the swarm state of DOCKER_HOST and the harness workers.
function Get-DockerSnapshot {
    [CmdletBinding()]
    param()
    Assert-HarnessHost $env:DOCKER_HOST
    $swarm = [ordered]@{}
    foreach ($h in @($env:DOCKER_HOST, $env:DOCKER_WORKER1_HOST, $env:DOCKER_WORKER2_HOST, $env:DOCKER_WORKER3_HOST) | Where-Object { $_ } | Select-Object -Unique) {
        $swarm[$h] = Get-SwarmState $h
    }
    $vol = Invoke-DockerApi -Path '/volumes'
    [pscustomobject]@{
        DockerHost = $env:DOCKER_HOST
        Containers = Get-ListIds '/containers/json?all=1' { param($o) $o.Id }
        Networks   = Get-ListIds '/networks' { param($o) $o.Id }
        Volumes    = @(if ($vol.StatusCode -lt 400) { @(Get-Prop $vol.Content 'Volumes') | Where-Object { $_ } | ForEach-Object { $_.Name } })
        Images     = Get-ListIds '/images/json' { param($o) $o.Id }
        Services   = Get-ListIds '/services' { param($o) $o.ID }
        Secrets    = Get-ListIds '/secrets' { param($o) $o.ID }
        Configs    = Get-ListIds '/configs' { param($o) $o.ID }
        Plugins    = Get-ListIds '/plugins' { param($o) $o.Name }
        Nodes      = Get-ListIds '/nodes' { param($o) $o.ID }
        Swarm      = $swarm
    }
}

# Synopsis: remove every object that is on the daemon now but not in -Baseline, and leave any swarm that was joined since; returns what was removed.
function Clear-DockerSnapshotDiff {
    [CmdletBinding()]
    param([Parameter(Mandatory)]$Baseline)
    $saved = $env:DOCKER_HOST
    $env:DOCKER_HOST = $Baseline.DockerHost
    $removed = [System.Collections.Generic.List[string]]::new()
    try {
        $now = Get-DockerSnapshot
        $new = { param($kind) @($now.$kind | Where-Object { $_ -notin $Baseline.$kind }) }
        foreach ($id in (& $new 'Services')) { $null = Invoke-DockerApi -Method DELETE -Path "/services/$id"; $removed.Add("service $id") }
        if (@(& $new 'Services').Count) { Start-Sleep -Seconds 3 }
        foreach ($id in (& $new 'Containers')) { $null = Invoke-DockerApi -Method DELETE -Path "/containers/$id" -Query @{ force = '1'; v = '1' }; $removed.Add("container $id") }
        foreach ($id in (& $new 'Secrets')) { $null = Invoke-DockerApi -Method DELETE -Path "/secrets/$id"; $removed.Add("secret $id") }
        foreach ($id in (& $new 'Configs')) { $null = Invoke-DockerApi -Method DELETE -Path "/configs/$id"; $removed.Add("config $id") }
        foreach ($id in (& $new 'Networks')) { $null = Invoke-DockerApi -Method DELETE -Path "/networks/$id"; $removed.Add("network $id") }
        foreach ($id in (& $new 'Volumes')) { $null = Invoke-DockerApi -Method DELETE -Path "/volumes/$([uri]::EscapeDataString($id))" -Query @{ force = '1' }; $removed.Add("volume $id") }
        foreach ($id in (& $new 'Plugins')) { $null = Invoke-DockerApi -Method DELETE -Path "/plugins/$([uri]::EscapeDataString($id))" -Query @{ force = '1' }; $removed.Add("plugin $id") }
        foreach ($id in (& $new 'Images')) { $null = Invoke-DockerApi -Method DELETE -Path "/images/$id" -Query @{ force = '1' }; $removed.Add("image $id") }
        foreach ($id in (& $new 'Nodes')) {
            $r = Invoke-DockerApi -Method DELETE -Path "/nodes/$id" -Query @{ force = '1' }
            if ($r.StatusCode -lt 400) { $removed.Add("node $id") }
        }
        foreach ($h in @($Baseline.Swarm.Keys)) {
            $was = $Baseline.Swarm[$h]
            $is = Get-SwarmState $h
            if ($was -eq 'inactive' -and $is -in 'active', 'locked', 'pending', 'error') {
                $null = Invoke-DockerApi -Method POST -Path '/swarm/leave' -Query @{ force = '1' } -DockerHost $h
                $removed.Add("swarm membership of $h")
            } elseif ($was -in 'active' -and $is -eq 'inactive') {
                Write-Warning "TfDockerTest: $h left the swarm during the example; the swarm pass may be affected."
            }
        }
    } finally { $env:DOCKER_HOST = $saved }
    $removed
}

# ---------------------------------------------------------------------------- HCL text edits

# Synopsis: remove the first HCL block whose header line matches HeaderPattern (a regex, e.g. '^resource "docker_volume" "nfs"' or '^\s*tls\s*\{'), braces matched outside strings, comments and heredocs.
function Remove-HclBlock {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string]$HeaderPattern
    )
    $nl = if ($Text.Contains("`r`n")) { "`r`n" } else { "`n" }
    $lines = $Text -split "`r?`n"
    $start = -1
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match $HeaderPattern -and $lines[$i].TrimEnd().EndsWith('{')) { $start = $i; break }
    }
    if ($start -lt 0) { throw "Remove-HclBlock: no block header matches '$HeaderPattern'" }
    $depth = 0; $end = -1
    $stack = [System.Collections.Generic.Stack[object]]::new()   # 'S' = inside a string; [int[]] = inside ${...} of a string
    $heredoc = $null
    for ($i = $start; $i -lt $lines.Count -and $end -lt 0; $i++) {
        $line = $lines[$i]
        if ($heredoc) { if ($line.Trim() -eq $heredoc) { $heredoc = $null }; continue }
        for ($j = 0; $j -lt $line.Length; $j++) {
            $c = $line[$j]
            $inString = $stack.Count -and $stack.Peek() -is [string]
            if ($inString) {
                if ($c -eq '\') { $j++; continue }
                if ($c -eq '"') { $null = $stack.Pop(); continue }
                if ($c -eq '$' -and $j + 1 -lt $line.Length -and $line[$j + 1] -eq '{') { $stack.Push([int[]]@(0)); $j++; continue }
                continue
            }
            if ($c -eq '#' -or ($c -eq '/' -and $j + 1 -lt $line.Length -and $line[$j + 1] -eq '/')) { break }
            if ($c -eq '"') { $stack.Push('S'); continue }
            if ($c -eq '<' -and $line.Substring($j) -match '^<<-?([A-Za-z_][A-Za-z0-9_]*)\s*$') { $heredoc = $Matches[1]; break }
            if ($c -eq '{') {
                if ($stack.Count) { $stack.Peek()[0]++ } else { $depth++ }
                continue
            }
            if ($c -eq '}') {
                if ($stack.Count) {
                    $top = $stack.Peek()
                    if ($top[0] -eq 0) { $null = $stack.Pop() } else { $top[0]-- }
                    continue
                }
                $depth--
                if ($depth -eq 0) { $end = $i; break }
            }
        }
    }
    if ($end -lt 0) { throw "Remove-HclBlock: unbalanced braces after '$HeaderPattern'" }
    $keep = @(if ($start -gt 0) { $lines[0..($start - 1)] }) + @(if ($end + 1 -lt $lines.Count) { $lines[($end + 1)..($lines.Count - 1)] })
    $keep -join $nl
}

# Synopsis: apply literal replacements (in order) and block removals to one configuration file's text.
function Edit-TfExampleText {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [System.Collections.IDictionary]$Replace,
        [string[]]$RemoveBlock
    )
    foreach ($b in @($RemoveBlock | Where-Object { $_ })) {
        try { $Text = Remove-HclBlock -Text $Text -HeaderPattern $b } catch { if ($_.Exception.Message -notlike 'Remove-HclBlock: no block header*') { throw } }
    }
    if ($Replace) { foreach ($k in $Replace.Keys) { $Text = $Text.Replace([string]$k, [string]$Replace[$k]) } }
    $Text
}

# ---------------------------------------------------------------------------- example runner

function Write-TfFiles([string]$Dir, [System.Collections.IDictionary]$Files) {
    if (-not $Files) { return }
    foreach ($k in $Files.Keys) {
        $p = Join-Path $Dir $k
        $null = New-Item -ItemType Directory -Force -Path (Split-Path -Parent $p)
        $v = $Files[$k]
        if ($v -is [byte[]]) { [IO.File]::WriteAllBytes($p, $v) } else { [IO.File]::WriteAllText($p, [string]$v) }
    }
}

function Add-ProviderRequirement([string]$Dir) {
    $tf = @(Get-ChildItem -LiteralPath $Dir -Filter *.tf -File)
    foreach ($f in $tf) { if ((Get-Content -Raw -LiteralPath $f.FullName) -match 'required_providers') { return } }
    $text = "# Added by TfDockerTest: the examples omit the terraform block.`nterraform {`n  required_providers {`n    docker = {`n      source = `"$($script:ProviderAddress)`"`n    }`n  }`n}`n"
    [IO.File]::WriteAllText((Join-Path $Dir '_tfexample_providers.tf'), $text)
}

function Get-ErrorExcerpt([string[]]$Output) {
    $lines = @($Output | Where-Object { $_ -and $_.Trim() })
    $i = 0
    for (; $i -lt $lines.Count; $i++) { if ($lines[$i] -match '^\s*(│\s*)?Error:') { break } }
    if ($i -lt $lines.Count) { return ($lines[$i..([Math]::Min($i + 14, $lines.Count - 1))] | ForEach-Object { $_.Trim() }) -join ' | ' }
    ($lines | Select-Object -Last 12 | ForEach-Object { $_.Trim() }) -join ' | '
}

# Synopsis: run one examples/ folder end to end on the harness: init, apply (+ state check, + -invoke of actions), empty re-plan, destroy; or only validate.
function Invoke-TfExample {
    [CmdletBinding()]
    param(
        # The example folder (usually $PSScriptRoot of the .tests.ps1 beside it).
        [Parameter(Mandatory)][string]$Path,
        # Literal text replacements applied, in order, to every .tf / .tfquery.hcl file of the copy (use [ordered]@{} when order matters).
        [System.Collections.IDictionary]$Replace,
        # Regexes of block header lines to remove from the copy (Remove-HclBlock), applied before Replace.
        [string[]]$RemoveBlock,
        # Extra files written into the copy (relative path => text or byte[]), e.g. a build context.
        [System.Collections.IDictionary]$Files,
        # Root variables, written to a .tfvars.json and passed with -var-file.
        [System.Collections.IDictionary]$Vars,
        # Environment variables set for the run (restored afterwards), e.g. DOCKER_HOST of another harness node.
        [System.Collections.IDictionary]$Env,
        # A configuration applied before the example and destroyed after it: main.tf text, or files (path => text).
        $Prerequisite,
        # Run after the prerequisite and before the example; gets the context object (WorkDir, PrereqDir, Root, LogFile).
        [scriptblock]$Setup,
        # Run after the example is destroyed (also on failure), before the prerequisite is destroyed.
        [scriptblock]$Cleanup,
        # Action addresses to run with terraform apply -invoke after the apply (action.<type>.<name>).
        [string[]]$Invoke,
        # Run terraform query (list resources) instead of apply / plan / destroy; QueryExpect regexes must appear in its output.
        [switch]$Query,
        [string[]]$QueryExpect,
        # Only terraform init + validate (allowlisted examples; no daemon needed).
        [switch]$ValidateOnly,
        # Second plan: Empty (default), IgnoreOutputs (only resource changes fail), Any (it runs, changes are expected,
        # e.g. an action that stops what the resource keeps running) or Skip.
        [ValidateSet('Empty', 'IgnoreOutputs', 'Any', 'Skip')][string]$PlanCheck = 'Empty',
        [switch]$SkipStateAssert,
        # Keep the temporary directory (its path is in the result).
        [switch]$KeepWorkDir,
        # Budget for the example (prerequisite, apply, invokes, second plan); terraform still running at the deadline
        # is killed and the example fails with TIMEOUT. Teardown (destroy, prerequisite destroy) gets its own
        # CleanupMinutes after that. Default: TPD_EXAMPLE_TIMEOUT_MIN, else 5.
        [ValidateRange(1, 120)][int]$TimeoutMinutes = $(if ($env:TPD_EXAMPLE_TIMEOUT_MIN) { [int]$env:TPD_EXAMPLE_TIMEOUT_MIN } else { 5 }),
        [ValidateRange(1, 60)][int]$CleanupMinutes = 3
    )
    $Path = (Resolve-Path -LiteralPath $Path).Path
    if ($env:TPD_EXAMPLE_DRYRUN -eq '1') { $ValidateOnly = [switch]$true }
    $name = ((Split-Path -Leaf (Split-Path -Parent $Path)) + '_' + (Split-Path -Leaf $Path)) -replace '[^A-Za-z0-9_.-]', '_'
    $root = Join-Path ([IO.Path]::GetTempPath()) ("tpd-ex-$name-" + [IO.Path]::GetRandomFileName().Replace('.', ''))
    $work = Join-Path $root 'work'
    $pre = Join-Path $root 'prereq'
    $null = New-Item -ItemType Directory -Force -Path $work
    $logDir = if ($env:TPD_EXAMPLE_LOG_DIR) { $env:TPD_EXAMPLE_LOG_DIR } else { $root }
    $null = New-Item -ItemType Directory -Force -Path $logDir
    $log = Join-Path $logDir "$name.log"
    Set-Content -LiteralPath $log -Value "# $Path  $(Get-Date -Format o)"

    $result = [pscustomobject]@{ Example = $Path; WorkDir = $work; LogFile = $log; Steps = [System.Collections.Generic.List[object]]::new(); StateChecked = 0; Swept = @() }
    $ctx = [pscustomobject]@{ WorkDir = $work; PrereqDir = $pre; Root = $root; LogFile = $log; Name = $name }

    $envNames = @('TF_CLI_CONFIG_FILE', 'TF_IN_AUTOMATION', 'TF_INPUT', 'CHECKPOINT_DISABLE') + @(if ($Env) { $Env.Keys })
    $savedEnv = @{}
    foreach ($n in $envNames) { $savedEnv[$n] = [Environment]::GetEnvironmentVariable($n) }

    $errors = [System.Collections.Generic.List[string]]::new()
    $step = {
        param([string]$Dir, [string]$Label, [string[]]$TfArgs, [int[]]$OkCodes = @(0))
        $r = Invoke-TfCli -WorkingDir $Dir -Arguments $TfArgs -LogFile $log
        $result.Steps.Add([pscustomobject]@{ Step = $Label; ExitCode = $r.ExitCode })
        if ($r.ExitCode -notin $OkCodes) { throw "$Label failed (exit $($r.ExitCode)): $(Get-ErrorExcerpt $r.Output)" }
        $r
    }
    $applied = $false; $preApplied = $false; $baseline = $null
    $script:TfDeadline = (Get-Date).AddMinutes($TimeoutMinutes)
    Add-Content -LiteralPath $log -Value "# budget ${TimeoutMinutes}m (deadline $('{0:HH:mm:ss}' -f $script:TfDeadline)), teardown ${CleanupMinutes}m"
    try {
        $providerDir = Initialize-TfDevProvider
        $env:TF_CLI_CONFIG_FILE = New-TfDevOverrideConfig -ProviderDir $providerDir -Path (Join-Path $root 'dev.tfrc')
        $env:TF_IN_AUTOMATION = '1'; $env:TF_INPUT = '0'; $env:CHECKPOINT_DISABLE = '1'
        if ($Env) { foreach ($k in $Env.Keys) { [Environment]::SetEnvironmentVariable($k, [string]$Env[$k]) } }

        # The copy: every file but the tests and import.sh, with the edits applied.
        $sources = @(Get-ChildItem -LiteralPath $Path -File | Where-Object { $_.Name -like '*.tf' -or $_.Name -like '*.hcl' } | ForEach-Object { Get-Content -Raw -LiteralPath $_.FullName })
        foreach ($b in @($RemoveBlock | Where-Object { $_ })) {
            if (-not @($sources | Where-Object { ($_ -split "`r?`n") -match $b }).Count) { throw "RemoveBlock '$b' matches no line of the example: update the test." }
        }
        if ($Replace) {
            foreach ($k in $Replace.Keys) {
                if (-not @($sources | Where-Object { $_.Contains([string]$k) }).Count) { throw "Replace key '$k' is not in the example: update the test." }
            }
        }
        Get-ChildItem -LiteralPath $Path -File | Where-Object { $_.Name -notlike '*.tests.ps1' -and $_.Name -ne 'import.sh' } | ForEach-Object {
            $dst = Join-Path $work $_.Name
            if ($_.Name -like '*.tf' -or $_.Name -like '*.hcl') {
                [IO.File]::WriteAllText($dst, (Edit-TfExampleText -Text (Get-Content -Raw -LiteralPath $_.FullName) -Replace $Replace -RemoveBlock $RemoveBlock))
            } else { Copy-Item -LiteralPath $_.FullName -Destination $dst }
        }
        Get-ChildItem -LiteralPath $Path -Directory | ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $work -Recurse }
        Write-TfFiles $work $Files
        Add-ProviderRequirement $work
        $varArgs = @()
        if ($Vars -and $Vars.Count) {
            $vf = Join-Path $root 'example.tfvars.json'
            [IO.File]::WriteAllText($vf, ($Vars | ConvertTo-Json -Depth 20))
            $varArgs = @("-var-file=$vf")
        }

        # No terraform init: the examples use only this provider (dev_overrides) and built-in providers, which need no
        # installation, and Terraform before 1.15 fails init for a dev-overridden provider the registry does not have.
        if ($ValidateOnly) {
            if ($env:TPD_EXAMPLE_DRYRUN -eq '1') {
                # Files a -Setup block would write (rootfs.tar, images.tar, plugin/config.json) do not exist in a dry run:
                # empty stand-ins, so file functions in the configuration can be evaluated.
                foreach ($tf in Get-ChildItem -LiteralPath $work -Filter *.tf -File) {
                    foreach ($m in [regex]::Matches((Get-Content -Raw -LiteralPath $tf.FullName), '(?:file|filesha256|filebase64)\("\$\{path\.module\}/([^"]+)"\)')) {
                        $p = Join-Path $work $m.Groups[1].Value
                        if (-not (Test-Path -LiteralPath $p)) { $null = New-Item -ItemType Directory -Force (Split-Path -Parent $p); [IO.File]::WriteAllText($p, '') }
                    }
                }
            }
            $null = & $step $work 'validate' @('validate', '-no-color')
            throw [System.OperationCanceledException]::new('done')
        }
        Assert-HarnessHost $env:DOCKER_HOST
        $baseline = Get-DockerSnapshot

        if ($Prerequisite) {
            $null = New-Item -ItemType Directory -Force -Path $pre
            if ($Prerequisite -is [System.Collections.IDictionary]) { Write-TfFiles $pre $Prerequisite } else { [IO.File]::WriteAllText((Join-Path $pre 'main.tf'), [string]$Prerequisite) }
            Add-ProviderRequirement $pre
            $preApplied = $true
            $null = & $step $pre 'prerequisite apply' @('apply', '-auto-approve', '-input=false', '-no-color')
        }
        if ($Setup) { $null = & $Setup $ctx }

        if ($Query) {
            $q = & $step $work 'query' (@('query', '-no-color') + $varArgs)
            foreach ($re in @($QueryExpect)) {
                if ($re -and -not (($q.Output -join "`n") -match $re)) { throw "query output does not match '$re': $(($q.Output | Select-Object -Last 10) -join ' | ')" }
            }
            throw [System.OperationCanceledException]::new('done')
        }

        $applied = $true
        $null = & $step $work 'apply' (@('apply', '-auto-approve', '-input=false', '-no-color') + $varArgs)
        if (-not $SkipStateAssert) { $result.StateChecked = Assert-TfState -WorkingDir $work -LogFile $log }
        foreach ($a in @($Invoke | Where-Object { $_ })) {
            $null = & $step $work "invoke $a" (@('apply', '-auto-approve', '-input=false', '-no-color', "-invoke=$a") + $varArgs)
        }
        if ($PlanCheck -ne 'Skip') {
            $planFile = Join-Path $root 'second.tfplan'
            $p = & $step $work 'second plan' (@('plan', '-detailed-exitcode', '-input=false', '-no-color', "-out=$planFile") + $varArgs) @(0, 2)
            if ($p.ExitCode -eq 2 -and $PlanCheck -ne 'Any') {
                $show = Invoke-TfCli -WorkingDir $work -Arguments @('show', '-json', '-no-color', $planFile) -LogFile $null
                $plan = ($show.Output -join "`n") | ConvertFrom-Json -Depth 100
                $changes = @(@(Get-Prop $plan 'resource_changes') | Where-Object { $_ -and (@($_.change.actions) -join ',') -notin 'no-op', 'read' } | ForEach-Object { "$($_.address) ($(@($_.change.actions) -join ','))" })
                $outs = @()
                $oc = Get-Prop $plan 'output_changes'
                if ($oc) { $outs = @($oc.PSObject.Properties | Where-Object { (@($_.Value.actions) -join ',') -ne 'no-op' } | ForEach-Object { "output.$($_.Name)" }) }
                if ($changes.Count -or ($PlanCheck -eq 'Empty' -and $outs.Count)) {
                    throw "second plan is not empty: $((@($changes) + @(if ($PlanCheck -eq 'Empty') { $outs })) -join ', ')"
                }
            }
        }
    } catch [System.OperationCanceledException] {
        # ValidateOnly / Query: finished early.
    } catch {
        $errors.Add($_.Exception.Message)
        # Diagnosis for the log: the tasks of every service created since the baseline (state, error, message).
        if ($baseline) {
            try {
                $svcResp = Invoke-DockerApi -Path '/services'
                $svcs = if ($svcResp.StatusCode -eq 200) { @($svcResp.Content) } else { @() }
                foreach ($svc in $svcs | Where-Object { $_ -and $_.PSObject.Properties['ID'] -and $_.ID -notin @($baseline.Services) }) {
                    $f = @{ service = @($svc.ID) } | ConvertTo-Json -Compress
                    $tasks = @((Invoke-DockerApi -Path '/tasks' -Query @{ filters = $f }).Content) | Where-Object { $_ }
                    $lines = foreach ($t in $tasks) { "  task $(Get-Prop $t 'ID') node $(Get-Prop $t 'NodeID') desired $(Get-Prop $t 'DesiredState') state $(Get-Prop $t 'Status', 'State') err '$(Get-Prop $t 'Status', 'Err')' msg '$(Get-Prop $t 'Status', 'Message')'" }
                    Add-Content -LiteralPath $log -Value (@("### tasks of service $(Get-Prop $svc 'Spec', 'Name')") + @($lines))
                }
            } catch { Add-Content -LiteralPath $log -Value "### task diagnosis failed: $($_.Exception.Message)" }
        }
    } finally {
        # Teardown gets its own budget, so an example that ran into its deadline is still destroyed.
        $script:TfDeadline = (Get-Date).AddMinutes($CleanupMinutes)
        if ($applied) {
            try { $null = & $step $work 'destroy' (@('destroy', '-auto-approve', '-input=false', '-no-color') + $varArgs) } catch { $errors.Add($_.Exception.Message) }
        }
        if ($Cleanup -and $baseline) { try { $null = & $Cleanup $ctx } catch { $errors.Add("cleanup: $($_.Exception.Message)") } }
        if ($preApplied) {
            try { $null = & $step $pre 'prerequisite destroy' @('destroy', '-auto-approve', '-input=false', '-no-color') } catch { $errors.Add($_.Exception.Message) }
        }
        if ($baseline) {
            try {
                $result.Swept = @(Clear-DockerSnapshotDiff -Baseline $baseline)
                if ($result.Swept.Count) { Add-Content -LiteralPath $log -Value ("### sweep removed: " + ($result.Swept -join ', ')) }
            } catch { $errors.Add("sweep: $($_.Exception.Message)") }
        }
        $script:TfDeadline = $null
        foreach ($n in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($n, $savedEnv[$n]) }
        if (-not $KeepWorkDir) {
            if ($logDir -eq $root -and $errors.Count) {
                # Keep the log, so the failure can be read.
                $kept = Join-Path ([IO.Path]::GetTempPath()) "tpd-ex-$name-failed.log"
                Copy-Item -LiteralPath $log -Destination $kept -Force -ErrorAction Ignore
                $result.LogFile = $kept
            }
            Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction Ignore
        }
    }
    if ($errors.Count) { throw ("$name`: " + ($errors -join ' || ') + "  (log: $($result.LogFile))") }
    $result
}

# ---------------------------------------------------------------------------- setup helpers for the example tests

# Synopsis: the swarm join token (worker, or manager with -Manager) of the swarm DOCKER_HOST manages.
function Get-DockerSwarmJoinToken {
    [CmdletBinding()]
    param([switch]$Manager)
    if ($env:TPD_EXAMPLE_DRYRUN -eq '1') { return 'SWMTKN-1-dry-run' }
    $sw = Get-DockerObject -Kind swarm
    if (-not $sw) { throw 'Get-DockerSwarmJoinToken: DOCKER_HOST is not a swarm manager.' }
    if ($Manager) { $sw.JoinTokens.Manager } else { $sw.JoinTokens.Worker }
}

# Synopsis: change a swarm node's role and/or availability through the Engine API (node update with the current version).
function Set-DockerNode {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Node,
        [ValidateSet('worker', 'manager')][string]$Role,
        [ValidateSet('active', 'pause', 'drain')][string]$Availability
    )
    $n = Get-DockerObject -Kind node -Id $Node
    if (-not $n) { throw "Set-DockerNode: node '$Node' not found" }
    $spec = $n.Spec
    if ($Role) { $spec.Role = $Role }
    if ($Availability) { $spec.Availability = $Availability }
    $r = Invoke-DockerApi -Method POST -Path "/nodes/$($n.ID)/update" -Query @{ version = [string]$n.Version.Index } -Body $spec
    if ($r.StatusCode -ge 400) { throw "Set-DockerNode $Node`: HTTP $($r.StatusCode): $($r.Raw)" }
}

# Synopsis: add an environment variable to a service's task template (a new spec, so docker_service_rollback has a previous one).
function Update-DockerServiceEnv {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Service, [Parameter(Mandatory)][string]$Env)
    $s = Get-DockerObject -Kind service -Id $Service
    if (-not $s) { throw "Update-DockerServiceEnv: service '$Service' not found" }
    $spec = $s.Spec
    $cs = $spec.TaskTemplate.ContainerSpec
    $list = @(if ($cs.PSObject.Properties['Env']) { $cs.Env }) + $Env
    $cs | Add-Member -NotePropertyName Env -NotePropertyValue $list -Force
    $r = Invoke-DockerApi -Method POST -Path "/services/$($s.ID)/update" -Query @{ version = [string]$s.Version.Index } -Body $spec
    if ($r.StatusCode -ge 400) { throw "Update-DockerServiceEnv $Service`: HTTP $($r.StatusCode): $($r.Raw)" }
}

# Synopsis: pull an image on DOCKER_HOST (through the harness's Docker Hub mirror) and write `docker save` output for it.
function Save-DockerImageArchive {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Image, [Parameter(Mandatory)][string]$OutFile)
    $repo, $tag = $Image -split ':(?=[^:/]+$)', 2
    if (-not $tag) { $tag = 'latest' }
    $p = Invoke-DockerApi -Method POST -Path '/images/create' -Query @{ fromImage = $repo; tag = $tag }
    if ($p.StatusCode -ge 400) { throw "Save-DockerImageArchive: pull $Image`: HTTP $($p.StatusCode): $($p.Raw)" }
    $null = New-Item -ItemType Directory -Force -Path (Split-Path -Parent $OutFile)
    $r = Invoke-DockerApi -Path "/images/$Image/get" -OutFile $OutFile
    if ($r.StatusCode -ge 400) { throw "Save-DockerImageArchive: save $Image`: HTTP $($r.StatusCode)" }
}

# Synopsis: write a tar archive of a directory's content (tar -cf, available on Windows 10+, Linux and macOS).
function New-TarArchive {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$SourceDir, [Parameter(Mandatory)][string]$OutFile)
    # On Windows the bsdtar in System32 first: a GNU tar earlier on PATH (Git Bash's /usr/bin/tar when the run starts
    # from bash) reads "C:\..." as host "C:" ("Cannot connect to C: resolve failed"); GNU tar gets --force-local.
    $sys = if ($IsWindows -and $env:SystemRoot) { Join-Path $env:SystemRoot 'System32\tar.exe' }
    $tarPath = if ($sys -and (Test-Path -LiteralPath $sys)) { $sys } else { (Get-Command tar -CommandType Application -ErrorAction Ignore | Select-Object -First 1).Source }
    if (-not $tarPath) { throw 'New-TarArchive: tar is not on PATH' }
    $extra = @(if ((& $tarPath --version 2>&1 | Out-String) -match 'GNU tar') { '--force-local' })
    $null = New-Item -ItemType Directory -Force -Path (Split-Path -Parent $OutFile)
    $out = & $tarPath @extra -cf $OutFile -C $SourceDir . 2>&1
    if ($LASTEXITCODE) { throw "New-TarArchive: tar failed: $out" }
}

# Synopsis: take a harness node out of the swarm for good: demote it if it is a manager, leave on its own daemon, delete the node entry.
function Remove-DockerSwarmMember {
    [CmdletBinding()]
    param(
        # Hostname (or ID) of the node, as the manager lists it.
        [Parameter(Mandatory)][string]$Node,
        # The node's own Engine endpoint, e.g. $env:DOCKER_WORKER1_HOST.
        [Parameter(Mandatory)][string]$NodeHost
    )
    $n = Get-DockerObject -Kind node -Id $Node
    if ($n -and $n.Spec.Role -eq 'manager') { Set-DockerNode -Node $Node -Role worker; Start-Sleep -Seconds 5 }
    if ((Get-SwarmState $NodeHost) -in 'active', 'pending', 'error', 'locked') {
        $null = Invoke-DockerApi -Method POST -Path '/swarm/leave' -Query @{ force = '1' } -DockerHost $NodeHost
    }
    if ($n) {
        for ($i = 0; $i -lt 10; $i++) {
            $r = Invoke-DockerApi -Method DELETE -Path "/nodes/$($n.ID)" -Query @{ force = '1' }
            if ($r.StatusCode -lt 400 -or $r.StatusCode -eq 404) { break }
            Start-Sleep -Seconds 2
        }
    }
}

# Synopsis: create a managed plugin on DOCKER_HOST from a directory holding config.json and rootfs/ (docker plugin create).
function New-DockerPluginFromDir {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$Dir)
    $tarFile = Join-Path ([IO.Path]::GetTempPath()) ("tpd-plugin-" + [IO.Path]::GetRandomFileName() + '.tar')
    try {
        New-TarArchive -SourceDir $Dir -OutFile $tarFile
        $r = Invoke-DockerApi -Method POST -Path '/plugins/create' -Query @{ name = $Name } -InFile $tarFile -ContentType 'application/x-tar'
        if ($r.StatusCode -ge 400) { throw "New-DockerPluginFromDir $Name`: HTTP $($r.StatusCode): $($r.Raw)" }
    } finally { Remove-Item -LiteralPath $tarFile -ErrorAction Ignore }
}

# Synopsis: the text of test/TfDockerTest/fixtures/<Name>.tf (a prerequisite configuration), with each key of -Values replaced by its value.
function Get-TfFixture {
    [CmdletBinding()]
    param([Parameter(Mandatory, Position = 0)][string]$Name, [Parameter(Position = 1)][System.Collections.IDictionary]$Values)
    $p = Join-Path $PSScriptRoot "fixtures/$Name.tf"
    if (-not (Test-Path -LiteralPath $p)) { throw "Get-TfFixture: no fixture $p" }
    $text = Get-Content -Raw -LiteralPath $p
    if ($Values) { foreach ($k in $Values.Keys) { $text = $text.Replace([string]$k, [string]$Values[$k]) } }
    $text
}

# ---------------------------------------------------------------------------- allowlist

# Synopsis: the rows of reference/_gen/deferred.d/examples.json (examples, or parts of them, that cannot run on the harness).
function Get-TfExampleAllowlist {
    [CmdletBinding()]
    param([string]$RepoRoot = $script:RepoRoot)
    $p = Join-Path $RepoRoot 'reference/_gen/deferred.d/examples.json'
    if (-not (Test-Path -LiteralPath $p)) { return @() }
    @((Get-Content -Raw -LiteralPath $p | ConvertFrom-Json).examples)
}
