# Example test for examples/actions/docker_plugin_create, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_plugin_create' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness) -or -not $env:TF_ACC_PLUGIN_DIR) {
        # ./plugin is the harness's no-op volume plugin (config.json + rootfs/).
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'registry.example.com' = $env:TF_ACC_REGISTRY } `
            -Setup { param($c) Copy-Item -LiteralPath $env:TF_ACC_PLUGIN_DIR -Destination (Join-Path $c.WorkDir 'plugin') -Recurse }
    }
}
