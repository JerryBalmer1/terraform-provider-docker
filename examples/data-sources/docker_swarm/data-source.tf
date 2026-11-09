data "docker_swarm" "this" {}

output "swarm_id" {
  value = data.docker_swarm.this.id
}

output "data_path_port" {
  value = data.docker_swarm.this.inspect.data_path_port
}
