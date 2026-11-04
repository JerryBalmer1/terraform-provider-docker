# The provider points at a swarm manager; each docker_swarm_node reaches the
# joining machine through its own Docker endpoint.
resource "docker_swarm_init" "this" {
  advertise_addr = "10.0.0.5"
}

resource "docker_swarm_node" "worker1" {
  host            = "tcp://10.0.0.6:2376"
  join_token      = docker_swarm_init.this.worker_join_token
  manager_address = "10.0.0.5:2377"
  advertise_addr  = "10.0.0.6"

  tls {
    ca   = "~/.docker/worker1/ca.pem"
    cert = "~/.docker/worker1/cert.pem"
    key  = "~/.docker/worker1/key.pem"
  }

  availability = "active"
  role         = "worker"
  labels = {
    zone = "a"
  }
}
