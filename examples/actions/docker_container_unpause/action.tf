resource "docker_container" "app" {
  name  = "app"
  image = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_unpause.app]
    }
  }
}

# Resume a paused container. On demand:
#   terraform apply -invoke=action.docker_container_unpause.app
action "docker_container_unpause" "app" {
  config {
    container_id = docker_container.app.id
    result_path  = "${path.module}/out/unpause.json"
  }
}
