resource "docker_image" "app" {
  name = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_system_prune.old]
    }
  }
}

# Removes unused objects older than a day after each image change. On demand:
#   terraform apply -invoke=action.docker_system_prune.old
action "docker_system_prune" "old" {
  config {
    containers  = true
    images      = true
    networks    = true
    volumes     = false
    build_cache = true
    filters     = { until = "24h" }
  }
}
