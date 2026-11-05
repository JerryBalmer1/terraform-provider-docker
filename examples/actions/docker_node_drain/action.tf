variable "maintenance_node" {
  type    = string
  default = "worker-1"
}

resource "terraform_data" "maintenance" {
  input = var.maintenance_node

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_node_drain.maintenance]
    }
  }
}

# Drains the node and waits for its tasks to move. On demand:
#   terraform apply -invoke=action.docker_node_drain.maintenance
action "docker_node_drain" "maintenance" {
  config {
    node    = var.maintenance_node
    timeout = "10m"
  }
}
