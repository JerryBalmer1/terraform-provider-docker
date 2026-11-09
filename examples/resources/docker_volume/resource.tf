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

# A Swarm cluster (CSI) volume: the provider must point at a swarm manager
# and the CSI plugin must be installed on the nodes. Only availability
# changes in place.
resource "docker_volume" "csi" {
  name   = "db-data"
  driver = "csi-driver-nfs"
  cluster_volume_spec = {
    group        = "db"
    availability = "active"
    access_mode = {
      scope   = "single"
      sharing = "none"
      mount_volume = {
        fs_type = "ext4"
      }
    }
    capacity_range = {
      required_bytes = 10737418240
    }
  }
}
