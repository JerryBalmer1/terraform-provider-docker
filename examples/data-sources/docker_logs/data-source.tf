data "docker_logs" "web" {
  container  = "web"
  tail       = "100"
  since      = "15m"
  timestamps = true
}

output "web_errors" {
  value = [for l in data.docker_logs.web.logs_list_string : l if strcontains(l, "ERROR")]
}
