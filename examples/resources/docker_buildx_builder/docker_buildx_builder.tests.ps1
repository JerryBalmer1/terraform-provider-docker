# Example test for examples/resources/docker_buildx_builder, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_buildx_builder' -Tag 'example', 'resources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # The remote builder needs a BuildKit daemon reachable from the provider over TLS: removed (see examples.json).
        Invoke-TfExample -Path $PSScriptRoot -RemoveBlock '^resource "docker_buildx_builder" "remote"'
    }
}
