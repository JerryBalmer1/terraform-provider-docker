# Example test for examples/list-resources/docker_swarm_node, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/list-resources/docker_swarm_node' -Tag 'example', 'list-resources', 'swarm', 'query' {
    It 'lists what the prerequisite created with terraform query' -Skip:(-not (Test-TfHarness -Swarm)) {
        Invoke-TfExample -Path $PSScriptRoot -Query -QueryExpect 'worker4'
    }
}
