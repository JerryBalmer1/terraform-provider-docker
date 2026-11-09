variable "registry_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

# Check credentials through the daemon (POST /auth) without storing anything.
ephemeral "docker_registry_login" "ghcr" {
  server_address = "ghcr.io"
  username       = "ci-bot"
  password       = var.registry_password
}

# The identity token, when the registry issues one, can replace the password
# in a later login. It is ephemeral, so pass it on only to ephemeral or
# write-only arguments.
locals {
  ghcr_status = ephemeral.docker_registry_login.ghcr.auth.status
}
