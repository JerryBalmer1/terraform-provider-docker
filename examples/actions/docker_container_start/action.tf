resource "docker_container" "app" {
  name     = "app"
  image    = "nginx:1.27"
  must_run = false

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_start.app]
    }
  }
}

# Start a container that docker_container created but left stopped. On demand:
#   terraform apply -invoke=action.docker_container_start.app
action "docker_container_start" "app" {
  config {
    container_id = docker_container.app.id
    result_path  = "${path.module}/out/start.json"
  }
}
