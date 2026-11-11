# Prerequisite: an nginx container named NAME.
resource "docker_container" "c" {
  name  = "NAME"
  image = "nginx:1.27-alpine"
}
