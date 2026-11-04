# Join tokens of the swarm the provider's host manages.
data "docker_swarm_tokens" "current" {}

resource "docker_swarm_node" "worker2" {
  host            = "tcp://10.0.0.7:2375"
  join_token      = data.docker_swarm_tokens.current.worker
  manager_address = "10.0.0.5:2377"
}
