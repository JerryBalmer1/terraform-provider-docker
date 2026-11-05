# End-to-end test harness

A throwaway three-node swarm built from Docker-in-Docker (`docker:28-dind`):
one manager and two workers on a user-defined bridge network `swarmnet`. It
exists only to run `TestAccE2EImageToService` in
`internal/provider/e2e_test.go`, which drives the whole chain through the
provider: image pull, swarm init, overlay network, two worker joins, secret,
and a 3-replica service spread across the workers.

The daemons listen on plain TCP **without TLS**, published on `127.0.0.1`
only. That is acceptable for a local, disposable harness and nothing else.

## Requirements

- A Docker host that allows `privileged` containers (Docker Desktop or Linux
  Docker Engine) with the `docker compose` v2 plugin.
- Outbound internet from the dind daemons (they pull `nginx:1.27-alpine`).
- Free host ports 12375, 12376, 12377.
- `bash`, `make`, Go, and Terraform CLI on `PATH` (terraform-plugin-testing
  runs the real `terraform` binary, or downloads one if `TF_ACC_TERRAFORM_PATH`
  is not set and network access is available).

## Run

From the repository root:

```sh
make e2e
```

This runs `harness-up`, the test, then `harness-down` (even if the test
fails), and exits with the test's status.

Manual sequence (useful to keep the harness up between runs):

```sh
bash test/harness/scripts/harness-up.sh
source test/harness/.env
TF_ACC=1 go test ./internal/provider/ -run TestAccE2E -count=1 -v -timeout 30m
bash test/harness/scripts/harness-down.sh
```

Both scripts can be run from any working directory.

## Windows (PowerShell 7)

`make` is not needed. PowerShell equivalents use the same compose file and
write the same `.env` (`export NAME=value`, LF line endings, so bash
`source test/harness/.env` still works):

| Script | Does |
|---|---|
| `test/harness/scripts/harness-up.ps1` | Fails fast with a clear message if `docker info` fails; otherwise `docker compose up -d --wait`, waits for the three published endpoints, writes `.env`, and sets the same variables in the current session (`$env:DOCKER_HOST`, ...). |
| `test/harness/scripts/harness-down.ps1` | `docker compose down -v --remove-orphans`, deletes `.env` and clears the variables (even if compose fails). |
| `test/harness/Invoke-E2E.ps1` | One-shot wrapper: up, then `TF_ACC=1` plus the hosts from `.env`, then `go test ./internal/provider/ -run <Run> -count=1 -v -timeout <Timeout>` from the repo root, with the full output tee'd to `-Log`, then down in a `finally` block. Exits with go test's exit code. |

`Invoke-E2E.ps1` parameters: `-Run` (default `TestAccE2E`), `-Timeout`
(default `30m`), `-Log` (default `test/harness/last-e2e.log`, resolved
against the repository root), and `-KeepUp` (leave the harness running).
If the Docker daemon is not reachable it prints a one-line error and exits 1
before touching compose or `.env`.

From the repository root:

```powershell
# the image -> service chain (same as `make e2e`)
pwsh -NoProfile -File test/harness/Invoke-E2E.ps1

# every acceptance test against the harness
pwsh -NoProfile -File test/harness/Invoke-E2E.ps1 -Run TestAcc -Timeout 60m -Log test/harness/last-acc.log
```

`build.ps1 -Task e2e` and `build.ps1 -Task testacc` at the repository root
call this script with those arguments.

Manual sequence (the harness stays up between runs):

```powershell
./test/harness/scripts/harness-up.ps1      # also sets $env:DOCKER_HOST etc.
$env:TF_ACC = '1'
go test ./internal/provider/ -run TestAccE2E -count=1 -v -timeout 30m
./test/harness/scripts/harness-down.ps1
```

Run the `.ps1` scripts in the current session (`./script.ps1` or `& ...`), not
with `pwsh -File`, if you want the `$env:` variables to stay set afterwards.

## Environment written to `test/harness/.env`

| Variable | Value | Meaning |
|---|---|---|
| `DOCKER_HOST` | `tcp://127.0.0.1:12375` | The manager daemon. The provider (configured from the environment) and the test's check functions talk to this. |
| `DOCKER_WORKER1_HOST` | `tcp://127.0.0.1:12376` | worker1's daemon, used as `docker_swarm_node.host` so the provider can make it join. |
| `DOCKER_WORKER2_HOST` | `tcp://127.0.0.1:12377` | worker2's daemon, as above. |
| `E2E_MANAGER_ADDR` | `manager:2377` | The swarm address the workers dial to join. It resolves inside `swarmnet`, not from the host. |
| `E2E_ADVERTISE_ADDR` | `eth0` | Interface each node advertises. dind containers also have `docker0`, so swarm needs an explicit choice. |

The test skips cleanly unless `TF_ACC`, `DOCKER_HOST`, `DOCKER_WORKER1_HOST`,
`DOCKER_WORKER2_HOST` and `E2E_MANAGER_ADDR` are all set.

## Cleanup

`bash test/harness/scripts/harness-down.sh` runs `docker compose down -v`
(containers, network, and the per-node `/var/lib/docker` volumes) and deletes
`.env`. If a run is interrupted mid-test, running it is always safe.
