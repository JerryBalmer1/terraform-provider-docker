variable "worker_replicas" {
  type    = number
  default = 3
}

resource "terraform_data" "scale" {
  input = var.worker_replicas

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_service_scale.worker]
    }
  }
}

# Scales a service whose replica count Terraform does not own. On demand:
#   terraform apply -invoke=action.docker_service_scale.worker
action "docker_service_scale" "worker" {
  config {
    service_id  = "queue-worker"
    result_path = "${path.module}/scale-result.json"
    replicas    = var.worker_replicas
    timeout     = "5m"
  }
}
