# Example test for examples/data-sources/docker_container_file, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_container_file' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        $pre = @'
resource "docker_container" "app" {
  name    = "app"
  image   = "busybox:1.36"
  command = ["sh", "-c", "mkdir -p /var/lib/app && echo listen=8080 > /var/lib/app/generated.conf && sleep 3600"]
}
'@
        Invoke-TfExample -Path $PSScriptRoot -Prerequisite $pre
    }
}
