# Example test for examples/data-sources/docker_service, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_service' -Tag 'example', 'data-sources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        $pre = @'
resource "docker_service" "web" {
  name  = "web"
  image = "nginx:1.27-alpine"
  endpoint_spec {
    ports {
      target_port    = 80
      published_port = 8080
    }
  }
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre
    }
}
