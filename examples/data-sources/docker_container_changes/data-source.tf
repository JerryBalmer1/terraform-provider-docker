data "docker_container_changes" "web" {
  container = "web"
}

# Paths the container added (kind 1) on top of its image.
output "added_paths" {
  value = [for c in data.docker_container_changes.web.changes : c.path if c.kind == 1]
}
