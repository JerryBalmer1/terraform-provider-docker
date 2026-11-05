data "docker_registry_image_tags" "app" {
  name      = "ghcr.io/org/app"
  page_size = 500
}

# Newest release tag by name, e.g. "v1.12.0".
output "latest_release" {
  value = reverse(sort([for t in data.docker_registry_image_tags.app.tags : t if startswith(t, "v")]))[0]
}
