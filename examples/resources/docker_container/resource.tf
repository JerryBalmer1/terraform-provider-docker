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
    container_port = 80
    host_port      = 8080
  }

  mounts {
    type   = "volume"
    source = docker_volume.data.name
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

  # SELinux hosts: relabel the bind source for this container (`-v src:dst:z`).
  mounts {
    type            = "bind"
    source          = "/srv/web/conf.d"
    target          = "/etc/nginx/conf.d"
    read_only       = true
    selinux_relabel = "z"
  }

  # Files copied in after create and before start.
  files {
    path    = "/etc/nginx/headers.conf"
    content = "add_header X-Served-By terraform;\n"
    mode    = "0640"
    uid     = 0
    gid     = 101 # nginx
  }

  # For a local file use `source = "${path.module}/banner.sh"` together with
  # `source_hash = filesha256("${path.module}/banner.sh")`.
  files {
    path           = "/docker-entrypoint.d/99-banner.sh"
    content_base64 = base64encode("#!/bin/sh\necho web starting\n")
    mode           = "0755"
  }

  # Keyed by network name; adding or removing a network connects or
  # disconnects the container in place.
  networks = {
    (docker_network.app.name) = {
      aliases = ["www"]
    }
  }

  healthcheck {
    test           = ["CMD", "curl", "-fsS", "http://localhost/"]
    interval       = "10s"
    timeout        = "3s"
    start_period   = "5s"
    start_interval = "1s"
    retries        = 3
  }

  cap_drop = ["NET_RAW"]

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
  init         = true
  shm_size     = 134217728 # 128 MiB

  devices {
    path_on_host = "/dev/fuse"
  }

  logging {
    driver  = "json-file"
    options = { max-size = "10m", max-file = "3" }
  }

  memory       = 268435456 # 256 MiB, updated in place
  nano_cpus    = 500000000 # 0.5 CPU, updated in place
  pids_limit   = 512       # updated in place
  blkio_weight = 300       # updated in place

  oom_score_adj = 100
  annotations   = { "com.example.team" = "web" }

  state        = "running" # the default: started, and started again if found stopped
  wait         = true
  wait_timeout = "2m"
}

# A one-shot run kept as an exited container: created, started and waited for.
# A non-zero exit code fails the apply; the output is kept in logs / log_entries.
resource "docker_container" "migrate" {
  name    = "migrate"
  image   = "busybox:1.36"
  command = ["sh", "-c", "echo migrating; exit 0"]

  state               = "exited"
  expected_exit_codes = [0]
  wait_timeout        = "10m"
  logs_tail           = 200
}

# Read-only endpoint values sit beside the settings in each networks entry;
# the full ContainerInspect document is in `inspect` (object) and `inspect_json`.
output "web_ip" {
  value = docker_container.web.networks["app"].ip_address
}
