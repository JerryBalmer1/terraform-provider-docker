# Running containers of one compose-style project.
data "docker_containers" "shop" {
  all    = false
  labels = { "com.example.project" = "shop" }
}

output "shop_containers" {
  value = [for c in data.docker_containers.shop.containers : "${c.name} (${c.status})"]
}
