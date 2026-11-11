# Example test for examples/data-sources/docker_buildx_history, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_buildx_history' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # One BuildKit build with a provenance attestation, so "^0" exists.
        $pre = @{
            'main.tf'        = @'
resource "docker_image" "history" {
  name = "tpd-example/history:1"
  build {
    context    = "${path.module}/ctx"
    builder    = "buildkit"
    provenance = "min"
  }
}
'@
            'ctx/Dockerfile' = "FROM busybox:1.36`nRUN echo built > /built`n"
        }
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre
    }
}
