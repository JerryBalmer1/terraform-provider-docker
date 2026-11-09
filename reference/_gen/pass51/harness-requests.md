# Pass 5.1 harness requests (Lane P -> Lane H)

Lane P (ComposeParity coverage). Written at Lane P's start; updated at its end.

## Requests

1. **Nice to have, not blocking: keep the Docker Hub mirror on the manager.**
   TestAccComposeParity pulls `busybox:1.36`, `busybox:latest`, `nginx:1.27`,
   `nginx:1.27-alpine`, `nginx:latest`, `postgres:16`, `postgres:16-alpine`,
   `postgres:latest`, `redis:7`, `redis:7-alpine` on the manager daemon
   (DOCKER_HOST=tcp://127.0.0.1:12375). If your image seeding preloads these
   into the manager (or the mirror), the parity run is faster; it works without.

2. **No harness change needed for the fake / missing images, external networks
   or host files.** The parity test seeds them itself on the manager daemon
   through the Engine API before each sub-case (aliases such as
   `ghcr.io/example/app:1.0` and `registry.example.com/sink/*:2.1.0` are
   committed locally from busybox / nginx; external networks `traefik_public`,
   `shared_ext` and the volume `legacy_data` are created; bind / file secret
   sources are written under `/srv/compose-parity/<case>/` on the manager's
   filesystem with a helper container). Please do not wipe `/srv` or
   prune images on the manager between `harness-up` and the test run.

3. **IPv6 on the manager's dind daemon** (kitchen_sink: networks with
   `enable_ipv6: true` and `enable_ipv4: false`). If the manager's dockerd
   cannot create IPv6 bridge networks, Lane P will report it; nothing to do
   unless Lane P updates this file with a concrete request below.

## Updates at Lane P's end

No harness change is required by Lane P. With the current harness (unchanged
by Lane P), TestAccComposeParity runs all 13 sub-cases; the setup in
internal/provider/p_compose_parity_test.go covers images, external networks /
volumes and host files on the manager. IPv6 networks (kitchen_sink) work on
the manager's dind daemon as is. Request 1 (preloading the listed images)
stays optional, for speed only.
