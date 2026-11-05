resource "docker_network" "app" {
  name = "app"
}

resource "docker_volume" "data" {
  name = "web-data"
}

resource "docker_container" "web" {
  name    = "web"
  image   = "nginx:1.27"
  restart = "unless-stopped"

  env    = { NGINX_ENTRYPOINT_QUIET_LOGS = "1" }
  labels = { team = "web" }

  ports {
    internal = 80
    external = 8080
  }

  mounts {
    type   = "volume"
    source = docker_volume.data.name
    target = "/usr/share/nginx/html"
  }

  mounts {
    type       = "tmpfs"
    target     = "/var/cache/nginx"
    tmpfs_size = 67108864
  }

  # SELinux hosts: relabel the bind source for this container (`-v src:dst:z`).
  mounts {
    type            = "bind"
    source          = "/srv/web/conf.d"
    target          = "/etc/nginx/conf.d"
    read_only       = true
    selinux_relabel = "z"
  }

  # Files copied in after create and before start.
  upload {
    file        = "/etc/nginx/snippets/headers.conf"
    content     = "add_header X-Served-By terraform;\n"
    permissions = "0640"
    owner       = 0
    group       = 101 # nginx
  }

  # For a local file use `source = "${path.module}/banner.sh"` together with
  # `source_hash = filesha256("${path.module}/banner.sh")`.
  upload {
    file           = "/docker-entrypoint.d/99-banner.sh"
    content_base64 = base64encode("#!/bin/sh\necho web starting\n")
    executable     = true
  }

  networks_advanced {
    name    = docker_network.app.name
    aliases = ["www"]
  }

  healthcheck {
    test         = ["CMD", "curl", "-fsS", "http://localhost/"]
    interval     = "10s"
    timeout      = "3s"
    start_period = "5s"
    retries      = 3
  }

  capabilities {
    drop = ["NET_RAW"]
  }

  ulimits {
    name = "nofile"
    soft = 65536
    hard = 65536
  }

  # Create-time settings (changing any of these replaces the container).
  hostname     = "web"
  dns          = ["1.1.1.1", "8.8.8.8"]
  extra_hosts  = ["db.internal:10.0.0.5", "host.docker.internal:host-gateway"]
  stop_signal  = "SIGQUIT"
  stop_timeout = 30
  sysctls      = { "net.core.somaxconn" = "1024" }
  security_opt = ["no-new-privileges:true"]

  devices {
    host_path = "/dev/fuse"
  }

  log_driver = "json-file"
  log_opts   = { max-size = "10m", max-file = "3" }

  memory    = 268435456 # 256 MiB, updated in place
  nano_cpus = 500000000 # 0.5 CPU, updated in place

  must_run     = true
  wait         = true
  wait_timeout = "2m"
}
