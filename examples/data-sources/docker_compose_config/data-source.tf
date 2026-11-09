data "docker_compose_config" "shop" {
  name              = "shop"
  project_directory = path.module
  env               = { WEB_TAG = "1.27-alpine" }
  content           = <<-EOT
    services:
      web:
        image: nginx:$${WEB_TAG}
  EOT
}

# The canonical YAML `docker compose config` would print, and one field of it.
output "compose_yaml" {
  value     = data.docker_compose_config.shop.compose_yaml
  sensitive = true
}

output "web_image" {
  value = data.docker_compose_config.shop.project.services["web"].image
}
