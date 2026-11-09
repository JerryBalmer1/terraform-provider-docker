# A container started outside Terraform, for example by a CI agent.
data "docker_container" "agent" {
  name = "ci-agent"
}

output "agent_image" {
  value = data.docker_container.agent.image_id
}

output "agent_running" {
  value = data.docker_container.agent.inspect.state.running
}
