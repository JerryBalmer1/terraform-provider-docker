terraform {
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
  }
}

# Local daemon (DOCKER_HOST or unix:///var/run/docker.sock).
provider "docker" {}

# Remote daemon over TLS with registry credentials.
provider "docker" {
  alias = "remote"
  host  = "tcp://10.0.0.5:2376"

  tls {
    ca   = "~/.docker/remote/ca.pem"
    cert = "~/.docker/remote/cert.pem"
    key  = "~/.docker/remote/key.pem"
  }

  registry_auth {
    address  = "ghcr.io"
    username = "octocat"
    password = var.ghcr_token
  }
}

variable "ghcr_token" {
  type      = string
  sensitive = true
}
