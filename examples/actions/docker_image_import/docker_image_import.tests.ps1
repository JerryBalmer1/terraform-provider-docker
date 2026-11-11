# Example test for examples/actions/docker_image_import, on the dind harness (Invoke-Build ExampleTests). Module: test/TfDockerTest.
BeforeDiscovery { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }
BeforeAll { Import-Module (Join-Path $PSScriptRoot '../../../test/TfDockerTest') -Force }

Describe 'examples/actions/docker_image_import' -Tag 'example', 'actions' {
    It 'applies (triggering the action), invokes it, re-plans empty and destroys' -Skip:(-not (Test-TfHarness)) {
        # rootfs.tar: a one-file root filesystem, written before the plan reads its hash.
        $setup = {
            param($c)
            $src = Join-Path $c.Root 'rootfs'
            $null = New-Item -ItemType Directory -Force (Join-Path $src 'etc')
            Set-Content -LiteralPath (Join-Path $src 'etc/hello') -Value 'imported by the example test'
            New-TarArchive -SourceDir $src -OutFile (Join-Path $c.WorkDir 'rootfs.tar')
        }
        Invoke-TfExample -Path $PSScriptRoot -Setup $setup -Invoke 'action.docker_image_import.rootfs'
    }
}
