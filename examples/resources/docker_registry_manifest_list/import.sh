# Import by the tag the index is pushed under. The entries are read back as
# digest references (<registry>/<repository>@sha256:...), without overrides.
terraform import docker_registry_manifest_list.app registry.example.com/team/app:1.0
