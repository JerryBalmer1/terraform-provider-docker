# terraform-provider-docker

One Terraform provider for the whole Docker chain, built on
`terraform-plugin-framework` and the Docker Engine API (`github.com/docker/docker/client`).
No compose files, no `docker` CLI.

It is organised in four layers: images, single-host primitives (networks,
volumes, containers), Swarm topology (init, node joins) and Swarm workload
(secrets, configs, services, stacks). Every resource and data source, grouped
by layer with a one-line description and links to its skill reference and
docs page, is listed in [README_resources.md](README_resources.md). That file
is generated from the provider schema, so it always matches the build.

## Agent skill

A Claude skill generated from the provider schema lives in
[skills/terraform-provider-docker/SKILL.md](skills/terraform-provider-docker/SKILL.md).
It covers the four layers, their dependency order, the attributes that
force replacement (read from the resources' `RequiresReplace` plan
modifiers), and a worked example that goes from a cold start to a
three-replica service across two nodes. `make skill` / `./build.ps1 -Task skill`
regenerates it together with README_resources.md, and `docs` runs `skill` first.

## Provider configuration

```hcl
provider "docker" {
  host = "tcp://10.0.0.5:2376" # default unix:///var/run/docker.sock, or DOCKER_HOST

  tls {
    ca   = file("~/.docker/ca.pem")
    cert = file("~/.docker/cert.pem")
    key  = file("~/.docker/key.pem")
  }

  registry_auth {
    address     = "ghcr.io"
    config_file = "~/.docker/config.json"
  }
}
```

## Build on Windows (build.ps1)

`make` is not needed on Windows. [build.ps1](build.ps1) (PowerShell 7) runs the
same commands as the GNUmakefile from any working directory. Tasks run in the
order given, comma separated:

```powershell
./build.ps1                                   # build (default): provider binary in the repo root
./build.ps1 -Task build,vet,test              # build, go vet, unit tests (no daemon)
./build.ps1 -Task skill                       # regenerate the skill and README_resources.md
./build.ps1 -Task docs                        # skill, then tfplugindocs generate + validate
./build.ps1 -Task check                       # gofmt, vet, genskill -check, genskill -strict, tfplugindocs validate
./build.ps1 -Task fmt                         # gofmt -w .
./build.ps1 -Task install                     # go install (dev_overrides path)
./build.ps1 -Task e2e                         # test/harness/Invoke-E2E.ps1: dind manager + 2 workers, TestAccE2E
./build.ps1 -Task testacc                     # every TestAcc* against the same harness (60m timeout)
pwsh -NoProfile -File build.ps1 -Task check   # from cmd.exe or another shell
```

`e2e` and `testacc` need Docker Desktop (or another Docker host) running with
privileged containers allowed; see [test/harness/README.md](test/harness/README.md).

## Build on Linux/macOS (make)

```sh
make build     # provider binary
make vet       # go vet ./...
make test      # unit tests (no daemon)
make skill     # skill + README_resources.md
make docs      # skill + tfplugindocs generate/validate
make check     # gofmt, vet, genskill -check, genskill -strict, tfplugindocs validate
make testacc   # acceptance tests against DOCKER_HOST
make e2e       # dind manager + 2 workers (test/harness), full end-to-end run
make fmt       # gofmt -w .
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs `make build`,
`go vet`, `make test` and `make check` on every push to main and pull request,
then `make e2e` against the dind harness.

`genskill -strict` fails when a resource attribute or block has a
`RequiresReplace` plan modifier but its description does not say so; add
"Forces replacement." to its `MarkdownDescription`.

See [BUILD_REPORT.md](BUILD_REPORT.md) for the current build state.
