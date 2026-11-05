resource "docker_buildx_builder" "ci" {
  name           = "ci"
  buildkit_flags = "--allow-insecure-entitlement=network.host --debug"

  buildkit_config_content = <<-EOT
    [worker.oci]
      max-parallelism = 4
  EOT

  docker_container {
    image  = "moby/buildkit:buildx-stable-1"
    memory = 4294967296 # 4 GiB
    env = {
      HTTPS_PROXY = "http://proxy.internal:3128"
    }
  }

  # Optional: also serve BuildKit on TCP (unauthenticated).
  tcp_port = 1234
}

output "buildkit_endpoint" {
  # e.g. buildctl --addr docker-container://buildx_buildkit_ci0 debug workers
  value = docker_buildx_builder.ci.endpoint
}

output "buildkit_tcp" {
  value = docker_buildx_builder.ci.tcp_address
}
