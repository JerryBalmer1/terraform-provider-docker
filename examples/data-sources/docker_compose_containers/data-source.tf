data "docker_compose_containers" "shop" {
  name = "shop"
  all  = true
}

output "states" {
  value = { for c in data.docker_compose_containers.shop.containers : c.names[0] => c.state }
}
