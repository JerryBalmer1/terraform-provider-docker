# Import by image name (as tagged on the daemon) or by image ID.
# A built image's build block is not recoverable from the daemon: importing
# it and then adding a build block plans a rebuild.
terraform import docker_image.nginx nginx:1.27
terraform import docker_image.nginx sha256:0f5ee0c03b21c1cd4bb68d1a52e2d1e7d1d3a5c0b36e04e14e3d6fdd5d4c1a2b
