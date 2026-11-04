data "docker_nodes" "managers" {
  role = "manager"
}

output "manager_hostnames" {
  value = [for n in data.docker_nodes.managers.nodes : n.hostname]
}
