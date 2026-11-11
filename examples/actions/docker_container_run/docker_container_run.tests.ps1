# Example test for examples/actions/docker_container_run, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_container_run' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # Placeholders: a real image for the migration, the default bridge, and postgres allowed to start without a password.
        $replace = [ordered]@{
            'ghcr.io/org/migrate:2.1.0' = 'busybox:1.36'
            'command = ["migrate", "up"]' = 'command = ["sh", "-c", "echo migrate up"]'
            'network_mode        = "app"' = 'network_mode        = "bridge"'
            'image = "postgres:17"' = "image = `"postgres:17`"`n  env   = { POSTGRES_HOST_AUTH_METHOD = `"trust`" }"
        }
        Invoke-TfExample -Path $PSScriptRoot -Replace $replace -Invoke 'action.docker_container_run.migrate'
    }
}
