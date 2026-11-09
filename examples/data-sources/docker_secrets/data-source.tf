data "docker_secrets" "shop" {
  labels = { app = "shop" }
}

output "shop_secret_names" {
  value = data.docker_secrets.shop.names
}
