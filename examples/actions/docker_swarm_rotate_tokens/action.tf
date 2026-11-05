resource "docker_swarm_init" "this" {
  advertise_addr = "eth0"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_swarm_rotate_tokens.all]
    }
  }
}

# Rotates the join tokens; nodes already joined are unaffected. On demand:
#   terraform apply -invoke=action.docker_swarm_rotate_tokens.all
action "docker_swarm_rotate_tokens" "all" {
  config {
    worker  = true
    manager = true
  }
}
