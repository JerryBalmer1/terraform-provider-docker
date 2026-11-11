# Example test for examples/data-sources/docker_registry_image, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_registry_image' -Tag 'example', 'data-sources', 'internet' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Internet)) {
        Invoke-TfExample -Path $PSScriptRoot -RemoveBlock '^data "docker_registry_image" "internal"'
    }
}
