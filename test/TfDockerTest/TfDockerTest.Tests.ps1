#Requires -Version 7.0
# Unit tests of the TfDockerTest module itself. No harness, no daemon, no terraform: Invoke-TfCli, Invoke-DockerApi
# and the snapshot functions are mocked inside the module.
#   Invoke-Pester -Path test/TfDockerTest/TfDockerTest.Tests.ps1
BeforeAll {
    Import-Module (Join-Path $PSScriptRoot 'TfDockerTest.psd1') -Force
    $script:SavedEnv = @{}
    foreach ($n in 'DOCKER_HOST', 'TF_ACC', 'TF_ACC_TERRAFORM_PATH', 'TPD_EXAMPLE_ALLOW_HOST', 'TPD_EXAMPLE_LOG_DIR', 'TPD_PROVIDER_DIR', 'TF_ACC_SKIP_INTERNET') {
        $script:SavedEnv[$n] = [Environment]::GetEnvironmentVariable($n)
    }
}
AfterAll {
    foreach ($n in $script:SavedEnv.Keys) { [Environment]::SetEnvironmentVariable($n, $script:SavedEnv[$n]) }
}

Describe 'Remove-HclBlock' {
    It 'removes a top-level block with heredocs, strings and interpolation' {
        $text = @'
resource "a" "keep" {
  x = 1
}

resource "docker_volume" "nfs" {
  name = "x${"}"}"
  content = <<-EOT
    server { listen 80; }
    }}}
  EOT
  nested {
    s = "{ not a brace"   # } comment brace
  }
}

