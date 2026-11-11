# Example test for examples/data-sources/docker_image_search, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_image_search' -Tag 'example', 'data-sources', 'internet' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Internet)) {
        Invoke-TfExample -Path $PSScriptRoot -PlanCheck IgnoreOutputs
    }
}
