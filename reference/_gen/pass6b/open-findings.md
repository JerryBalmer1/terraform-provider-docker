# Pass 6b open findings

All five findings below are closed in Pass 6c; the current list is [../pass6c/open-findings.md](../pass6c/open-findings.md).

Findings this pass found but did not close, each with what reproduces it. Everything handed to Lane Z that is not
listed here was fixed (BUILD_REPORT.md, Pass 6b).

## 1. `docker_exec.stdin` is not sensitive on the action

terraform-plugin-framework v1.19 action schema attributes cannot be `Sensitive`, and `WriteOnly` is rejected for
actions, so the value shows in plan output (never in logs or `result_path`). `docker_exec_output.stdin` (ephemeral)
is sensitive. The attribute description says so. Reproduce:

```hcl
action "docker_exec" "x" {
  config {
    container_id = docker_container.db.id
    command      = ["sh", "-c", "cat > /tmp/in"]
    stdin        = "secret"
  }
}
```

`terraform plan -invoke=action.docker_exec.x` prints `stdin = "secret"`. Closes when the framework supports
sensitive action attributes; until then secrets belong in `docker_exec_output` or a mounted secret.

## 2. Generated config for a global `docker_service` (unverified)

`replicas` with `mode = "global"` is now a validation error (it is ignored by the Engine; the docker CLI rejects it,
cli/command/service/opts.go:630/:666 at v29.9.3). The validator reads only the configuration, so a default never
triggers it, but a config written by `terraform plan -generate-config-out` from an imported global service, or by
`terraform query -generate-config-out` from the docker_service list resource, may contain `replicas = 1` and then
fail validation. Reproduce: `docker service create --mode global --name g nginx:alpine` on the harness, an `import`
block for `docker_service.g`, `terraform plan -generate-config-out=g.tf`, then `terraform plan`.

## 3. Harness Terraform 1.14.9 fails `terraform init` under dev_overrides

Lane E: with test/harness/.bin/terraform_1.14.9 (TF_ACC_TERRAFORM_PATH) and a dev_overrides CLI config for
JerryBalmer1/docker, `terraform init` fails for this provider; terraform 1.17.1 works. The example runner skips init
for the overridden provider and the manual tasks use the terraform on PATH. Reproduce: `$env:TF_CLI_CONFIG_FILE=<dev
override config>; & $env:TF_ACC_TERRAFORM_PATH init` in any examples/resources/<type> copy. Either raise
HARNESS_TF_VERSION or find the 1.14 init incompatibility.

## 4. Preflight diagnostics not covered on every path

Lane V made permission-denied and TLS failures carry the Configure preflight diagnostic on every Engine operation
(client.go, preflight.go). Not covered: hijacked exec/attach streams over TLS, TLS through an HTTP proxy, a TLS 1.3
client-certificate rejection (reported after the handshake), plain HTTP sent to a TLS port, and SSH hosts. Each
still surfaces the raw client error. Reproduce the plain-HTTP case: `host = "tcp://127.0.0.1:12378"` (the harness TLS
node) with no `cert_path`, then any resource.

## 5. Remaining `compose v5.1.4` citations in provider code comments

The parity rules (CONTRACT.md, the parity test, c_compose_refs.go, compose_local_apply.go), every fieldmap.d skip and
the compose:models partials now cite v5.7.0. Comments in compose_hooks.go, compose_local.go, compose_local_k.go,
compose_build.go, compose_publish_k.go, pass53_partial_k.go, action_compose_images.go (oci push media type) and
z_compose_endpoint_test.go still cite v5.1.4 file:line pairs; several of those files moved in v5.7.0
(convergence.go -> reconcile.go / executor_ops.go / service_containers.go). The behaviour they describe was not
re-verified line by line this pass.
