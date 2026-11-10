resource "docker_buildx_builder" "ci" {
  name            = "ci"
  buildkitd_flags = "--allow-insecure-entitlement=network.host --debug"

  buildkitd_config_content = <<-EOT
    [worker.oci]
      max-parallelism = 4
  EOT

  # Pin the platforms this node advertises first (docker buildx create --platform).
  platform = ["linux/amd64", "linux/arm64"]

  docker_container {
    image        = "moby/buildkit:buildx-stable-1"
    memory       = 4294967296 # 4 GiB
    cpu_quota    = 200000     # two CPUs per 100 ms period
    cpu_period   = 100000
    default_load = true
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

output "buildkit_version" {
  value = docker_buildx_builder.ci.buildkit_version
}

# A BuildKit daemon run elsewhere (docker buildx create --driver remote).
# Nothing is created on the Docker daemon; builds select it by endpoint.
resource "docker_buildx_builder" "remote" {
  name     = "remote"
  driver   = "remote"
  endpoint = "tcp://buildkit.internal:1234"

  remote {
    cacert     = "${path.module}/certs/ca.pem"
    cert       = "${path.module}/certs/client-cert.pem"
    key        = "${path.module}/certs/client-key.pem"
    servername = "buildkit.internal"
  }
}
