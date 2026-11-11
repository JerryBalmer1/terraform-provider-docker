# Example test for examples/actions/docker_node_activate, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_node_activate' -Tag 'example', 'actions', 'swarm' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # worker4 is the harness's standby worker (joined by harness-swarm-init).
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'default = "worker-1"' = 'default = "worker4"' } -Invoke 'action.docker_node_activate.back'
    }
}
