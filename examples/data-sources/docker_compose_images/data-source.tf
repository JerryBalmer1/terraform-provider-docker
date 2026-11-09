data "docker_compose_images" "shop" {
  name = "shop"
}

output "image_digests" {
  value = data.docker_compose_images.shop.image_digest_refs
}
