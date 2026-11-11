# Actions on a container and an image: docker_container_cp, docker_exec, docker_container_kill and docker_image_save run on create; pause, unpause, restart, export, run and prune on demand.

Run: `Invoke-Build _Up`, then `Invoke-Build _Init, _Plan, _Apply -Scenario actions_tour`.

After apply, look at (working copy `<temp>/tpd-manual/actions_tour`):

- `out/exec.json`: stdout of `docker_exec` shows the `/etc/motd` written by `docker_container_cp` first, then
  "hello from docker_exec" and the uid.
- `docker -H tcp://127.0.0.1:12375 logs manual-box`: "got HUP" from `docker_container_kill`.
- `out/busybox.tar` and `out/save.json`: `docker_image_save`.

Then run actions on demand (nothing else changes; the plan shows only the action):

```
Invoke-Build _Apply -Scenario actions_tour -Invoke action.docker_container_pause.box
docker -H tcp://127.0.0.1:12375 ps --filter name=manual-box   # (Paused)
Invoke-Build _Apply -Scenario actions_tour -Invoke action.docker_container_unpause.box
Invoke-Build _Apply -Scenario actions_tour -Invoke action.docker_container_restart.box
Invoke-Build _Apply -Scenario actions_tour -Invoke action.docker_container_export.box   # out/box-rootfs.tar
Invoke-Build _Apply -Scenario actions_tour -Invoke action.docker_container_run.oneshot  # out/run.json, container removed
Invoke-Build _Apply -Scenario actions_tour -Invoke action.docker_image_prune.dangling
Invoke-Build _Apply -Scenario actions_tour -Invoke action.docker_container_prune.stopped # out/container-prune.json
```

A second `_Plan` is empty: triggered actions run only on create.

`_Destroy`, then `_Down`.
