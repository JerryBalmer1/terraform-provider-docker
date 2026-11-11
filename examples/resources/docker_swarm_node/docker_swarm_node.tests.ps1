# Example test for examples/resources/docker_swarm_node, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_swarm_node' -Tag 'example', 'resources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness) -or -not $env:TF_ACC_TLS_HOST) {
        # The joining node is the harness's mutual-TLS daemon (tlsnode) with the harness client certificate.
        $replace = [ordered]@{
            'advertise_addr = "10.0.0.5"'          = "advertise_addr = `"$env:E2E_ADVERTISE_ADDR`""
            'tcp://10.0.0.6:2376'                  = $env:TF_ACC_TLS_HOST
            'manager_address = "10.0.0.5:2377"'    = "manager_address = `"$env:E2E_MANAGER_ADDR`""
            'advertise_addr  = "10.0.0.6"'         = "advertise_addr  = `"$env:E2E_ADVERTISE_ADDR`""
            'data_path_addr  = "10.1.0.6"'         = "data_path_addr  = `"$env:E2E_ADVERTISE_ADDR`""
            '~/.docker/worker1/ca.pem'             = $env:TF_ACC_TLS_CA
            '~/.docker/worker1/cert.pem'           = $env:TF_ACC_TLS_CERT
            '~/.docker/worker1/key.pem'            = $env:TF_ACC_TLS_KEY
        }
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace
    }
}
