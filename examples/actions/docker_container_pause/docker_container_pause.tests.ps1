# Example test for examples/actions/docker_container_pause, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_container_pause' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # No second -invoke: the trigger already paused the container and the Engine refuses to pause it twice.
        Invoke-TfExample -Path $PSScriptRoot
    }
}
