# Example test for examples/resources/docker_swarm_init, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_swarm_init' -Tag 'example', 'resources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        $replace = [ordered]@{ 'advertise_addr    = "10.0.0.5"' = "advertise_addr    = `"$env:E2E_ADVERTISE_ADDR`""; 'data_path_addr = "10.1.0.5"' = "data_path_addr = `"$env:E2E_ADVERTISE_ADDR`"" }
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace
    }
}
