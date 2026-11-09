resource "docker_image" "app" {
  name = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_prune.stopped]
    }
  }
}

# Removes stopped containers older than a day after each image change. On demand:
#   terraform apply -invoke=action.docker_container_prune.stopped
action "docker_container_prune" "stopped" {
  config {
    filters     = { until = "24h" }
    result_path = "${path.module}/.prune/containers.json"
  }
}
