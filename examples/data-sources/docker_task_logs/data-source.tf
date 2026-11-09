data "docker_tasks" "web" {
  service_id = "web"
}

data "docker_task_logs" "first" {
  task_id = data.docker_tasks.web.ids[0]
  tail    = 50
}

output "first_task_logs" {
  value     = data.docker_task_logs.first.logs
  sensitive = true
}
