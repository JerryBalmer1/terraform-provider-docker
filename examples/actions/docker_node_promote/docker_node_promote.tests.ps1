# Example test for examples/actions/docker_node_promote, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_node_promote' -Tag 'example', 'actions', 'swarm' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # worker1 joins the harness swarm and is promoted; the cleanup demotes it and removes it again.
        $replace = [ordered]@{
            'tcp://10.0.0.8:2376' = $env:DOCKER_WORKER1_HOST
            'manager_address = "10.0.0.5:2377"' = "manager_address = `"$env:E2E_MANAGER_ADDR`"`n  advertise_addr  = `"$env:E2E_ADVERTISE_ADDR`""
        }
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace -Vars @{ worker_join_token = (Get-DockerSwarmJoinToken) } `
            -Cleanup { Remove-DockerSwarmMember -Node worker1 -NodeHost $env:DOCKER_WORKER1_HOST }
    }
}
