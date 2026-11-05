resource "docker_image" "app" {
  name = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_image_prune.dangling]
    }
  }
}

# Removes the dangling image left behind when the tag moves. On demand:
#   terraform apply -invoke=action.docker_image_prune.dangling
action "docker_image_prune" "dangling" {
  config {
    dangling = true
    filters  = { "label!" = "keep" }
  }
}
