data "docker_compose_volumes" "shop" {
  name = "shop"
}

output "volume_names" {
  value = data.docker_compose_volumes.shop.names
}
