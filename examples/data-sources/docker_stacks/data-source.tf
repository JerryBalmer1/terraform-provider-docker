data "docker_stacks" "all" {}

output "stacks" {
  value = { for s in data.docker_stacks.all.stacks : s.name => s.services_count }
}
