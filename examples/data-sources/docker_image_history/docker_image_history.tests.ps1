# Example test for examples/data-sources/docker_image_history, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_image_history' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite 'resource "docker_image" "nginx" { name = "nginx:1.27" }'
    }
}
