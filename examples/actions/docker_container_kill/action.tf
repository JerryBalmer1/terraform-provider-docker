resource "docker_container" "web" {
  name  = "web"
  image = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_kill.reload]
    }
  }
}

# nginx reloads its configuration on SIGHUP. On demand:
#   terraform apply -invoke=action.docker_container_kill.reload
action "docker_container_kill" "reload" {
  config {
    container_id = docker_container.web.id
    signal       = "SIGHUP"
  }
}
