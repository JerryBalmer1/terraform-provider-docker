# An application that writes an admin token on first start.
resource "docker_container" "app" {
  name  = "app"
  image = "ghcr.io/org/app:1.4.2"
}

# Read the token out of the container during the run; it never reaches plan
# or state. A non-zero exit code (say, the file is not there yet) fails the run.
ephemeral "docker_exec_output" "admin_token" {
  container_id = docker_container.app.id
  command      = ["cat", "/var/lib/app/admin-token"]
  timeout      = "30s"
}

# Share it with swarm services as a write-only secret.
resource "docker_secret" "admin_token" {
  name            = "app-admin-token"
  data_wo         = base64encode(trimspace(ephemeral.docker_exec_output.admin_token.stdout))
  data_wo_version = 1
}
