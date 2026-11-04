data "docker_network" "bridge" {
  name = "bridge"
}

output "bridge_subnet" {
  value = data.docker_network.bridge.ipam_config[0].subnet
}
