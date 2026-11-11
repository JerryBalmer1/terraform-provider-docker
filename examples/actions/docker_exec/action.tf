resource "docker_container" "db" {
  name  = "db"
  image = "postgres:17"

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
    container_id = docker_container.db.id
    command      = ["sh", "-c", "until pg_isready -U postgres; do sleep 1; done; psql -U postgres -v ON_ERROR_STOP=1"]
    # The SQL script on standard input (`docker exec -i`); psql reads it to
    # end of file. stdin may show in plan output: read secrets with the
    # docker_exec_output ephemeral resource instead.
    stdin   = <<-EOT
      CREATE TABLE IF NOT EXISTS t (id int);
      INSERT INTO t VALUES (1);
    EOT
    env     = { PGCONNECT_TIMEOUT = "5" }
    user    = "postgres"
    workdir = "/tmp"
    timeout = "2m"
    # A TTY with a fixed size (height, width) for tools that format to it;
    # stdout and stderr then arrive merged in stdout. Not together with stdin.
    # tty          = true
    # console_size = [40, 160]
    # stdout, stderr and the exec inspect (exit_code, pid, ...) as JSON.
    result_path = "${path.module}/out/migrate.json"
  }
}
