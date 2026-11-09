resource "terraform_data" "ca_rotation" {
  # Bump to rotate the swarm root CA again.
  input = 1

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_swarm_rotate_ca.yearly]
    }
  }
}

# Rotates to a new self-signed root CA and waits until every node has a
# certificate from it. On demand:
#   terraform apply -invoke=action.docker_swarm_rotate_ca.yearly
action "docker_swarm_rotate_ca" "yearly" {
  config {
    timeout     = "15m"
    result_path = "${path.module}/ca-rotation.json"
  }
}
