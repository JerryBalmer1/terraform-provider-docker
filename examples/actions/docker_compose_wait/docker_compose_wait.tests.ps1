# Example test for examples/actions/docker_compose_wait, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_compose_wait' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # The worker exits after 2 s instead of an hour, so the wait returns.
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'sleep 3600' = 'sleep 2' } -Invoke 'action.docker_compose_wait.this'
    }
}
