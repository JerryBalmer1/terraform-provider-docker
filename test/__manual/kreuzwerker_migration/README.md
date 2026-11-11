# A kreuzwerker/docker v4.6.0 state (network, volume, image) moved into JerryBalmer1/docker with cross-provider moved blocks, without touching the Docker objects.

Run: `Invoke-Build _Up`, then `Invoke-Build _Init -Scenario kreuzwerker_migration`. `_Init` runs `prepare.ps1`
first: it creates network `kw-app`, volume `kw-data` and pulls `busybox:1.36` on the harness manager, then writes
`kreuzwerker-state.json.tmpl` filled with their IDs as the working copy's `terraform.tfstate`, every resource
recorded under `registry.terraform.io/kreuzwerker/docker` at kreuzwerker's schema version. `terraform init` then
installs kreuzwerker/docker 4.6.0 from the registry (the state names it; needs the internet) beside the
dev_overrides build of this provider.

Then `Invoke-Build _Plan -Scenario kreuzwerker_migration` and look at:

- Three lines "has moved to": `docker_network.app` -> `docker_network.app_moved`, `docker_volume.data` ->
  `docker_volume.data_moved`, `docker_image.busybox` -> `docker_image.busybox_moved`.
- No create, replace or destroy. In-place updates are possible where kreuzwerker stored an SDKv2 empty value this
  provider keeps as null (the guide explains them); a replacement means the moved state and the configuration
  disagree on a "Forces replacement." attribute, which is a finding.
- `_Apply`: only the moves (and any in-place update) are applied; `terraform output moved_ids` shows the same
  network ID that prepare.ps1 printed. `terraform state show docker_network.app_moved` (in
  `<temp>/tpd-manual/kreuzwerker_migration`): labels are a map now, ipam_config kept.
- A second `_Plan` is empty.

`_Destroy` removes the three objects through this provider, then `_Down`.
