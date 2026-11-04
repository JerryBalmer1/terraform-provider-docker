resource "docker_swarm_init" "this" {
  advertise_addr    = "10.0.0.5"
  default_addr_pool = ["10.20.0.0/16"]
  subnet_size       = 24
}

output "worker_join_token" {
  value     = docker_swarm_init.this.worker_join_token
  sensitive = true
}
