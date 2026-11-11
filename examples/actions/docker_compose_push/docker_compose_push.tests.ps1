# Example test for examples/actions/docker_compose_push, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_compose_push' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # The pushed image must exist locally: the prerequisite tags busybox as <registry>/app:1.0.
        $pre = @'
resource "docker_image" "busybox" {
  name = "busybox:1.36"
}

resource "docker_tag" "app" {
  source_image = docker_image.busybox.id
  target_image = "REGISTRY/app:1.0"
}
'@.Replace('REGISTRY', $env:TF_ACC_REGISTRY)
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'registry.example.com' = $env:TF_ACC_REGISTRY } -Prerequisite $pre -Invoke 'action.docker_compose_push.this'
    }
}
