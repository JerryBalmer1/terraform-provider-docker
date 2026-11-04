# Pull an image from a registry.
resource "docker_image" "nginx" {
  name         = "nginx:1.27"
  keep_locally = true
}

# Re-pull when the tag moves in the registry.
data "docker_registry_image" "redis" {
  name = "redis:7"
}

resource "docker_image" "redis" {
  name     = data.docker_registry_image.redis.name
  triggers = { digest = data.docker_registry_image.redis.sha256_digest }
}

# Build from a local context. Editing any file in ./app that .dockerignore
# does not exclude changes context_digest and forces a rebuild.
resource "docker_image" "app" {
  name     = "registry.example.com/team/app:1.0"
  platform = "linux/amd64"

  build {
    context    = "${path.module}/app"
    dockerfile = "Dockerfile"
    target     = "runtime"
    args = {
      GO_VERSION = "1.27"
    }
    labels = {
      "org.opencontainers.image.source" = "https://github.com/example/app"
    }
    pull = true
  }
}
