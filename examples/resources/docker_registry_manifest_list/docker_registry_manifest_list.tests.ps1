# Example test for examples/resources/docker_registry_manifest_list, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_registry_manifest_list' -Tag 'example', 'resources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # The two single-platform images are busybox, tagged under both names.
        $pre = @'
resource "docker_image" "busybox" {
  name = "REGISTRY/busybox:1.36"
}

resource "docker_tag" "amd64" {
  source_image = docker_image.busybox.id
  target_image = "REGISTRY/team/app:1.0-amd64"
}

resource "docker_tag" "arm64" {
  source_image = docker_image.busybox.id
  target_image = "REGISTRY/team/app:1.0-arm64"
}
'@.Replace('REGISTRY', $env:TF_ACC_REGISTRY)
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'registry.example.com' = $env:TF_ACC_REGISTRY } -Prerequisite $pre
    }
}
