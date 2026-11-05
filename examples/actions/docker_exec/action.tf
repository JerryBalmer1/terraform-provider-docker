resource "docker_container" "db" {
  name  = "db"
  image = "postgres:17"
  env   = { POSTGRES_PASSWORD = "example" }

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_exec.migrate]
    }
  }
}

# Runs after the container is created or updated; fails the apply when the
# command exits non-zero. Run it on demand with:
#   terraform apply -invoke=action.docker_exec.migrate
action "docker_exec" "migrate" {
  config {
    container = docker_container.db.name
    command   = ["sh", "-c", "until pg_isready -U postgres; do sleep 1; done; psql -U postgres -c 'CREATE TABLE IF NOT EXISTS t (id int)'"]
    env       = { PGCONNECT_TIMEOUT = "5" }
    user      = "postgres"
    workdir   = "/tmp"
    timeout   = "2m"
  }
}
