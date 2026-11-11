# docker_network (bridge with IPAM and driver options, internal) and docker_volume (labels, tmpfs driver options), read back by the plural data sources.

Run: `Invoke-Build _Up`, then `Invoke-Build _Init, _Plan, _Apply -Scenario network_volume_full`.

After apply, look at:

- `terraform output`: `team_networks` = `["manual-app"]`, `team_volumes` = `["manual-data"]`, `app_subnet` = `10.78.0.0/24`.
- `docker -H tcp://127.0.0.1:12375 network inspect manual-app`: `IPAM.Config` (subnet, ip range, gateway,
  `AuxiliaryAddresses`), `Options` (bridge name br-manual, mtu 1450), `Labels`, `Attachable` true, and the
  `manual-probe` endpoint at 10.78.0.200.
- `docker -H tcp://127.0.0.1:12375 volume inspect manual-tmpfs`: `Options` type/device/o.
- A second `_Plan` is empty. Edit a label of `manual-app` and `_Plan`: labels force replacement on networks
  (the Engine cannot relabel a network), while a volume label change also replaces.

`_Destroy`, then `_Down`.
