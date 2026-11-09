# Metadata only: the Engine never returns secret data.
data "docker_secret" "db_password" {
  name = "db_password"
}

output "db_password_secret_id" {
  value = data.docker_secret.db_password.id
}
