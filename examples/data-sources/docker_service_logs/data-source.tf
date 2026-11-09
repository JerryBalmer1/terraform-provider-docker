# The last 100 lines of every task of the service, merged by time.
data "docker_service_logs" "web" {
  service_id = "web"
  tail       = 100
  since      = "30m"
}

output "web_errors" {
  value     = [for e in data.docker_service_logs.web.log_entries : e.message if e.stream == "stderr"]
  sensitive = true
}
