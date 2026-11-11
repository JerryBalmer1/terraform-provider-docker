# Prerequisite: a long-running busybox container named NAME.
resource "docker_container" "c" {
  name    = "NAME"
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
}
