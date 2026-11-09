# A one-shot migration container; the apply fails unless it exits 0.
resource "docker_container" "migrate" {
  name     = "db-migrate"
  image    = "ghcr.io/org/migrate:2.1.0"
  must_run = false

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_wait.migrate]
    }
  }
}

# On demand:
#   terraform apply -invoke=action.docker_container_wait.migrate
action "docker_container_wait" "migrate" {
  config {
    container_id     = docker_container.migrate.id
    condition        = "not-running"
    timeout          = "15m"
    expect_exit_code = 0
    result_path      = "${path.module}/out/migrate-exit.json"
  }
}
