# Prerequisite: stack "shop" with one service.
resource "docker_stack" "shop" {
  name = "shop"

  service {
    name  = "api"
    image = "busybox:1.36"
    args  = ["sleep", "3600"]

    # Wait until the task runs, so the data sources read a settled stack.
    converge_config {}
  }
}
