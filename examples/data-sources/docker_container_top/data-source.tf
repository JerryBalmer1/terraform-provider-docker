data "docker_container_top" "web" {
  container = "web"
  ps_args   = "aux"
}

output "web_processes" {
  value = data.docker_container_top.web.top.processes
}
