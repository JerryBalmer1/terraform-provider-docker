variable "node" {
  type    = string
  default = "worker-1"
}

resource "terraform_data" "back_in_service" {
  input = var.node

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_node_activate.back]
    }
  }
}

# Makes a drained node schedulable again. On demand:
#   terraform apply -invoke=action.docker_node_activate.back
action "docker_node_activate" "back" {
  config {
    node = var.node
  }
}
