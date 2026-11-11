# Prerequisite: swarm config NAME.
resource "docker_config" "c" {
  name = "NAME"
  data = base64encode("server { listen 80; }")
}
