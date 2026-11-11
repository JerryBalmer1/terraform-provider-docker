# Example test for examples/list-resources/docker_container, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/list-resources/docker_container' -Tag 'example', 'list-resources', 'query' {
    It 'lists what the prerequisite created with terraform query' -Skip:(-not (Test-TfHarness)) {
        $pre = @'
resource "docker_container" "web" {
  name    = "web-listed"
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
  labels  = { app = "web" }
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Query -QueryExpect 'web-listed' -Prerequisite $pre
    }
}
