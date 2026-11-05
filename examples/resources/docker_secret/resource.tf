resource "docker_secret" "db_password" {
  name = "db_password"
  data = base64encode("change-me")

  labels = {
    app = "shop"
  }
}

# Terraform 1.11+: a write-only secret. The content never reaches plan or
# state, so it can come from an ephemeral value (an `ephemeral = true`
# variable, ephemeral.docker_exec_output, ...); bump data_wo_version to rotate
# it (the secret is replaced, swarm secrets are immutable).
resource "docker_secret" "api_key" {
  name            = "api_key_v1"
  data_wo         = base64encode("replace-with-an-ephemeral-value")
  data_wo_version = 1
}
