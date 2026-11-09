data "docker_services" "replicated" {
  mode   = "replicated"
  labels = { "com.docker.stack.namespace" = "shop" }
}

output "replicas" {
  value = { for s in data.docker_services.replicated.services : s.spec.name => s.spec.mode.replicated.replicas }
}
