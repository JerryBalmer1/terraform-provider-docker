# container_full: one docker_container with as much of its schema as the dind harness can run, plus the
# network, volume and image it uses and the data sources that read it back. Plain harness (no swarm needed).
terraform {
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
  }
}

provider "docker" {}

resource "docker_image" "nginx" {
  name         = "nginx:1.27-alpine"
  keep_locally = true
}

resource "docker_network" "front" {
  name   = "manual-front"
  labels = { scenario = "container_full" }
  ipam_config {
    subnet  = "10.77.0.0/24"
    gateway = "10.77.0.1"
  }
}

resource "docker_network" "back" {
  name     = "manual-back"
  internal = true
}

resource "docker_volume" "html" {
  name   = "manual-html"
  labels = { scenario = "container_full" }
}

resource "docker_container" "web" {
  name     = "manual-web"
  image    = docker_image.nginx.id
  hostname = "web"
  restart  = "unless-stopped"
  user     = "root"
  working_dir = "/usr/share/nginx/html"

  env         = { NGINX_ENTRYPOINT_QUIET_LOGS = "1", SCENARIO = "container_full" }
  labels      = { scenario = "container_full", tier = "web" }
  annotations = { "com.example.owner" = "manual" }

  ports {
    container_port = 80
    host_port      = 8088
  }
  ports {
    container_port = 443
    host_ip        = "127.0.0.1"
    host_port      = 8443
  }

  mounts {
    type   = "volume"
    source = docker_volume.html.name
    target = "/usr/share/nginx/html"
  }
  mounts {
    type   = "tmpfs"
    target = "/var/cache/nginx"
    tmpfs_options {
      size_bytes = 67108864
      mode       = "1777"
    }
  }

  files {
    path    = "/etc/nginx/conf.d/headers.conf"
    content = "add_header X-Scenario container_full;\n"
    mode    = "0644"
  }
  files {
    path           = "/docker-entrypoint.d/99-banner.sh"
    content_base64 = base64encode("#!/bin/sh\necho manual-web starting\n")
    mode           = "0755"
  }

  networks = {
    (docker_network.front.name) = {
      aliases      = ["www", "web"]
      ipv4_address = "10.77.0.10"
    }
    (docker_network.back.name) = {}
  }

  healthcheck {
    test           = ["CMD", "wget", "-q", "-O-", "http://127.0.0.1/"]
    interval       = "10s"
    timeout        = "3s"
    start_period   = "5s"
    start_interval = "1s"
    retries        = 3
  }

  cap_drop     = ["NET_RAW"]
  cap_add      = ["NET_BIND_SERVICE"]
  security_opt = ["no-new-privileges:true"]
  init         = true
  dns          = ["1.1.1.1"]
  dns_search   = ["example.internal"]
  extra_hosts  = ["db.internal:10.77.0.50", "host.docker.internal:host-gateway"]
  sysctls      = { "net.core.somaxconn" = "1024" }
  stop_signal  = "SIGQUIT"
  stop_timeout = 20
  shm_size     = 134217728

  ulimits {
    name = "nofile"
    soft = 65536
    hard = 65536
  }

  logging {
    driver  = "json-file"
    options = { max-size = "10m", max-file = "3" }
  }

  memory        = 268435456
  nano_cpus     = 500000000
  pids_limit    = 512
  oom_score_adj = 100

  state        = "running"
  wait         = true
  wait_timeout = "2m"
}

# A one-shot container kept as exited, its output in logs.
resource "docker_container" "once" {
  name                = "manual-once"
  image               = "busybox:1.36"
  command             = ["sh", "-c", "echo hello from a one-shot container; exit 0"]
  state               = "exited"
  expected_exit_codes = [0]
  logs_tail           = 50
}

data "docker_container" "web" {
  name       = docker_container.web.name
  depends_on = [docker_container.web]
}

data "docker_container_top" "web" {
  container  = docker_container.web.name
  depends_on = [docker_container.web]
}

output "web_ip_front" {
  value = docker_container.web.networks["manual-front"].ip_address
}

output "web_health" {
  value = data.docker_container.web.inspect.state.health.status
}

output "web_processes" {
  value = data.docker_container_top.web.top.processes
}

output "once_logs" {
  value     = docker_container.once.logs
  sensitive = true
}

# The image's exposed ports as the Engine returns them: a map keyed by "<port>/<proto>".
output "image_exposed_ports" {
  value = docker_image.nginx.inspect.config.exposed_ports
}
