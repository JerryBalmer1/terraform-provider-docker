resource "docker_container" "app" {
  name  = "app"
  image = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_restart.app]
    }
  }
}

# Restart a container after its configuration changed. On demand:
#   terraform apply -invoke=action.docker_container_restart.app
action "docker_container_restart" "app" {
  config {
    container_id = docker_container.app.id
    timeout      = 10
    result_path  = "${path.module}/out/restart.json"
  }
}
