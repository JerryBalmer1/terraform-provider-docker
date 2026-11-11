# Example test for examples/provider, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../test/TfDockerTest') -Force }

Describe 'examples/provider' -Tag 'example', 'provider' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # Only provider configurations: nothing is created; the aliases are never configured.
        Invoke-TfExample -Path $PSScriptRoot -Vars @{ ghcr_token = 'unused' }
    }
}
