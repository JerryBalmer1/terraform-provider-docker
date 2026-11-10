---
page_title: "Container state: running, created, exited"
subcategory: ""
description: |-
  How docker_container keeps a container running, created or exited, how one-shot runs work, and how existing state moves to the state argument.
---

# Container state: running, created, exited

`docker_container` has one lifecycle argument, `state`. Its values are Docker's own `State.Status` names, so what you
write is what `docker container inspect` shows.

| `state` | At create | On a later refresh / apply | Typical use |
|---|---|---|---|
| `running` (default) | created and started (`wait` can wait for `healthy`) | found stopped: the plan shows `running: false -> true` and the apply starts it **in place** (no replacement) | services, databases |
| `created` | created only, never started by the provider | found running: the plan **warns** and changes nothing | containers started by something else (the `docker_container_start` action, a supervisor) |
| `exited` | created, started and waited for until it exits, within `wait_timeout` (default `5m`) | never started again; a missing container is planned for creation | migrations, seed jobs, one-shot tasks you want to keep for inspection |

Switching between `running` and `created` is applied in place. A change to or from `exited` forces a new container:
a one-shot run is never converted.

## One-shot runs (`state = "exited"`)

```terraform
resource "docker_container" "migrate" {
  name                = "migrate"
  image               = "ghcr.io/org/migrate:2.1.0"
  command             = ["migrate", "up"]
  state               = "exited"
  expected_exit_codes = [0]   # default
  wait_timeout        = "15m" # default 5m for exited
  logs_tail           = 1000  # default
}
```

- The run is checked once, when it finishes. An exit code outside `expected_exit_codes` fails the apply; the
  container is kept (tainted) so you can inspect it, and the next apply replaces it.
- Outputs: `exit_code`, `finished_at`, and the run's output as `logs` (text) and `log_entries` (one object per line,
  `timestamp` / `stream` / `source` / `message`), the last `logs_tail` lines. `logs` and `log_entries.message` are
  sensitive. They are captured at create; a refresh keeps them.
- A later refresh never runs the container again. Change its configuration (or `terraform apply -replace`) to run it
  again; if the container was removed outside Terraform, the next plan creates it.
- `auto_remove = true` with `state = "exited"` is a validation error, and so are `restart` policies other than `no`
  and `wait = true`. To run once and remove the container, use the `docker_container_run` action, which captures
  `exit_code`, `stdout` and `stderr` in its result (`result_path`) and then removes the container.

## How refresh reads the configuration

Arguments come from the container's `Config` and `HostConfig`, never from runtime state such as `NetworkSettings`
or `State`. A stopped container therefore plans with no diff: its `ports` come from `HostConfig.PortBindings`, not from
the published ports a stopped container no longer has. Values the daemon fills in itself (for example a default
`Config.StopTimeout`) are not reported for arguments you did not set.

## Moving existing state

State of this provider's own `docker_container` (schema version 0) is upgraded automatically: `must_run` is removed
and `state` becomes `running`.

| Before | After |
|---|---|
| `must_run = true` (the old default) | `state = "running"` (the default) |
| `must_run = false` | no exact counterpart: `state = "created"` never starts the container; with `state = "running"` a stopped container is started again. Choose one and remove `must_run` from the configuration. |

A `moved` block from kreuzwerker/docker sets `state` from kreuzwerker's arguments:

| kreuzwerker/docker | `state` |
|---|---|
| `start = false` | `created` |
| `attach = true` | `exited` |
| otherwise | `running` |
| `must_run` | dropped (subsumed by `running`) |
| `logs`, `container_logs` | dropped; with `state = "exited"` the run's output is in `logs` / `log_entries` |
