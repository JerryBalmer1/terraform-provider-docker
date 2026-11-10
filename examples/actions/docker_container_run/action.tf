# Run a one-shot container like `docker run --rm`: create, start, wait for the
# exit, capture exit_code / stdout / stderr, remove. The apply fails unless the
# exit code is in expected_exit_codes.
action "docker_container_run" "migrate" {
  config {
    image   = "ghcr.io/org/migrate:2.1.0"
    command = ["migrate", "up"]
    env = {
      DATABASE_URL = "postgres://db:5432/app"
    }
    network_mode        = "app"
    timeout             = "15m"
    expected_exit_codes = [0]
    result_path         = "${path.module}/out/migrate.json"
  }
}

# Run it after the database container is created or replaced.
resource "docker_container" "db" {
  name  = "db"
  image = "postgres:17"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_run.migrate]
    }
  }
}

# On demand:
#   terraform apply -invoke=action.docker_container_run.migrate
