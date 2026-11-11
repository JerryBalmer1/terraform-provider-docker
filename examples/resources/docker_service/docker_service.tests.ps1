# Example test for examples/resources/docker_service, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_service' -Tag 'example', 'resources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # Overlay networks frontend and backend come from the prerequisite.
        $pre = @'
resource "docker_network" "frontend" {
  name       = "frontend"
  driver     = "overlay"
  attachable = true
}

resource "docker_network" "backend" {
  name   = "backend"
  driver = "overlay"
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre
    }
}
