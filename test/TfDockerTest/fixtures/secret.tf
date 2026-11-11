# Prerequisite: swarm secret NAME, labelled app=shop.
resource "docker_secret" "s" {
  name   = "NAME"
  data   = base64encode("change-me")
  labels = { app = "shop" }
}
