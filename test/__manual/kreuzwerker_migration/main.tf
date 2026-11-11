# kreuzwerker_migration: state written by kreuzwerker/docker v4.6.0 (a fixture; prepare.ps1 creates the Docker
# objects it describes and writes it as terraform.tfstate) moved into JerryBalmer1/docker with cross-provider moved
# blocks (Terraform >= 1.8), as templates/guides/migrating-from-kreuzwerker.md.tmpl describes. The first plan must
# show three moves and no create, replace or destroy. Plain harness; terraform init downloads kreuzwerker/docker
# 4.6.0 from the registry (the state names it), so it needs the internet.
terraform {
  required_version = ">= 1.8"
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
    # Only while the state still names kreuzwerker: pins the version that wrote it.
    kreuzwerker = {
      source  = "kreuzwerker/docker"
      version = "4.6.0"
    }
  }
}

provider "docker" {}

moved {
  from = docker_network.app
  to   = docker_network.app_moved
}

resource "docker_network" "app_moved" {
  name   = "kw-app"
  driver = "bridge"
  labels = { env = "prod" }

  ipam_config {
    subnet  = "10.79.0.0/24"
    gateway = "10.79.0.1"
  }
}

moved {
  from = docker_volume.data
  to   = docker_volume.data_moved
}

resource "docker_volume" "data_moved" {
  name   = "kw-data"
  driver = "local"
  labels = { "com.example.team" = "ops" }
}

moved {
  from = docker_image.busybox
  to   = docker_image.busybox_moved
}

resource "docker_image" "busybox_moved" {
  name         = "busybox:1.36"
  keep_locally = true
}

output "moved_ids" {
  value = {
    network = docker_network.app_moved.id
    volume  = docker_volume.data_moved.id
    image   = docker_image.busybox_moved.id
  }
}
