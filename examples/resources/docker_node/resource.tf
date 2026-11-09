# Manage a node that already joined the swarm (for example with
# `docker swarm join` outside Terraform). The provider must point at a
# manager. Destroying this resource only forgets the node: it stays in the
# swarm with its current labels, availability and role.
resource "docker_node" "worker1" {
  node_id      = "worker1" # node ID or hostname
  availability = "active"
  labels = {
    zone = "a"
    disk = "ssd"
  }
}

output "worker1_addr" {
  value = docker_node.worker1.addr
}
