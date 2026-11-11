# Example test for examples/actions/docker_node_drain, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_node_drain' -Tag 'example', 'actions', 'swarm' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'default = "worker-1"' = 'default = "worker4"' } -Invoke 'action.docker_node_drain.maintenance' `
            -Cleanup { Set-DockerNode -Node worker4 -Availability active }
    }
}
