# Import by project name (local mode) or "<project>:swarm". The next apply
# adopts objects whose provider spec-hash label (or, for objects made before Pass 5, config-hash label) matches the
# configuration and recreates the rest.
terraform import docker_compose.shop shop
terraform import docker_compose.stack shopstack:swarm
