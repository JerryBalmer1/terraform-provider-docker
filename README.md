# terraform-provider-docker

One Terraform provider for the whole Docker chain, built on
`terraform-plugin-framework` and the Docker Engine API (`github.com/docker/docker/client`).
No compose files at runtime, no `docker` CLI.

It is organised in four layers: images, single-host primitives (networks,
volumes, containers), Swarm topology (init, node joins) and Swarm workload
(secrets, configs, services, stacks). Besides resources and data sources it
registers Terraform 1.10+ kinds: actions (Terraform >= 1.14, e.g.
`docker_exec`, `docker_service_scale`), ephemeral resources (>= 1.10),
provider functions (>= 1.8) and list resources for `terraform query`
(>= 1.14). Every registered type, grouped by layer with a one-line
description and links to its skill reference and docs page, is listed in
[README_resources.md](README_resources.md). That file is generated from the
provider schema, so it always matches the build.

## Agent skill

A Claude skill generated from the provider schema lives in
[.claude/skills/terraform-provider-docker/SKILL.md](.claude/skills/terraform-provider-docker/SKILL.md)
(Claude Code loads skills from `.claude/skills/` automatically).
It covers the four layers, their dependency order, the attributes that
force replacement (read from the resources' `RequiresReplace` plan
modifiers), write-only `*_wo` arguments, when to use actions, ephemeral
resources, provider functions and list resources, and two worked examples:
a cold start to a three-replica service across two nodes, and a compose file
converted with `tools/compose2stack` into a `docker_stack` that is seeded and
scaled with actions. `make skill` / `./build.ps1 -Task skill` regenerates it
together with README_resources.md, and `docs` runs `skill` first.

## Migrating a compose file

`tools/compose2stack` converts an existing `docker-compose.yml` into HCL once;
the generated `.tf` is the source of truth afterwards:

```sh
go run ./tools/compose2stack -in docker-compose.yml -out stack.tf -mode swarm
make compose2stack IN=docker-compose.yml OUT=stack.tf MODE=swarm
./build.ps1 -Task compose2stack -In docker-compose.yml -Out stack.tf -Mode swarm
```

## Parity with kreuzwerker/docker

`go run ./tools/parity` (`make parity`, `./build.ps1 -Task parity`) prints
every kreuzwerker/docker v4.6.0 type and whether this provider has it, has it
partially or is missing it, plus the types only this provider has. The list
is maintained by hand in [tools/parity/parity.go](tools/parity/parity.go).
CI runs `go run ./tools/parity -check BUILD_REPORT.md`, which fails when a
type marked missing has no `[BLOCKED]` line naming it in BUILD_REPORT.md.

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
./build.ps1 -Task parity                      # kreuzwerker v4.6.0 parity table
./build.ps1 -Task compose2stack -In docker-compose.yml -Out stack.tf -Mode swarm
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
make parity    # kreuzwerker v4.6.0 parity table
make compose2stack IN=docker-compose.yml OUT=stack.tf MODE=swarm
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs `make build`,
`go vet`, `make test`, `make check` and `go run ./tools/parity -check
BUILD_REPORT.md` on every push to main and pull request, then `make e2e`
against the dind harness. The e2e job is advisory until
`test/harness/CI_GREEN_RUNS` contains an integer >= 2; from then on a failing
e2e run fails the workflow.

`genskill -strict` fails when a resource attribute or block has a
`RequiresReplace` plan modifier but its description does not say so (add
"Forces replacement." to its `MarkdownDescription`), or when an action,
ephemeral resource, list resource or provider function, or one of its
attributes, blocks or parameters, has no description.

tfplugindocs templates for every kind live in [templates/](templates/)
(`resources`, `data-sources`, `actions`, `ephemeral-resources`, `functions`,
`list-resources`); examples go in `examples/actions/<type>/action.tf`,
`examples/ephemeral-resources/<type>/ephemeral-resource.tf`,
`examples/functions/<name>/function.tf` and
`examples/list-resources/<type>/list-resource.tfquery.hcl`.

See [BUILD_REPORT.md](BUILD_REPORT.md) for the current build state.
