data "docker_nodes" "managers" {
  role = "manager"
}

output "manager_hostnames" {
  value = [for n in data.docker_nodes.managers.nodes : n.description.hostname]
}

output "manager_ids" {
  value = data.docker_nodes.managers.ids
}
