resource "docker_container" "app" {
  name  = "app"
  image = "nginx:1.27"

  lifecycle {
    # Paused first, so there is something to resume (unpausing a running container fails).
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_pause.app]
    }
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_unpause.app]
    }
  }
}

action "docker_container_pause" "app" {
  config {
    container_id = docker_container.app.id
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
