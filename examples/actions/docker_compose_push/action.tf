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

# Pushes the app image and its extra build tags to the registry, as
# `docker compose push` does: only services with `build` and `image`. A tag
# not in the local image store (say, :latest was never built here) is
# skipped instead of failing the run. On demand:
#   terraform apply -invoke=action.docker_compose_push.this
action "docker_compose_push" "this" {
  config {
    name           = "app"
    content        = <<-EOT
      services:
        app:
          image: registry.example.com/app:1.0
          build:
            context: .
            tags: ["registry.example.com/app:latest"]
    EOT
    ignore_missing = true
  }
}
