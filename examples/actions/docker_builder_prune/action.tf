# Prunes a buildx builder's cache instead of the daemon's (set builder to its name).
action "docker_builder_prune" "buildx" {
  config {
    builder = "ci"
    all     = true
  }
}

resource "docker_image" "app" {
  name = "app:dev"

  build {
    context = "${path.module}/app"
  }

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_builder_prune.old]
    }
  }
}

# Removes build cache older than a week from the daemon's own BuildKit after each build. On demand:
#   terraform apply -invoke=action.docker_builder_prune.old
action "docker_builder_prune" "old" {
  config {
    filters = { until = "168h" }
  }
}
