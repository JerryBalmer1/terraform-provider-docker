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
      actions = [action.docker_compose_run.this]
    }
  }
}

# Runs a one-off worker container (shop-worker-run-<id>), waits for it and
# removes it; the exit code and output are written to run-result.json. On demand:
#   terraform apply -invoke=action.docker_compose_run.this
action "docker_compose_run" "this" {
  config {
    name        = "shop"
    content     = docker_compose.shop.content
    service     = "worker"
    command     = ["sh", "-c", "echo migrating && echo done"]
    environment = { STEP = "migrate" }
    result_path = "${path.module}/run-result.json"
  }
}
