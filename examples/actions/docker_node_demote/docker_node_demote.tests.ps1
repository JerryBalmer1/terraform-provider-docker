# Example test for examples/actions/docker_node_demote, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_node_demote' -Tag 'example', 'actions', 'swarm' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # worker4 is promoted first, so there is a second manager to demote.
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'default = "manager-3"' = 'default = "worker4"' } `
            -Setup { Set-DockerNode -Node worker4 -Role manager; Start-Sleep -Seconds 5 } `
            -Cleanup { Set-DockerNode -Node worker4 -Role worker -Availability active }
    }
}
