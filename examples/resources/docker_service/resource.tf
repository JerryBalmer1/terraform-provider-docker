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

  # Overlay networks by name or ID.
  networks = ["frontend"]

  env = {
    TZ = "UTC"
  }

  labels = {
    app = "shop"
  }

  mounts {
    type   = "volume"
    source = "web-cache"
    target = "/var/cache/nginx"
  }

  endpoint_spec {
    ports {
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
    }
  }

  healthcheck {
    test     = ["CMD", "wget", "-q", "-O-", "http://localhost/"]
    interval = "30s"
    timeout  = "5s"
    retries  = 3
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
