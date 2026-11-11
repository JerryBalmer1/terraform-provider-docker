# Example test for examples/resources/docker_volume, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_volume' -Tag 'example', 'resources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm) -or -not $env:TF_ACC_CSI_PLUGIN) {
        # The cluster volume uses the harness's CSI plugin (enabled by harness-swarm-init).
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'driver = "csi-driver-nfs"' = "driver = `"$env:TF_ACC_CSI_PLUGIN`"" }
    }
}
