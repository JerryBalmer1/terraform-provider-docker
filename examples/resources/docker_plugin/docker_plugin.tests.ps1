# Example test for examples/resources/docker_plugin, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_plugin' -Tag 'example', 'resources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness) -or -not $env:TF_ACC_PLUGIN) {
        # The harness's no-op volume plugin instead of vieux/sshfs (FUSE does not work inside dind).
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'name  = "vieux/sshfs:latest"' = "name  = `"$env:TF_ACC_PLUGIN`"" }
    }
}
