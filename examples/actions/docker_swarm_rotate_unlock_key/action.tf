resource "docker_swarm_init" "this" {
  advertise_addr = "eth0"
  autolock       = true

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_swarm_rotate_unlock_key.this]
    }
  }
}

# Rotates the manager unlock key; the new key is written to result_path
# (unlock_key) and shows on docker_swarm_init.unlock_key after a refresh.
# On demand:
#   terraform apply -invoke=action.docker_swarm_rotate_unlock_key.this
action "docker_swarm_rotate_unlock_key" "this" {
  config {
    result_path = "${path.module}/unlock-key.json"
  }
}
