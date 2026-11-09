# Running containers of one compose-style project.
data "docker_containers" "shop" {
  all    = false
  labels = { "com.example.project" = "shop" }
}

output "shop_containers" {
  value = [for c in data.docker_containers.shop.containers : "${trimprefix(c.names[0], "/")} (${c.status})"]
}

output "shop_container_ids" {
  value = data.docker_containers.shop.ids
}