resource "a" "after" {}
'@
        $out = Remove-HclBlock -Text $text -HeaderPattern '^resource "docker_volume" "nfs"'
        $out | Should -Not -Match 'nfs'
        $out | Should -Match 'resource "a" "keep"'
        $out | Should -Match 'resource "a" "after" \{\}'
        $out | Should -Not -Match 'listen 80'
    }
    It 'removes a nested block' {
        $text = "resource `"x`" `"y`" {`n  host = 1`n  tls {`n    ca = `"a`"`n  }`n  role = `"w`"`n}`n"
        $out = Remove-HclBlock -Text $text -HeaderPattern '^\s*tls\s*\{'
        $out | Should -Not -Match 'tls'
        $out | Should -Match 'role = "w"'
        $out | Should -Match '^resource'
    }
    It 'throws when no header matches' {
        { Remove-HclBlock -Text 'resource "a" "b" {}' -HeaderPattern '^resource "zz"' } | Should -Throw '*no block header*'
    }
}

Describe 'Edit-TfExampleText' {
    It 'applies replacements in order' {
        $r = [ordered]@{ 'registry.example.com' = '127.0.0.1:12380'; '127.0.0.1:12380/team' = 'X' }
        Edit-TfExampleText -Text 'name = "registry.example.com/team/app"' -Replace $r | Should -Be 'name = "X/app"'
    }
}

Describe 'New-TfDevOverrideConfig' {
    It 'writes dev_overrides for JerryBalmer1/docker with forward slashes' {
        $p = Join-Path $TestDrive 'dev.tfrc'
        New-TfDevOverrideConfig -ProviderDir 'C:\x\bin' -Path $p | Should -Be $p
        $c = Get-Content -Raw $p
        $c | Should -Match '"JerryBalmer1/docker" = "C:/x/bin"'
        $c | Should -Match 'direct \{\}'
    }
}

Describe 'Provider filesystem mirror' {
    It 'publishes a zip under the registry layout with its SHA256SUMS and mirror.json' {
        $src = Join-Path $TestDrive 'src'
        $null = New-Item -ItemType Directory -Force $src
        Set-Content (Join-Path $src 'terraform-provider-docker_v99.0.0') 'binary'
        $zipIn = Join-Path $TestDrive 'in.zip'
        Compress-Archive -Path (Join-Path $src '*') -DestinationPath $zipIn
        $saved = $env:TPD_PROVIDER_MIRROR
        try {
            $m = Initialize-TfProviderMirror -MirrorDir (Join-Path $TestDrive 'mirror') -ZipPath $zipIn
            $plat = Get-TfPlatform
            $m.Version | Should -Be '99.0.0'
            $m.Zip | Should -BeLike "*providers*registry.terraform.io*jerrybalmer1*docker*terraform-provider-docker_99.0.0_$plat.zip"
            Test-Path $m.Zip | Should -BeTrue
            $m.Sha256 | Should -Be (Get-FileHash -Algorithm SHA256 $zipIn).Hash.ToLowerInvariant()
            Get-Content (Join-Path $TestDrive 'mirror/terraform-provider-docker_99.0.0_SHA256SUMS') | Should -Be "$($m.Sha256)  terraform-provider-docker_99.0.0_$plat.zip"
            $env:TPD_PROVIDER_MIRROR | Should -Be (Join-Path $TestDrive 'mirror')
            # Reused through TPD_PROVIDER_MIRROR without a rebuild.
            (Initialize-TfProviderMirror).Sha256 | Should -Be $m.Sha256
        } finally { $env:TPD_PROVIDER_MIRROR = $saved }
    }
    It 'writes a filesystem_mirror CLI configuration with forward slashes' {
        $p = Join-Path $TestDrive 'm.tfrc'
        New-TfMirrorConfig -ProvidersDir 'C:\x\providers' -Path $p | Should -Be $p
        $c = Get-Content -Raw $p
        $c | Should -Match 'path    = "C:/x/providers"'
        $c | Should -Match 'include = \["registry.terraform.io/jerrybalmer1/docker"\]'
        $c | Should -Match 'exclude = \["registry.terraform.io/jerrybalmer1/docker"\]'
    }
    It 'Assert-TfLockFile accepts the mirror checksum and rejects another one or a missing lock' {
        $d = Join-Path $TestDrive 'lock'
        $null = New-Item -ItemType Directory -Force $d
        $mirror = [pscustomobject]@{ Version = '99.0.0'; Sha256 = 'abc123' }
        { Assert-TfLockFile -WorkingDir $d -Mirror $mirror } | Should -Throw '*no .terraform.lock.hcl*'
        $lock = "provider `"registry.terraform.io/jerrybalmer1/docker`" {`n  version = `"99.0.0`"`n  hashes = [`n    `"h1:xyz=`",`n    `"zh:abc123`",`n  ]`n}`n"
        Set-Content (Join-Path $d '.terraform.lock.hcl') $lock
        { Assert-TfLockFile -WorkingDir $d -Mirror $mirror } | Should -Not -Throw
        Set-Content (Join-Path $d '.terraform.lock.hcl') ($lock -replace 'zh:abc123', 'zh:other')
        { Assert-TfLockFile -WorkingDir $d -Mirror $mirror } | Should -Throw '*do not include*'
        Set-Content (Join-Path $d '.terraform.lock.hcl') ($lock -replace '99\.0\.0', '1.0.0')
        { Assert-TfLockFile -WorkingDir $d -Mirror $mirror } | Should -Throw '*another version*'
    }
}

Describe 'Assert-NoContractDrop' {
    It 'passes clean output and fails on the drop text' {
        { Assert-NoContractDrop -Output @('Apply complete!', '') -Label apply } | Should -Not -Throw
        { Assert-NoContractDrop -Output @() } | Should -Not -Throw
        { Assert-NoContractDrop -Output @('x', 'Outputs contract value dropped: docker_volume.v mountpoint') -Label plan } | Should -Throw '*plan: Outputs contract value dropped (1 line(s))*mountpoint*'
    }
}

Describe 'Get-TfTerraformPath' {
    It 'prefers TF_ACC_TERRAFORM_PATH' {
        $fake = Join-Path $TestDrive 'terraform.exe'
        Set-Content $fake 'x'
        $env:TF_ACC_TERRAFORM_PATH = $fake
        Get-TfTerraformPath | Should -Be $fake
        $env:TF_ACC_TERRAFORM_PATH = $null
    }
}

Describe 'Invoke-TfCli deadline' {
    It 'kills terraform at the example deadline and reports exit 124 with a TIMEOUT line' {
        if ($IsWindows) {
            $fake = Join-Path $TestDrive 'tf-hang.cmd'
            Set-Content $fake '@ping -n 60 127.0.0.1 > nul'
        } else {
            $fake = Join-Path $TestDrive 'tf-hang.sh'
            Set-Content $fake "#!/bin/sh`nsleep 60"
            & chmod +x $fake
        }
        $env:TF_ACC_TERRAFORM_PATH = $fake
        try {
            $sw = [Diagnostics.Stopwatch]::StartNew()
            $r = InModuleScope TfDockerTest {
                $script:TfDeadline = (Get-Date).AddSeconds(2)
                try { Invoke-TfCli -WorkingDir $TestDrive -Arguments @('apply') } finally { $script:TfDeadline = $null }
            }
            $sw.Elapsed.TotalSeconds | Should -BeLessThan 40
            $r.ExitCode | Should -Be 124
            ($r.Output -join "`n") | Should -Match 'TIMEOUT: terraform apply killed'
        } finally { $env:TF_ACC_TERRAFORM_PATH = $null }
    }
}

Describe 'Harness safety' {
    BeforeEach { $env:TPD_EXAMPLE_ALLOW_HOST = $null }
    It 'Test-TfHarness is false without TF_ACC' {
        $env:TF_ACC = $null; $env:DOCKER_HOST = 'tcp://127.0.0.1:12375'
        Test-TfHarness | Should -BeFalse
    }
    It 'Test-TfHarness is false for Docker Desktop' {
        $env:TF_ACC = '1'; $env:DOCKER_HOST = 'npipe:////./pipe/docker_engine'
        Test-TfHarness | Should -BeFalse
    }
    It 'Test-TfHarness -Internet is false when TF_ACC_SKIP_INTERNET is set' {
        $env:TF_ACC = '1'; $env:DOCKER_HOST = 'tcp://127.0.0.1:12375'; $env:TF_ACC_SKIP_INTERNET = '1'
        Test-TfHarness -Internet | Should -BeFalse
        Test-TfHarness | Should -BeTrue
        $env:TF_ACC_SKIP_INTERNET = $null
    }
    It 'Invoke-DockerApi refuses a non-harness DOCKER_HOST' {
        { Invoke-DockerApi -Path '/info' -DockerHost 'tcp://10.0.0.5:2375' } | Should -Throw '*refusing*'
    }
}

Describe 'Get-DockerObject' {
    It 'returns $null on 404 and the object otherwise' {
        Mock -ModuleName TfDockerTest Invoke-DockerApi { [pscustomobject]@{ StatusCode = 404; Content = $null; Raw = '' } } -ParameterFilter { $Path -eq '/containers/gone/json' }
        Mock -ModuleName TfDockerTest Invoke-DockerApi { [pscustomobject]@{ StatusCode = 200; Content = [pscustomobject]@{ Id = 'abc' }; Raw = '' } } -ParameterFilter { $Path -eq '/networks/n1' }
        Get-DockerObject -Kind container -Id gone | Should -BeNullOrEmpty
        (Get-DockerObject -Kind network -Id n1).Id | Should -Be 'abc'
    }
}

Describe 'Assert-TfState' {
    BeforeAll {
        $script:State = [pscustomobject]@{ values = [pscustomobject]@{ root_module = [pscustomobject]@{ resources = @(
                        [pscustomobject]@{ address = 'docker_container.web'; mode = 'managed'; type = 'docker_container'; values = [pscustomobject]@{ id = 'c1'; name = 'web'; labels = [pscustomobject]@{ team = 'web' } } }
                        [pscustomobject]@{ address = 'docker_volume.v'; mode = 'managed'; type = 'docker_volume'; values = [pscustomobject]@{ id = 'v'; name = 'v'; labels = $null } }
                        [pscustomobject]@{ address = 'data.docker_info.x'; mode = 'data'; type = 'docker_info'; values = [pscustomobject]@{} }
                        [pscustomobject]@{ address = 'terraform_data.t'; mode = 'managed'; type = 'terraform_data'; values = [pscustomobject]@{ id = 't' } }
                    ) } } }
    }
    It 'passes when the daemon agrees' {
        Mock -ModuleName TfDockerTest Invoke-DockerApi { [pscustomobject]@{ StatusCode = 200; Raw = ''; Content = [pscustomobject]@{ Id = 'c1'; Name = '/web'; Config = [pscustomobject]@{ Labels = [pscustomobject]@{ team = 'web'; 'io.terraform.provider.owner' = 'x' } } } } } -ParameterFilter { $Path -eq '/containers/c1/json' }
        Mock -ModuleName TfDockerTest Invoke-DockerApi { [pscustomobject]@{ StatusCode = 200; Raw = ''; Content = [pscustomobject]@{ Name = 'v'; Labels = $null } } } -ParameterFilter { $Path -eq '/volumes/v' }
        Assert-TfState -WorkingDir $TestDrive -State $script:State | Should -Be 2
    }
    It 'fails on a label mismatch and on a missing object' {
        Mock -ModuleName TfDockerTest Invoke-DockerApi { [pscustomobject]@{ StatusCode = 200; Raw = ''; Content = [pscustomobject]@{ Id = 'c1'; Name = '/web'; Config = [pscustomobject]@{ Labels = [pscustomobject]@{ team = 'db' } } } } } -ParameterFilter { $Path -eq '/containers/c1/json' }
        Mock -ModuleName TfDockerTest Invoke-DockerApi { [pscustomobject]@{ StatusCode = 404; Raw = ''; Content = $null } } -ParameterFilter { $Path -eq '/volumes/v' }
        { Assert-TfState -WorkingDir $TestDrive -State $script:State } | Should -Throw "*label 'team'*"
        { Assert-TfState -WorkingDir $TestDrive -State $script:State } | Should -Throw "*volume 'v' not found*"
    }
}

Describe 'Clear-DockerSnapshotDiff' {
    It 'deletes only what is new and leaves a swarm joined since the baseline' {
        $env:DOCKER_HOST = 'tcp://127.0.0.1:12375'; $env:TPD_EXAMPLE_ALLOW_HOST = $null
        $base = [pscustomobject]@{ DockerHost = 'tcp://127.0.0.1:12375'; Containers = @('old'); Networks = @('n0'); Volumes = @(); Images = @('i0'); Services = @(); Secrets = @(); Configs = @(); Plugins = @(); Nodes = @(); Swarm = [ordered]@{ 'tcp://127.0.0.1:12375' = 'inactive' } }
        $now = [pscustomobject]@{ DockerHost = 'tcp://127.0.0.1:12375'; Containers = @('old', 'new'); Networks = @('n0', 'n1'); Volumes = @('vol'); Images = @('i0'); Services = @(); Secrets = @(); Configs = @(); Plugins = @(); Nodes = @(); Swarm = [ordered]@{} }
        Mock -ModuleName TfDockerTest Get-DockerSnapshot { $now }
        Mock -ModuleName TfDockerTest Get-SwarmState { 'active' }
        Mock -ModuleName TfDockerTest Invoke-DockerApi { [pscustomobject]@{ StatusCode = 204; Raw = ''; Content = $null } }
        $removed = Clear-DockerSnapshotDiff -Baseline $base
        $removed | Should -Contain 'container new'
        $removed | Should -Contain 'network n1'
        $removed | Should -Contain 'volume vol'
        $removed | Should -Not -Contain 'container old'
        $removed | Should -Contain 'swarm membership of tcp://127.0.0.1:12375'
        Should -Invoke -ModuleName TfDockerTest Invoke-DockerApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/containers/new' } -Times 1 -Exactly
        Should -Invoke -ModuleName TfDockerTest Invoke-DockerApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/containers/old' } -Times 0 -Exactly
        Should -Invoke -ModuleName TfDockerTest Invoke-DockerApi -ParameterFilter { $Path -eq '/swarm/leave' } -Times 1 -Exactly
    }
}

Describe 'Invoke-TfExample' {
    BeforeAll {
        $script:Ex = Join-Path $TestDrive 'resources/docker_thing'
        $null = New-Item -ItemType Directory -Force $script:Ex
        Set-Content (Join-Path $script:Ex 'resource.tf') "resource `"docker_volume`" `"v`" {`n  name = `"registry.example.com`"`n}`n"
        Set-Content (Join-Path $script:Ex 'docker_thing.tests.ps1') '# not copied'
        Set-Content (Join-Path $script:Ex 'import.sh') '# not copied'
    }
    BeforeEach {
        $env:DOCKER_HOST = 'tcp://127.0.0.1:12375'; $env:TF_ACC = '1'; $env:TPD_EXAMPLE_ALLOW_HOST = $null; $env:TPD_EXAMPLE_LOG_DIR = $null
        $script:Calls = [System.Collections.Generic.List[string]]::new()
        $script:Seen = [System.Collections.Generic.List[string]]::new()
        Mock -ModuleName TfDockerTest Initialize-TfProviderMirror { [pscustomobject]@{ Providers = 'C:/fake/mirror/providers'; Version = '99.0.0'; Sha256 = 'ab' } }
        Mock -ModuleName TfDockerTest Assert-TfLockFile { }
        Mock -ModuleName TfDockerTest Get-DockerSnapshot { [pscustomobject]@{ DockerHost = $env:DOCKER_HOST } }
        Mock -ModuleName TfDockerTest Clear-DockerSnapshotDiff { @() }
        Mock -ModuleName TfDockerTest Assert-TfState { 1 }
    }
    It 'runs apply, invoke, second plan and destroy in order, on an edited copy' {
        Mock -ModuleName TfDockerTest Invoke-TfCli {
            $script:Calls.Add(($Arguments | Where-Object { $_ -notmatch '^-(input|no-color|auto-approve|backend)' }) -join ' ')
            if ($Arguments[0] -eq 'apply' -and -not ($Arguments -match '^-invoke')) {
                $script:Seen.Add((Get-ChildItem $WorkingDir -Name) -join ',')
                $script:Seen.Add((Get-Content -Raw (Join-Path $WorkingDir 'resource.tf')))
            }
            [pscustomobject]@{ ExitCode = 0; Output = @('ok') }
        }
        $r = Invoke-TfExample -Path $script:Ex -Replace @{ 'registry.example.com' = '127.0.0.1:12380' } -Files @{ 'app/Dockerfile' = 'FROM busybox' } -Invoke 'action.docker_x.y'
        $script:Calls[0] | Should -Be 'init'
        $script:Calls[1] | Should -Be 'apply'
        $script:Calls[2] | Should -Be 'apply -invoke=action.docker_x.y'
        $script:Calls[3] | Should -BeLike 'plan -detailed-exitcode -out=*'
        $script:Calls[4] | Should -Be 'destroy'
        Should -Invoke -ModuleName TfDockerTest Assert-TfLockFile -Times 1 -Exactly
        $script:Seen[0] | Should -Match '_tfexample_providers.tf'
        $script:Seen[0] | Should -Match 'app'
        $script:Seen[0] | Should -Not -Match 'tests.ps1|import.sh'
        $script:Seen[1] | Should -Match '127.0.0.1:12380'
        $r.StateChecked | Should -Be 1
        Should -Invoke -ModuleName TfDockerTest Clear-DockerSnapshotDiff -Times 1 -Exactly
        Test-Path $r.WorkDir | Should -BeFalse
    }
    It 'destroys, sweeps and reports the error line when apply fails' {
        Mock -ModuleName TfDockerTest Invoke-TfCli {
            $script:Calls.Add($Arguments[0])
            if ($Arguments[0] -eq 'apply') { return [pscustomobject]@{ ExitCode = 1; Output = @('noise', 'Error: container create failed', 'detail line') } }
            [pscustomobject]@{ ExitCode = 0; Output = @() }
        }
        { Invoke-TfExample -Path $script:Ex } | Should -Throw '*apply failed (exit 1): Error: container create failed*'
        $script:Calls | Should -Contain 'destroy'
        Should -Invoke -ModuleName TfDockerTest Clear-DockerSnapshotDiff -Times 1 -Exactly
    }
    It 'fails on a non-empty second plan unless only outputs changed and PlanCheck is IgnoreOutputs' {
        Mock -ModuleName TfDockerTest Invoke-TfCli {
            if ($Arguments[0] -eq 'plan') { return [pscustomobject]@{ ExitCode = 2; Output = @() } }
            if ($Arguments[0] -eq 'show') { return [pscustomobject]@{ ExitCode = 0; Output = @('{"resource_changes":[{"address":"data.x.y","change":{"actions":["read"]}}],"output_changes":{"o":{"actions":["update"]}}}') } }
            [pscustomobject]@{ ExitCode = 0; Output = @() }
        }
        { Invoke-TfExample -Path $script:Ex } | Should -Throw '*second plan is not empty: output.o*'
        { Invoke-TfExample -Path $script:Ex -PlanCheck IgnoreOutputs } | Should -Not -Throw
    }
    It 'fails on resource drift even with IgnoreOutputs' {
        Mock -ModuleName TfDockerTest Invoke-TfCli {
            if ($Arguments[0] -eq 'plan') { return [pscustomobject]@{ ExitCode = 2; Output = @() } }
            if ($Arguments[0] -eq 'show') { return [pscustomobject]@{ ExitCode = 0; Output = @('{"resource_changes":[{"address":"docker_volume.v","change":{"actions":["update"]}}]}') } }
            [pscustomobject]@{ ExitCode = 0; Output = @() }
        }
        { Invoke-TfExample -Path $script:Ex -PlanCheck IgnoreOutputs } | Should -Throw '*docker_volume.v (update)*'
    }
    It 'ValidateOnly runs validate and never touches the daemon' {
        $env:DOCKER_HOST = $null
        Mock -ModuleName TfDockerTest Invoke-TfCli { $script:Calls.Add($Arguments[0]); [pscustomobject]@{ ExitCode = 0; Output = @() } }
        Invoke-TfExample -Path $script:Ex -ValidateOnly | Out-Null
        $script:Calls -join ',' | Should -Be 'init,validate'
        Should -Invoke -ModuleName TfDockerTest Get-DockerSnapshot -Times 0 -Exactly
    }
    It 'applies the prerequisite first and destroys it last' {
        Mock -ModuleName TfDockerTest Invoke-TfCli { $script:Calls.Add("$(Split-Path -Leaf $WorkingDir):$($Arguments[0])"); [pscustomobject]@{ ExitCode = 0; Output = @() } }
        Invoke-TfExample -Path $script:Ex -Prerequisite 'resource "docker_network" "n" { name = "n" }' -PlanCheck Skip | Out-Null
        $script:Calls -join ',' | Should -Be 'work:init,prereq:init,prereq:apply,work:apply,work:destroy,prereq:destroy'
    }
    It 'runs terraform query for list resources and checks QueryExpect' {
        Mock -ModuleName TfDockerTest Invoke-TfCli { $script:Calls.Add($Arguments[0]); [pscustomobject]@{ ExitCode = 0; Output = @('list.docker_volume.unused   id=data') } }
        Invoke-TfExample -Path $script:Ex -Query -QueryExpect 'id=data' | Out-Null
        $script:Calls -join ',' | Should -Be 'init,query'
        { Invoke-TfExample -Path $script:Ex -Query -QueryExpect 'id=other' } | Should -Throw '*does not match*'
    }
    It 'rejects a Replace key or RemoveBlock pattern the example does not contain' {
        Mock -ModuleName TfDockerTest Invoke-TfCli { [pscustomobject]@{ ExitCode = 0; Output = @() } }
        { Invoke-TfExample -Path $script:Ex -Replace @{ 'ghcr.io' = 'x' } } | Should -Throw "*Replace key 'ghcr.io'*"
        { Invoke-TfExample -Path $script:Ex -RemoveBlock '^resource "zz"' } | Should -Throw '*RemoveBlock*'
    }
    It 'refuses to apply against a non-harness DOCKER_HOST' {
        $env:DOCKER_HOST = 'npipe:////./pipe/docker_engine'
        Mock -ModuleName TfDockerTest Invoke-TfCli { [pscustomobject]@{ ExitCode = 0; Output = @() } }
        { Invoke-TfExample -Path $script:Ex } | Should -Throw '*refusing*'
        Should -Invoke -ModuleName TfDockerTest Get-DockerSnapshot -Times 0 -Exactly
    }
    It 'fails when terraform output carries the outputs contract drop text, and still destroys' {
        Mock -ModuleName TfDockerTest Invoke-TfCli {
            $script:Calls.Add($Arguments[0])
            if ($Arguments[0] -eq 'apply') { return [pscustomobject]@{ ExitCode = 0; Output = @('Warning: Outputs contract value dropped: docker_container.c inspect.State.Health') } }
            [pscustomobject]@{ ExitCode = 0; Output = @() }
        }
        { Invoke-TfExample -Path $script:Ex -PlanCheck Skip } | Should -Throw '*apply: Outputs contract value dropped*docker_container.c*'
        $script:Calls | Should -Contain 'destroy'
    }
    It 'writes the mirror CLI configuration into the copy' {
        Mock -ModuleName TfDockerTest Invoke-TfCli {
            if ($Arguments[0] -eq 'init') { $script:Seen.Add([string]$env:TF_CLI_CONFIG_FILE); $script:Seen.Add((Get-Content -Raw $env:TF_CLI_CONFIG_FILE)) }
            [pscustomobject]@{ ExitCode = 0; Output = @() }
        }
        Invoke-TfExample -Path $script:Ex -PlanCheck Skip | Out-Null
        $script:Seen[0] | Should -BeLike '*mirror.tfrc'
        $script:Seen[1] | Should -Match 'filesystem_mirror'
        $script:Seen[1] | Should -Match 'C:/fake/mirror/providers'
        $script:Seen[1] | Should -Not -Match 'dev_overrides'
    }
    It 'restores environment variables it set' {
        $env:TPD_SOME = 'before'
        Mock -ModuleName TfDockerTest Invoke-TfCli { [pscustomobject]@{ ExitCode = 0; Output = @() } }
        Invoke-TfExample -Path $script:Ex -Env @{ TPD_SOME = 'during' } -PlanCheck Skip | Out-Null
        $env:TPD_SOME | Should -Be 'before'
        $env:TPD_SOME = $null
    }
}

Describe 'Get-TfExampleAllowlist' {
    It 'reads reference/_gen/deferred.d/examples.json' {
        $root = Join-Path $TestDrive 'repo'
        $null = New-Item -ItemType Directory -Force (Join-Path $root 'reference/_gen/deferred.d')
        Set-Content (Join-Path $root 'reference/_gen/deferred.d/examples.json') '{"examples":[{"example":"resources/docker_x","reason":"r"}]}'
        (Get-TfExampleAllowlist -RepoRoot $root).example | Should -Be 'resources/docker_x'
    }
}
