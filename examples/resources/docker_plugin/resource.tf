# With alias set, changing name later upgrades the plugin in place
# (docker plugin upgrade) instead of replacing it.
resource "docker_plugin" "sshfs" {
  name  = "vieux/sshfs:latest"
  alias = "sshfs"

  # The privileges the plugin requests; install fails if one is missing.
  grant_permissions {
    name  = "network"
    value = ["host"]
  }
  grant_permissions {
    name  = "mount"
    value = ["/var/lib/docker/plugins/"]
  }
  grant_permissions {
    name  = "device"
    value = ["/dev/fuse"]
  }
  grant_permissions {
    name  = "capabilities"
    value = ["CAP_SYS_ADMIN"]
  }

  env            = ["DEBUG=1"]
  enabled        = true
  enable_timeout = 120
}

resource "docker_volume" "remote" {
  name   = "remote-data"
  driver = docker_plugin.sshfs.alias
  driver_opts = {
    sshcmd = "user@files.example.com:/srv/data"
  }
}
