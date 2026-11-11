# Example test for examples/actions/docker_service_rollback, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_service_rollback' -Tag 'example', 'actions', 'swarm' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # Service web gets a second spec (an extra env variable) so there is one to roll back from.
        $pre = @'
resource "docker_service" "web" {
  name  = "web"
  image = "busybox:1.36"
  args  = ["sleep", "3600"]
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre -Setup { Update-DockerServiceEnv -Service web -Env 'ROLLOUT=2'; Start-Sleep -Seconds 3 } `
            -Vars @{ rollback_reason = 'bad release' } -Invoke 'action.docker_service_rollback.web'
    }
}
