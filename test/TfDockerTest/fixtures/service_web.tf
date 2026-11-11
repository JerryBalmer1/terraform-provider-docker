# Prerequisite: swarm service "web" with one running task that logs a line.
resource "docker_service" "web" {
  name  = "web"
  image = "busybox:1.36"
  args  = ["sh", "-c", "echo web task up; sleep 3600"]

  converge_config {
    timeout = "3m"
  }
}
