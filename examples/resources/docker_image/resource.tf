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
  triggers = { digest = data.docker_registry_image.redis.digest }
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

    # Classic-builder resource limits and extra tags.
    memory      = 2147483648
    shm_size    = 268435456
    network     = "host"
    tags        = ["registry.example.com/team/app:latest"]
    extra_hosts = ["internal.example.com:10.0.0.5"]
    ulimits = {
      nofile = { soft = 65536, hard = 65536 }
    }
  }
}

# Hand-off values: the digest reference for a pinned deployment, the image ID
# and the stages the Dockerfile declares.
output "app_image" {
  value = {
    id     = docker_image.app.id
    ref    = docker_image.app.image_ref
    pinned = docker_image.app.image_digest_ref
    stages = [for s in docker_image.app.stages : s.name]
  }
}

# Build from a Git repository the daemon fetches (classic builder only).
resource "docker_image" "remote" {
  name = "example/hello:git"

  build {
    remote     = "https://github.com/docker-library/hello-world.git#master:amd64"
    dockerfile = "Dockerfile"
  }
}

# Build with BuildKit: secrets are mounted with RUN --mount=type=secret and
# are read by the provider at build time (never stored in state).
#   # syntax=docker/dockerfile:1
#   RUN --mount=type=secret,id=npm_token NPM_TOKEN=$(cat /run/secrets/npm_token) npm ci
resource "docker_image" "web" {
  name = "registry.example.com/team/web:1.0"

  build {
    context = "${path.module}/web"
    builder = "buildkit"

    # Reuse layers from a previously pushed image built with inline cache.
    cache_from = ["registry.example.com/team/web:cache"]
    args = {
      BUILDKIT_INLINE_CACHE = "1"
    }

    platforms = ["linux/arm64"]

    # Export cache and attach attestations.
    cache_to   = ["type=inline"]
    provenance = "min"
    sbom       = true

    secrets {
      id  = "npm_token"
      env = "NPM_TOKEN"
    }
    secrets {
      id  = "netrc"
      src = "${path.module}/secrets/netrc"
    }
  }
}

# Multi-platform: several platforms build one image index (like
# `docker buildx build --platform linux/amd64,linux/arm64`). The daemon must
# use the containerd image store to hold an index; the provider checks this
# before building. id and digest are the index digest.
resource "docker_image" "multi" {
  name = "registry.example.com/team/app:1.4.0-multi"

  build {
    context   = "${path.module}/app"
    builder   = "buildkit"
    platforms = ["linux/amd64", "linux/arm64"]
  }
}
