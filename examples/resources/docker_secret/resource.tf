resource "docker_secret" "db_password" {
  name = "db_password"
  data = base64encode("change-me")

  labels = {
    app = "shop"
  }
}
