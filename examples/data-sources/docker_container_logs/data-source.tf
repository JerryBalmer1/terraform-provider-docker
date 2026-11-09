data "docker_container_logs" "web" {
  container = "web"
  tail      = 100
  since     = "15m"
  stderr    = true
}

# log_entries messages are sensitive: they can carry secrets the application printed.
output "web_errors" {
  value     = [for e in data.docker_container_logs.web.log_entries : "${e.timestamp} ${e.message}" if strcontains(e.message, "ERROR")]
  sensitive = true
}
