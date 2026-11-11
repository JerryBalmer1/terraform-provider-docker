# Example test for examples/data-sources/docker_containers, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_containers' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        $pre = @'
resource "docker_container" "shop" {
  name    = "shop-api"
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
  labels  = { "com.example.project" = "shop" }
}
'@
        # The output carries each container's status ("Up N seconds"), which changes between plans.
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre -PlanCheck IgnoreOutputs
    }
}
