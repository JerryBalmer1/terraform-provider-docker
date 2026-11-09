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
      actions = [action.docker_compose_push.this]
    }
  }
}

# Pushes the app image to its registry. On demand:
#   terraform apply -invoke=action.docker_compose_push.this
action "docker_compose_push" "this" {
  config {
    name    = "app"
    content = <<-EOT
      services:
        app:
          image: registry.example.com/app:1.0
    EOT
  }
}
