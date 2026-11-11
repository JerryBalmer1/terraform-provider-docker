# One docker_container with most of its schema: ports, volume and tmpfs mounts, files, two networks with a static IP, healthcheck, limits, logging.

Run: `Invoke-Build _Up`, then `Invoke-Build _Init, _Plan, _Apply -Scenario container_full`.

After apply, look at:

- `terraform output`: `web_ip_front` is `10.77.0.10`; `web_health` turns `healthy` within a few seconds
  (re-run `_Plan`, which refreshes); `web_processes` lists nginx master and workers.
- `docker -H tcp://127.0.0.1:12375 inspect manual-web`: `HostConfig.Ulimits`, `HostConfig.Memory` 268435456,
  `HostConfig.NanoCpus`, `HostConfig.CapDrop` NET_RAW, `HostConfig.Init` true, `HostConfig.Sysctls`,
  `Config.Labels` (scenario, tier), `HostConfig.Annotations`, `NetworkSettings.Networks` (manual-front with
  aliases www/web and 10.77.0.10, manual-back internal).
- `docker -H tcp://127.0.0.1:12375 exec manual-web cat /etc/nginx/conf.d/headers.conf`: the `files` block.
- `docker -H tcp://127.0.0.1:12375 logs manual-web`: the `99-banner.sh` line from the second `files` block.
- `manual-once` is an exited container (exit 0); `terraform output once_logs` shows its line.
- A second `_Plan` is empty. Change `memory` and `_Plan` again: an in-place update, no replacement.

`_Destroy`, then `_Down`.
