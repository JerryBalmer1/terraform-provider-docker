# Example test for examples/list-resources/docker_service, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/list-resources/docker_service' -Tag 'example', 'list-resources', 'swarm', 'query' {
    It 'lists what the prerequisite created with terraform query' -Skip:(-not (Test-TfHarness -Swarm)) {
        $pre = @'
resource "docker_service" "api" {
  name   = "shop_listed"
  image  = "busybox:1.36"
  args   = ["sleep", "3600"]
  labels = { "com.docker.stack.namespace" = "shop" }
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Query -QueryExpect 'shop_listed' -Prerequisite $pre
    }
}
