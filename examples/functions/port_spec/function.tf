locals {
  p = provider::docker::port_spec("127.0.0.1:8080:80/tcp")
  # p = { host_ip = "127.0.0.1", host_port = "8080", container_port = "80", protocol = "tcp" }

  v6 = provider::docker::port_spec("[::1]:5353:53/udp")
  # v6.host_ip = "::1", v6.protocol = "udp"

  range = provider::docker::port_spec("8000-8010:80-90")
  # range.host_port = "8000-8010", range.container_port = "80-90"
}

output "published_port" {
  value = tonumber(local.p.host_port)
}
