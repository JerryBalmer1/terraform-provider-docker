# Example test for examples/data-sources/docker_swarm_tokens, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_swarm_tokens' -Tag 'example', 'data-sources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # worker2 joins the harness swarm with the token the data source reads.
        $replace = [ordered]@{
            'tcp://10.0.0.7:2375' = $env:DOCKER_WORKER2_HOST
            'manager_address = "10.0.0.5:2377"' = "manager_address = `"$env:E2E_MANAGER_ADDR`"`n  advertise_addr  = `"$env:E2E_ADVERTISE_ADDR`""
        }
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace -Cleanup { Remove-DockerSwarmMember -Node worker2 -NodeHost $env:DOCKER_WORKER2_HOST }
    }
}
