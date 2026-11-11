# Example test for examples/data-sources/docker_registry_image_tags, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_registry_image_tags' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # A repository in the harness registry with a v-prefixed tag.
        $pre = @'
resource "docker_image" "busybox" {
  name = "REGISTRY/busybox:1.36"
}

resource "docker_tag" "v1" {
  source_image = docker_image.busybox.id
  target_image = "REGISTRY/tpd-example/app:v1.0.0"
}

resource "docker_registry_image" "v1" {
  name       = docker_tag.v1.target_image
  depends_on = [docker_tag.v1]
}
'@.Replace('REGISTRY', $env:TF_ACC_REGISTRY)
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'ghcr.io/org/app' = "$env:TF_ACC_REGISTRY/tpd-example/app" } -Prerequisite $pre
    }
}
