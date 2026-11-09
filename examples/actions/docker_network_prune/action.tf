resource "docker_network" "app" {
  name = "app"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_network_prune.unused]
    }
  }
}

# Removes unused networks labelled env=test. On demand:
#   terraform apply -invoke=action.docker_network_prune.unused
action "docker_network_prune" "unused" {
  config {
    filters = { label = "env=test" }
  }
}
