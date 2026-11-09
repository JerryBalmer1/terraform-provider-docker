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
      actions = [action.docker_compose_publish.this]
    }
  }
}

# Publishes the compose file as an OCI artifact. On demand:
#   terraform apply -invoke=action.docker_compose_publish.this
action "docker_compose_publish" "this" {
  config {
    name       = "shop"
    content    = docker_compose.shop.content
    repository = "registry.example.com/apps/shop:1.0"
  }
}
