# Example test for examples/ephemeral-resources/docker_registry_token, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/ephemeral-resources/docker_registry_token' -Tag 'example', 'ephemeral-resources', 'swarm', 'internet' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm -Internet)) {
        # An anonymous Docker Hub pull token instead of ghcr.io credentials (the provider block with them is removed).
        $replace = [ordered]@{ 'registry = "ghcr.io"' = 'registry = "docker.io"'; 'repository:org/app:pull' = 'repository:library/alpine:pull' }
        Invoke-TfExample -Path $PSScriptRoot -RemoveBlock '^provider "docker"' -Replace $replace -Vars @{ ghcr_token = 'unused' }
    }
}
