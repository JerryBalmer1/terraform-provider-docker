locals {
  ref = provider::docker::parse_image_ref("ghcr.io/org/app:1.4.2")
  # ref = {
  #   registry   = "ghcr.io"
  #   repository = "org/app"
  #   tag        = "1.4.2"
  #   digest     = ""
  # }

  hub = provider::docker::parse_image_ref("nginx")
  # hub.registry = "docker.io", hub.repository = "library/nginx", hub.tag = "latest"
}

output "app_tag" {
  value = local.ref.tag
}
