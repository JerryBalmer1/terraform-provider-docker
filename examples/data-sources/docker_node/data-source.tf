data "docker_node" "worker1" {
  hostname = "worker1"
}

output "worker1_state" {
  value = data.docker_node.worker1.state
}
