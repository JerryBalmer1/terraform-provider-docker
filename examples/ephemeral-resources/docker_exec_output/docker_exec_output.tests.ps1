# Example test for examples/ephemeral-resources/docker_exec_output, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/ephemeral-resources/docker_exec_output' -Tag 'example', 'ephemeral-resources', 'swarm' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness -Swarm)) {
        # A container that writes the token file on start (instead of the placeholder ghcr.io/org/app image).
        $replace = @{ 'image = "ghcr.io/org/app:1.4.2"' = "image   = `"busybox:1.36`"`n  command = [`"sh`", `"-c`", `"mkdir -p /var/lib/app && echo token-123 > /var/lib/app/admin-token && sleep 3600`"]" }
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace
    }
}
