# Run with `terraform query` (Terraform 1.14+); add `-generate-config-out=imports.tf`
# to write import blocks and resource configuration for what is found.
provider "docker" {}

list "docker_service" "shop" {
  provider = docker

  config {
    labels = { "com.docker.stack.namespace" = "shop" }
  }
}
