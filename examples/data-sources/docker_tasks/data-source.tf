# Like `docker service ps web`.
data "docker_tasks" "web" {
  service_id    = "web"
  desired_state = "running"
}

output "web_tasks" {
  value = { for t in data.docker_tasks.web.tasks : t.id => t.status.state }
}

# Like `docker node ps worker1`.
data "docker_tasks" "on_worker1" {
  node_id = "worker1"
}
