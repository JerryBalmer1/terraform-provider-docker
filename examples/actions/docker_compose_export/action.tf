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
      actions = [action.docker_compose_export.this]
    }
  }
}

# Exports the filesystem of shop-web-1 as a tar file. On demand:
#   terraform apply -invoke=action.docker_compose_export.this
action "docker_compose_export" "this" {
  config {
    name    = docker_compose.shop.name
    service = "web"
    output  = "${path.module}/shop-web.tar"
  }
}
