# Example test for examples/actions/docker_plugin_push, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_plugin_push' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness) -or -not $env:TF_ACC_PLUGIN_DIR) {
        # The plugin to push is created first from the harness's no-op volume plugin.
        Invoke-TfExample -Path $PSScriptRoot -Replace @{ 'registry.example.com' = $env:TF_ACC_REGISTRY } `
            -Setup { New-DockerPluginFromDir -Name "$env:TF_ACC_REGISTRY/org/myplugin:1.0" -Dir $env:TF_ACC_PLUGIN_DIR } -Invoke 'action.docker_plugin_push.myplugin'
    }
}
