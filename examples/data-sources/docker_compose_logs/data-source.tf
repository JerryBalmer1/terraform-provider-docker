data "docker_compose_logs" "shop" {
  name     = "shop"
  services = ["web"]
  tail     = 100
  since    = "15m"
}

output "web_errors" {
  value     = [for e in data.docker_compose_logs.shop.log_entries : e.message if e.stream == "stderr"]
  sensitive = true
}
