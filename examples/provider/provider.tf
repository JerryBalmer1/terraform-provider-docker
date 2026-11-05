terraform {
  required_version = ">= 1.14.0"

  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
  }
}

# Local daemon: DOCKER_HOST, else the current Docker CLI context, else
# unix:///var/run/docker.sock.
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

# The same TLS host with cert_path: a directory holding ca.pem, cert.pem and
# key.pem. cert_path and the tls block are mutually exclusive.
provider "docker" {
  alias     = "remote_certs"
  host      = "tcp://10.0.0.5:2376"
  cert_path = "~/.docker/remote"
}

# A Docker CLI context created with `docker context create staging ...`.
# Host and TLS material come from ~/.docker/contexts (or DOCKER_CONFIG).
provider "docker" {
  alias   = "staging"
  context = "staging"
}

# SSH in pure Go (no ssh binary): one connection, reused for every call and
# forwarded to the remote daemon's unix socket, here through a bastion.
provider "docker" {
  alias = "ssh"
  host  = "ssh://deploy@10.0.1.20"

  ssh {
    identity_file    = "~/.ssh/id_ed25519"
    identity_agent   = true
    known_hosts_file = "~/.ssh/known_hosts"
    proxy_jump       = "deploy@bastion.example.com:22"
    connect_timeout  = "15s"
    remote_socket    = "/var/run/docker.sock"
  }

  # Skip the plan-time ping (it only ever warns) when the host may be down.
  disable_docker_daemon_check = true
}

# Registry credentials from docker config.json files, including credsStore /
# credHelpers (Docker Desktop's "desktop" helper, ecr-login, ...). Running
# docker-credential-* helpers must be allowed explicitly.
provider "docker" {
  alias                    = "helpers"
  allow_credential_helpers = true

  registry_auth {
    address     = "registry-1.docker.io"
    config_file = "~/.docker/config.json"
  }

  registry_auth {
    address             = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
    config_file_content = var.docker_config_json
  }

  registry_auth {
    address       = "public.ecr.aws"
    auth_disabled = true
  }
}

variable "ghcr_token" {
  type      = string
  sensitive = true
}

variable "docker_config_json" {
  type      = string
  sensitive = true
  default   = "{\"credHelpers\": {\"123456789012.dkr.ecr.eu-west-1.amazonaws.com\": \"ecr-login\"}}"
}
