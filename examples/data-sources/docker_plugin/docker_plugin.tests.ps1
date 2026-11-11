# Example test for examples/data-sources/docker_plugin, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_plugin' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness) -or -not $env:TF_ACC_PLUGIN) {
        # The harness's no-op volume plugin, installed under the alias the example reads.
        $pre = @'
resource "docker_plugin" "p" {
  name  = "PLUGIN"
  alias = "sshfs"
}
'@.Replace('PLUGIN', $env:TF_ACC_PLUGIN)
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre
    }
}
