# Every user-defined bridge network carrying the label team=web.
data "docker_networks" "web" {
  driver = "bridge"
  type   = "custom"
  labels = { team = "web" }
}

output "web_network_names" {
  value = data.docker_networks.web.names
}

# Subnets of the first match, from the structured list.
output "first_network_subnets" {
  value = try([for c in data.docker_networks.web.networks[0].ipam.config : c.subnet], [])
}
