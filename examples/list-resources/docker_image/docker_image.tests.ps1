# Example test for examples/list-resources/docker_image, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/list-resources/docker_image' -Tag 'example', 'list-resources', 'query' {
    It 'lists what the prerequisite created with terraform query' -Skip:(-not (Test-TfHarness)) {
        Invoke-TfExample -Path $PSScriptRoot -Query -QueryExpect 'nginx' -Prerequisite 'resource "docker_image" "nginx" { name = "nginx:1.27-alpine" }'
    }
}
