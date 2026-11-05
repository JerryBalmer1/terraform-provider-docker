provider "docker" {
  registry_auth {
    address  = "ghcr.io"
    username = "ci-bot"
    password = var.ghcr_token
  }
}

variable "ghcr_token" {
  type      = string
  sensitive = true
}

# A short-lived bearer token for one repository, never stored in state.
ephemeral "docker_registry_token" "app" {
  registry = "ghcr.io"
  scope    = "repository:org/app:pull"
}

# Hand it to something that talks to the registry API directly, e.g. a
# write-only secret for a service that pulls from ghcr.io itself.
resource "docker_secret" "ghcr_token" {
  name            = "ghcr-pull-token"
  data_wo         = base64encode(ephemeral.docker_registry_token.app.token)
  data_wo_version = 1
}
