# Example test for examples/data-sources/docker_dockerfile, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/data-sources/docker_dockerfile' -Tag 'example', 'data-sources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # ./app/Dockerfile is a two-stage build; the history example reads busybox instead of nginx:latest.
        $files = @{ 'app/Dockerfile' = "FROM busybox:1.36 AS build`nRUN echo hi > /hi`n`nFROM busybox:1.36 AS runtime`nCOPY --from=build /hi /hi`n" }
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'registry.example.com' = $env:TF_ACC_REGISTRY; 'image = "nginx:latest"' = 'image = "busybox:1.36"' } -Files $files `
            -Prerequisite 'resource "docker_image" "busybox" { name = "busybox:1.36" }'
    }
}
