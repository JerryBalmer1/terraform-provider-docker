# Example test for examples/actions/docker_service_force_update, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_service_force_update' -Tag 'example', 'actions', 'swarm' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # worker2 joins the harness swarm; service web is the prerequisite.
        $replace = [ordered]@{
            'tcp://10.0.0.7:2376' = $env:DOCKER_WORKER2_HOST
            'manager_address = "10.0.0.5:2377"' = "manager_address = `"$env:E2E_MANAGER_ADDR`"`n  advertise_addr  = `"$env:E2E_ADVERTISE_ADDR`""
        }
        $pre = @'
resource "docker_service" "web" {
  name  = "web"
  image = "busybox:1.36"
  args  = ["sleep", "3600"]
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace -Vars @{ worker_join_token = (Get-DockerSwarmJoinToken) } -Prerequisite $pre `
            -Invoke 'action.docker_service_force_update.web' -Cleanup { Remove-DockerSwarmMember -Node worker2 -NodeHost $env:DOCKER_WORKER2_HOST }
    }
}
