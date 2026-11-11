# Example test for examples/actions/docker_service_scale, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_service_scale' -Tag 'example', 'actions', 'swarm' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        $pre = @'
resource "docker_service" "queue_worker" {
  name  = "queue-worker"
  image = "busybox:1.36"
  args  = ["sleep", "3600"]
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre -Invoke 'action.docker_service_scale.worker'
    }
}
