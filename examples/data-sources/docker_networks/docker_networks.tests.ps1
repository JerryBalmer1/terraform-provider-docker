# Example test for examples/data-sources/docker_networks, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_networks' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        $pre = @'
resource "docker_network" "web" {
  name   = "web-net"
  labels = { team = "web" }
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre
    }
}
