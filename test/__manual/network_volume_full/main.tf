# network_volume_full: docker_network and docker_volume with every setting the harness manager supports,
# read back through the plural data sources. Plain harness (no swarm needed).
terraform {
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
  }
}

provider "docker" {}
resource "docker_network" "app" {
  name        = "manual-app"
  driver      = "bridge"
  attachable  = true
  ipam_driver = "default"
  labels      = { scenario = "network_volume_full", team = "web" }
  options = {
    "com.docker.network.bridge.name"                 = "br-manual"
    "com.docker.network.bridge.enable_icc"           = "true"
    "com.docker.network.driver.mtu"                  = "1450"
  }

  ipam_config {
    subnet      = "10.78.0.0/24"
    ip_range    = "10.78.0.128/25"
    gateway     = "10.78.0.1"
    aux_address = { reserved = "10.78.0.2" }
  }
}

resource "docker_network" "internal" {
  name     = "manual-internal"
  internal = true
  labels   = { scenario = "network_volume_full" }
}

resource "docker_volume" "data" {
  name   = "manual-data"
  labels = { scenario = "network_volume_full", team = "web" }
}

resource "docker_volume" "tmpfs" {
  name   = "manual-tmpfs"
  driver = "local"
  driver_opts = {
    type   = "tmpfs"
    device = "tmpfs"
    o      = "size=64m,uid=1000"
  }
}

# A container using both, so the volume is in use and the network has an endpoint.
resource "docker_container" "probe" {
  name    = "manual-probe"
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
  mounts {
    type   = "volume"
    source = docker_volume.data.name
    target = "/data"
  }
  networks = {
    (docker_network.app.name) = { ipv4_address = "10.78.0.200" }
  }
}

data "docker_networks" "team" {
  labels     = { team = "web" }
  depends_on = [docker_network.app]
}

data "docker_volumes" "team" {
  labels     = { team = "web" }
  depends_on = [docker_volume.data]
}

data "docker_network" "app" {
  name       = docker_network.app.name
  depends_on = [docker_container.probe]
}

output "team_networks" {
  value = data.docker_networks.team.names
}

output "team_volumes" {
  value = data.docker_volumes.team.names
}

output "app_subnet" {
  value = data.docker_network.app.ipam_config[0].subnet
}

output "data_mountpoint" {
  value = docker_volume.data.mountpoint
}
