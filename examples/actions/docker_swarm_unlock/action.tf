variable "swarm_unlock_key" {
  type      = string
  sensitive = true
}

resource "terraform_data" "manager_restart" {
  # Change after a manager daemon restart to unlock it.
  input = timestamp()

  lifecycle {
    ignore_changes = [input]
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_swarm_unlock.manager]
    }
  }
}

# Unlocks the provider's (autolocked) manager after a daemon restart; an
# unlocked manager is left alone. On demand:
#   terraform apply -invoke=action.docker_swarm_unlock.manager
action "docker_swarm_unlock" "manager" {
  config {
    unlock_key = var.swarm_unlock_key
  }
}
