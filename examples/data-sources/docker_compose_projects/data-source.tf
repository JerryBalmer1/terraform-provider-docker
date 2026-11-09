data "docker_compose_projects" "all" {
  all = true
}

output "projects" {
  value = data.docker_compose_projects.all.names
}
