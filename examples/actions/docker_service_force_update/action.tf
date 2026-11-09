resource "docker_swarm_node" "worker2" {
  host            = "tcp://10.0.0.7:2376"
  join_token      = var.worker_join_token
  manager_address = "10.0.0.5:2377"

  # Swarm does not move running tasks onto a new node by itself: redeploy
  # the service once the node has joined so tasks spread over it.
  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_service_force_update.web]
    }
  }
}

variable "worker_join_token" {
  type      = string
  sensitive = true
}

# Redeploys every task without changing the spec. On demand:
#   terraform apply -invoke=action.docker_service_force_update.web
action "docker_service_force_update" "web" {
  config {
    service_id = "web"
    timeout    = "10m"
  }
}
