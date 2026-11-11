# Example test for examples/actions/docker_image_load, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_image_load' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # images.tar: `docker save` of busybox, written before the plan reads its hash.
        Invoke-TfExample -Path $PSScriptRoot -Setup { param($c) Save-DockerImageArchive -Image 'busybox:1.36' -OutFile (Join-Path $c.WorkDir 'images.tar') } -Invoke 'action.docker_image_load.bundle'
    }
}
