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
      actions = [action.docker_compose_cp.this]
    }
  }
}

# Copies the nginx configuration directory out of shop-web-1. On demand:
#   terraform apply -invoke=action.docker_compose_cp.this
action "docker_compose_cp" "this" {
  config {
    name           = docker_compose.shop.name
    service        = "web"
    direction      = "from_container"
    container_path = "/etc/nginx/conf.d"
    local_path     = "${path.module}/nginx-conf"
  }
}
