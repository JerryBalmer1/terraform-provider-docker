resource "docker_secret" "db_password" {
  name = "db_password"
  data = base64encode("change-me")
}

resource "docker_config" "nginx" {
  name = "nginx-default.conf"
  data = base64encode("server { listen 80; location / { return 200 \"ok\n\"; } }")
}

resource "docker_service" "web" {
  name     = "web"
  image    = "nginx:1.27-alpine"
  replicas = 3

  # Overlay networks keyed by name or ID; `{}` attaches without extras.
  networks = {
    frontend = { aliases = ["www"] }
    backend  = {}
  }

  env = {
    TZ = "UTC"
  }

  # Task container settings; all of these update in place (rolling update).
  hostname          = "web-{{.Task.Slot}}"
  user              = "nginx"
  hosts             = ["db.internal:10.0.0.5"]
  cap_drop          = ["NET_RAW"]
  sysctls           = { "net.core.somaxconn" = "1024" }
  stop_grace_period = "30s"
  stop_signal       = "SIGQUIT"
  init              = true

  dns_config {
    nameservers = ["10.0.0.2"]
    options     = ["ndots:2"]
  }

  ulimits {
    name = "nofile"
    soft = 65536
    hard = 65536
  }

  # Bump to force a rolling restart without other changes.
  force_update = 1

  labels = {
    app = "shop"
  }

  mounts {
    type   = "volume"
    source = "web-cache"
    target = "/var/cache/nginx"

    volume_options {
      no_copy = true
      driver_config {
        name = "local"
      }
    }
  }

  mounts {
    type   = "tmpfs"
    target = "/tmp"

    tmpfs_options {
      size_bytes = 67108864
      mode       = 448 # 0700
    }
  }

  endpoint_spec {
    ports {
      name           = "http"
      target_port    = 80
      published_port = 8080
    }
  }

  secrets {
    secret_id = docker_secret.db_password.id
    file_name = "db_password"
  }

  configs {
    config_id = docker_config.nginx.id
    file_name = "/etc/nginx/conf.d/default.conf"
  }

  update_config {
    parallelism    = 1
    delay          = "10s"
    failure_action = "rollback"
    order          = "start-first"
  }

  restart_policy {
    condition    = "on-failure"
    max_attempts = 3
  }

  placement {
    constraints = ["node.role==worker"]
    preferences = ["spread=node.labels.zone"]
  }

  resources {
    limits {
      nano_cpus    = 500000000
      memory_bytes = 268435456
      pids         = 256
    }
  }

  # Hardening: no privilege escalation through setuid binaries.
  privileges {
    no_new_privileges = true
  }

  healthcheck {
    test           = ["CMD", "wget", "-q", "-O-", "http://localhost/"]
    interval       = "30s"
    timeout        = "5s"
    retries        = 3
    start_period   = "1m"
    start_interval = "5s" # check more often while starting
  }

  logging {
    driver = "json-file"
    options = {
      max-size = "10m"
    }
  }

  # Wait for all replicas to run before apply finishes.
  converge_config {
    timeout = "3m"
    delay   = "5s"
  }
}

# A one-off job: runs 6 tasks to completion, 2 at a time. With
# converge_config, apply waits until total_completions tasks completed.
# Changing mode (e.g. to "replicated") replaces the service.
resource "docker_service" "migrate" {
  name              = "db-migrate"
  image             = "busybox:1.36"
  args              = ["sh", "-c", "echo migrating; sleep 5"]
  mode              = "replicated_job"
  max_concurrent    = 2
  total_completions = 6

  restart_policy {
    condition = "on-failure"
  }

  converge_config {
    timeout = "5m"
  }
}
