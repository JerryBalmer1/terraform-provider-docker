# docker_compose in local mode: three services (one scaled to 2), two networks, a volume, a secret, a config, healthcheck-gated depends_on and lifecycle hooks, plus the compose data sources and a triggered docker_compose_exec.

Run: `Invoke-Build _Up`, then `Invoke-Build _Init, _Plan, _Apply -Scenario compose_local`.

After apply, look at:

- `terraform output containers`: manualshop-web-1, manualshop-api-1, manualshop-api-2, manualshop-db-1, all running.
- `docker -H tcp://127.0.0.1:12375 ps --filter label=com.docker.compose.project=manualshop`: the labels the
  docker compose CLI would set (`com.docker.compose.service`, `...config-hash`), so `docker compose -p manualshop ps`
  shows the project too.
- `docker -H tcp://127.0.0.1:12375 exec manualshop-web-1 wget -qO- http://127.0.0.1/` prints "manualshop ok"
  (the inline config), and `exec manualshop-db-1 cat /run/secrets/db_password` prints the secret.
- `<temp>/tpd-manual/compose_local/out/nginx-t.json`: the triggered action's exit code and output. Run it again
  on demand with `Invoke-Build _Apply -Scenario compose_local -Invoke action.docker_compose_exec.check`.
- Network `manualshop_back` is internal; volume `manualshop_pgdata` carries the backup label.
- A second `_Plan` is empty. Change `replicas: 2` to 3 and `_Plan`: an in-place update of the project.

`_Destroy`, then `_Down`.
