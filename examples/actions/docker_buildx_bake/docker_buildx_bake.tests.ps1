# Example test for examples/actions/docker_buildx_bake, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_buildx_bake' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # Pushes to the harness registry; one platform only (the harness daemons have no arm64 emulation).
        $replace = [ordered]@{ 'registry.example.com' = $env:TF_ACC_REGISTRY; 'platforms = ["linux/amd64", "linux/arm64"]' = 'platforms = ["linux/amd64"]' }
        $files = @{ 'app/Dockerfile' = "FROM busybox:1.36`n"; 'base.Dockerfile' = "FROM busybox:1.36`nRUN echo base > /base`n"; 'worker/Dockerfile' = "FROM base`n" }
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace -Files $files -Invoke 'action.docker_buildx_bake.release'
    }
}
