data "docker_stack" "shop" {
  name = "shop"
}

output "replicas" {
  value = { for s in data.docker_stack.shop.services : s.name => s.replicas }
}
