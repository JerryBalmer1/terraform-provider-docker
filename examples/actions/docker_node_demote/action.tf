variable "retiring_manager" {
  type    = string
  default = "manager-3"
}

resource "terraform_data" "retire" {
  input = var.retiring_manager

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_node_demote.retire]
    }
  }
}

# Demotes a manager to worker (the Engine refuses the last manager). On demand:
#   terraform apply -invoke=action.docker_node_demote.retire
action "docker_node_demote" "retire" {
  config {
    node_id = var.retiring_manager
  }
}
