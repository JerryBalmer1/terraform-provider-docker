# Example test for examples/actions/docker_container_stop, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_container_stop' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # state = running (the default) starts the stopped container again, so the second plan shows that update.
        Invoke-TfExample -Path $PSScriptRoot -Invoke 'action.docker_container_stop.app' -PlanCheck Any
    }
}
