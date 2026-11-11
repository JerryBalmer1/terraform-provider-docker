# Example test for examples/data-sources/docker_registry_image_manifests, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_registry_image_manifests' -Tag 'example', 'data-sources', 'internet' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Internet)) {
        # The second lookup reads busybox from the harness registry instead of ghcr.io/org/app.
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'ghcr.io/org/app:1.0' = "$env:TF_ACC_REGISTRY/busybox:1.36" }
    }
}
