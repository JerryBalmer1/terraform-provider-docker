resource "docker_secret" "db_password" {
  name = "db_password"
  data = base64encode("change-me")

  # Labels update in place (SecretUpdate); a new data value replaces the secret.
  labels = {
    app = "shop"
  }
}

# The Engine's inspect body, e.g. for a downstream tool.
output "db_password_secret" {
  value = docker_secret.db_password.inspect.spec.name
}

# Terraform 1.11+: a write-only secret. The content never reaches plan or
# state, so it can come from an ephemeral value (an `ephemeral = true`
# variable, ephemeral.docker_exec_output, ...); bump data_wo_version to rotate
# it (the secret is replaced: swarm secret content is immutable).
resource "docker_secret" "api_key" {
  name            = "api_key_v1"
  data_wo         = base64encode("replace-with-an-ephemeral-value")
  data_wo_version = 1
}

# A templated secret: the golang templating driver expands it per task.
resource "docker_secret" "per_task" {
  name       = "per_task_id"
  data       = base64encode("{{ .Service.Name }}-{{ .Task.Slot }}")
  templating = { name = "golang" }
}
