# Example test for examples/data-sources/docker_tasks, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_tasks' -Tag 'example', 'data-sources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'node_id = "worker1"' = 'node_id = "worker4"' } -Prerequisite (Get-TfFixture service_web)
    }
}
