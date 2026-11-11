# Example test for examples/ephemeral-resources/docker_registry_login, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/ephemeral-resources/docker_registry_login' -Tag 'example', 'ephemeral-resources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # Logs in to the harness registry (no authentication: the daemon reports success).
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'server_address = "ghcr.io"' = "server_address = `"$env:TF_ACC_REGISTRY`"" } -Vars @{ registry_password = 'unused' }
    }
}
