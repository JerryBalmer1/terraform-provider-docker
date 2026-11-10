resource "docker_container" "app" {
  name  = "app"
  image = "nginx:1.27"
  state = "created" # created only: the provider never starts it

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_start.app]
    }
  }
}

# Start a container that docker_container created but left stopped (state = "created"). On demand:
#   terraform apply -invoke=action.docker_container_start.app
action "docker_container_start" "app" {
  config {
    container_id = docker_container.app.id
    result_path  = "${path.module}/out/start.json"
  }
}
