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
