# Example test for examples/data-sources/docker_services, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_services' -Tag 'example', 'data-sources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        $pre = @'
resource "docker_service" "api" {
  name   = "shop_api"
  image  = "busybox:1.36"
  args   = ["sleep", "3600"]
  labels = { "com.docker.stack.namespace" = "shop" }
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre
    }
}
