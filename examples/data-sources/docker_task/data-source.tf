data "docker_tasks" "web" {
  service_id    = "web"
  desired_state = "running"
}

data "docker_task" "first" {
  id = data.docker_tasks.web.ids[0]
}

output "first_task" {
  value = {
    name      = data.docker_task.first.name
    node_id   = data.docker_task.first.node_id
    container = data.docker_task.first.container_id
    state     = data.docker_task.first.inspect.status.state
  }
}
