# Example test for examples/data-sources/docker_config, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_config' -Tag 'example', 'data-sources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite (Get-TfFixture config @{ NAME = 'nginx-default.conf' })
    }
}
