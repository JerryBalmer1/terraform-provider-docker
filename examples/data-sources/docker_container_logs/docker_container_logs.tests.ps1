# Example test for examples/data-sources/docker_container_logs, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_container_logs' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite (Get-TfFixture container_nginx @{ NAME = 'web' }) -PlanCheck IgnoreOutputs
    }
}
