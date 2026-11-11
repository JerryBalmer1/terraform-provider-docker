# Example test for examples/resources/docker_image, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_image' -Tag 'example', 'resources', 'internet' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Internet) -or -not $env:TF_ACC_CONTAINERD_HOST) {
        # Runs on cdnode (containerd image store): the multi-platform and attestation builds need it.
        $files = @{
            'app/Dockerfile'  = "ARG GO_VERSION=1`nFROM busybox:1.36 AS runtime`nARG GO_VERSION`nLABEL go.version=`$GO_VERSION`n"
            'web/Dockerfile'  = "FROM busybox:1.36`nCOPY . /src`n"
            'secrets/netrc'   = "machine example.com login x password y`n"
        }
        Invoke-TfExample -Path $PSScriptRoot -Env @{ DOCKER_HOST = $env:TF_ACC_CONTAINERD_HOST; NPM_TOKEN = 'npm-test-token' } `
            -Replace @{ 'registry.example.com' = $env:TF_ACC_REGISTRY } -Files $files
    }
}
