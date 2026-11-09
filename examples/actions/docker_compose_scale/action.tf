resource "docker_compose" "shop" {
  name    = "shop"
  content = <<-EOT
    services:
      web:
        image: nginx:1.27-alpine
        ports: ["8080:80"]
      worker:
        image: busybox:1.36
        command: ["sh", "-c", "sleep 3600"]
  EOT

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_compose_scale.this]
    }
  }
}

# Scales the worker service to 3 containers (a later docker_compose apply
# brings it back to the configured count). On demand:
#   terraform apply -invoke=action.docker_compose_scale.this
action "docker_compose_scale" "this" {
  config {
    name     = "shop"
    content  = docker_compose.shop.content
    replicas = { worker = 3 }
  }
}
