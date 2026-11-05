# Import by project name (local mode) or "<project>:swarm". The next apply
# adopts objects whose com.docker.compose.config-hash label matches the
# configuration and recreates the rest.
terraform import docker_compose.shop shop
terraform import docker_compose.stack shopstack:swarm
