# Example test for examples/actions/docker_compose_scale, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_compose_scale' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # The next apply scales back to the configured count, so the second plan shows that update.
        Invoke-TfExample -Path $PSScriptRoot -Invoke 'action.docker_compose_scale.this' -PlanCheck Any
    }
}
