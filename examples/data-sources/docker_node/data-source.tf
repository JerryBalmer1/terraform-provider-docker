data "docker_node" "worker1" {
  hostname = "worker1"
}

output "worker1_state" {
  value = data.docker_node.worker1.state
}

# The whole Engine node object is under inspect.
output "worker1_resources" {
  value = data.docker_node.worker1.inspect.description.resources
}
