# Every object is created as "shop_<name>" and labelled
# com.docker.stack.namespace=shop, so `docker stack ps shop` shows it.
resource "docker_stack" "shop" {
  name = "shop"

  network {
    name       = "backend"
    attachable = true
  }

  # Not pre-created: tasks create "shop_pgdata" on their node on first use.
  volume {
    name   = "pgdata"
    driver = "local"
  }

  secret {
    name = "db_password"
    data = base64encode("change-me")
  }

  config {
    name = "nginx"
    data = base64encode("server { listen 80; location / { return 200 \"ok\n\"; } }")
  }

  service {
    name     = "db"
    image    = "postgres:16-alpine"
    networks = { backend = {} } # stack-local short name

    env = {
      POSTGRES_PASSWORD_FILE = "/run/secrets/db_password"
    }

    mounts {
      type   = "volume"
      source = "pgdata" # stack volume short name
      target = "/var/lib/postgresql/data"
    }

    secrets {
      secret_id = "db_password" # stack secret short name
    }

    placement {
      constraints = ["node.role==manager"]
    }
  }

  service {
    name     = "web"
    image    = "nginx:1.27-alpine"
    replicas = 2
    networks = { backend = {} }

    configs {
      config_id = "nginx"
      file_name = "/etc/nginx/conf.d/default.conf"
    }

    endpoint_spec {
      ports {
        target_port    = 80
        published_port = 80
      }
    }

    converge_config {}
  }
}
