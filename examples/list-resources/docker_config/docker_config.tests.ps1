# Example test for examples/list-resources/docker_config, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/list-resources/docker_config' -Tag 'example', 'list-resources', 'swarm', 'query' {
    It 'lists what the prerequisite created with terraform query' -Skip:(-not (Test-TfHarness -Swarm)) {
        Invoke-TfExample -Path $PSScriptRoot -Query -QueryExpect 'nginx-site.conf' -Prerequisite (Get-TfFixture config @{ NAME = 'nginx-site.conf' })
    }
}
