# A user-defined bridge network with a static subnet.
resource "docker_network" "app" {
  name   = "app"
  labels = { team = "web" }

  ipam_config {
    subnet  = "10.20.0.0/24"
    gateway = "10.20.0.1"
  }
}

# A Swarm overlay network standalone containers may also join
# (requires the daemon to be a Swarm manager).
resource "docker_network" "mesh" {
  name       = "mesh"
  driver     = "overlay"
  attachable = true
}

# A swarm-scoped macvlan network: each node holds a config-only network with
# its local parent interface and subnet, and the swarm network takes its
# configuration from it (docker network create --config-only / --config-from).
resource "docker_network" "macvlan_config" {
  name        = "macvlan-config"
  driver      = "macvlan"
  config_only = true
  options     = { parent = "eth0" }

  ipam_config {
    subnet = "192.168.50.0/24"
  }
}

resource "docker_network" "macvlan" {
  name        = "macvlan"
  driver      = "macvlan"
  scope       = "swarm"
  config_from = docker_network.macvlan_config.name
}
