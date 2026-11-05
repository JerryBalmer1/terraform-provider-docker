variable "rollback_reason" {
  type    = string
  default = ""
}

resource "terraform_data" "rollback" {
  count = var.rollback_reason == "" ? 0 : 1
  input = var.rollback_reason

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_service_rollback.web]
    }
  }
}

# Rolls the service back to its previous spec. Usually run on demand:
#   terraform apply -invoke=action.docker_service_rollback.web
action "docker_service_rollback" "web" {
  config {
    service = "web"
    timeout = "10m"
  }
}
