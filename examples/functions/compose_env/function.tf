locals {
  app_env = {
    LOG_LEVEL = "info"
    DB_HOST   = "db"
    HOME      = null # bare "HOME": inherited
  }

  # ["DB_HOST=db", "HOME", "LOG_LEVEL=info"]
  env_lines = provider::docker::compose_env(local.app_env)
}

# An env file for the application, shipped as a swarm config.
resource "docker_config" "app_env" {
  name = "app.env"
  data = base64encode(join("", [for l in local.env_lines : "${l}\n"]))
}
