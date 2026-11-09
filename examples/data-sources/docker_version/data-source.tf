data "docker_version" "engine" {}

output "engine" {
  value = {
    platform    = data.docker_version.engine.platform_name
    version     = data.docker_version.engine.version.version
    api_version = data.docker_version.engine.version.api_version
    min_api     = data.docker_version.engine.version.min_api_version
  }
}
