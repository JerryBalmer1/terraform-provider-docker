# Example test for examples/actions/docker_compose_build, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_compose_build' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'registry.example.com' = $env:TF_ACC_REGISTRY } -Files @{ 'app/Dockerfile' = "FROM busybox:1.36`n" } -Invoke 'action.docker_compose_build.this'
    }
}
