data "docker_info" "host" {}

output "daemon" {
  value = {
    id             = data.docker_info.host.id
    name           = data.docker_info.host.name
    cpus           = data.docker_info.host.info.ncpu
    storage_driver = data.docker_info.host.info.driver
    swarm_state    = data.docker_info.host.info.swarm.local_node_state
  }
}
