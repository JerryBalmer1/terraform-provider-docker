# Example test for examples/actions/docker_compose_stop, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_compose_stop' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # docker_compose keeps its containers running, so the second plan restarts the stopped workers.
        Invoke-TfExample -Path $PSScriptRoot -Invoke 'action.docker_compose_stop.this' -PlanCheck Any
    }
}
