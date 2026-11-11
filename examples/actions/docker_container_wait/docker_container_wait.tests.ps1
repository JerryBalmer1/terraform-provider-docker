# Example test for examples/actions/docker_container_wait, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_container_wait' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # busybox's default command (sh) exits 0 at once.
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'ghcr.io/org/migrate:2.1.0' = 'busybox:1.36' } -Invoke 'action.docker_container_wait.migrate'
    }
}
