resource "docker_volume" "data" {
  name   = "app-data"
  labels = { team = "web" }
}

# A local volume backed by an NFS export.
resource "docker_volume" "nfs" {
  name   = "shared"
  driver = "local"
  driver_opts = {
    type   = "nfs"
    o      = "addr=10.0.0.10,rw,nfsvers=4"
    device = ":/export/shared"
  }
}
