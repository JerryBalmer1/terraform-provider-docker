resource "docker_swarm_node" "manager2" {
  host            = "tcp://10.0.0.8:2376"
  join_token      = var.worker_join_token
  manager_address = "10.0.0.5:2377"

  # Joins as a worker, then promotes the node to manager once it is up.
  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_node_promote.manager2]
    }
  }
}

variable "worker_join_token" {
  type      = string
  sensitive = true
}

# Promotes the node to manager. On demand:
#   terraform apply -invoke=action.docker_node_promote.manager2
action "docker_node_promote" "manager2" {
  config {
    node_id     = docker_swarm_node.manager2.node_id
    result_path = "${path.module}/promote-result.json"
  }
}
