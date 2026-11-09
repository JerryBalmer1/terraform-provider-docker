data "docker_compose_top" "shop" {
  name = "shop"
}

output "web_processes" {
  value = data.docker_compose_top.shop.processes["shop-web-1"].processes
}
