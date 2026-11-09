resource "docker_container" "app" {
  name  = "app"
  image = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_stop.app]
    }
  }
}

# Stop a container after an update, for example before a backup. On demand:
#   terraform apply -invoke=action.docker_container_stop.app
action "docker_container_stop" "app" {
  config {
    container_id = docker_container.app.id
    signal       = "SIGTERM"
    timeout      = 30
    result_path  = "${path.module}/out/stop.json"
  }
}
