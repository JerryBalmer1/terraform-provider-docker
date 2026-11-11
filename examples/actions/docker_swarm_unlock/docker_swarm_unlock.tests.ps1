# Example test for examples/actions/docker_swarm_unlock, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_swarm_unlock' -Tag 'example', 'actions', 'swarm' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # The harness manager is not locked: the action must leave it alone.
        Invoke-TfExample -Path $PSScriptRoot -Vars @{ swarm_unlock_key = 'SWMKEY-1-not-locked' } -Invoke 'action.docker_swarm_unlock.manager'
    }
}
