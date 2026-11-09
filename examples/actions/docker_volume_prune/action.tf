resource "docker_volume" "data" {
  name = "data"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_volume_prune.scratch]
    }
  }
}

# Removes every unused volume labelled scratch, named ones included. On demand:
#   terraform apply -invoke=action.docker_volume_prune.scratch
action "docker_volume_prune" "scratch" {
  config {
    all         = true
    filters     = { label = "scratch" }
    result_path = "${path.module}/.prune/volumes.json"
  }
}
