# Example test for examples/functions/parse_image_ref, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/functions/parse_image_ref' -Tag 'example', 'functions' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        Invoke-TfExample -Path $PSScriptRoot
    }
}
