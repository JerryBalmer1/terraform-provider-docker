# Example test for examples/resources/docker_container, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/resources/docker_container' -Tag 'example', 'resources' {
    It 'applies, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # The bind source is a host path the harness node does not have: /tmp, mounted beside the default nginx config.
        # blkio_weight is dropped: the dind nodes' cgroup has no io.weight (see examples.json).
        $replace = [ordered]@{ 'source          = "/srv/web/conf.d"' = 'source          = "/tmp"'; 'target          = "/etc/nginx/conf.d"' = 'target          = "/etc/nginx/extra.d"'; 'blkio_weight = 300' = '# blkio_weight = 300' }
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace
    }
}
