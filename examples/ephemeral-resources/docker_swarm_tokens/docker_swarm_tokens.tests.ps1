# Example test for examples/ephemeral-resources/docker_swarm_tokens, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/ephemeral-resources/docker_swarm_tokens' -Tag 'example', 'ephemeral-resources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        Invoke-TfExample -Path $PSScriptRoot
    }
}
