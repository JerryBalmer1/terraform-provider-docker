# A migration container started by an action and then waited for; the apply
# fails unless it exits 0. (docker_container with state = "exited" does the
# same in one resource, and docker_container_run also removes it.)
resource "docker_container" "migrate" {
  name  = "db-migrate"
  image = "ghcr.io/org/migrate:2.1.0"
  state = "created"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_start.migrate]
    }
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_wait.migrate]
    }
  }
}

action "docker_container_start" "migrate" {
  config {
    container_id = docker_container.migrate.id
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
