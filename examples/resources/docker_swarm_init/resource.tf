resource "docker_swarm_init" "this" {
  advertise_addr    = "10.0.0.5"
  default_addr_pool = ["10.20.0.0/16"]
  subnet_size       = 24

  # `docker swarm update` settings, changed in place.
  labels = {
    env = "prod"
  }
  orchestration = {
    task_history_retention_limit = 3
  }
  dispatcher = {
    heartbeat_period = "10s"
  }
  ca_config = {
    node_cert_expiry = "720h"
  }
}

output "worker_join_token" {
  value     = docker_swarm_init.this.worker_join_token
  sensitive = true
}

# The whole Engine swarm object, e.g. the root CA certificate.
output "swarm_trust_root" {
  value = docker_swarm_init.this.inspect.tls_info.trust_root
}
