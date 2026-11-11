# Example test for examples/data-sources/docker_image_attestations, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_image_attestations' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness) -or -not $env:TF_ACC_CONTAINERD_HOST) {
        # Runs on cdnode (containerd image store): the prerequisite builds an image with an SBOM attestation.
        $pre = @{
            'main.tf'        = @'
resource "docker_image" "app" {
  name = "tpd-example/attested:1.0"
  build {
    context = "${path.module}/ctx"
    builder = "buildkit"
    sbom    = true
  }
}
'@
            'ctx/Dockerfile' = "FROM busybox:1.36`nRUN echo attested > /attested`n"
        }
        Invoke-TfExample -Path $PSScriptRoot -Env @{ DOCKER_HOST = $env:TF_ACC_CONTAINERD_HOST } -Replace @{ 'ghcr.io/example/app:1.0' = 'tpd-example/attested:1.0' } -Prerequisite $pre
    }
}
