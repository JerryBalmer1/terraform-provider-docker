# Example test for examples/actions/docker_container_export, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_container_export' -Tag 'example', 'actions', 'internet' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Internet)) {
        Invoke-TfExample -Path $PSScriptRoot -Invoke 'action.docker_container_export.rootfs'
    }
}
