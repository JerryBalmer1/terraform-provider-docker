"""Builds reference/_gen/renames.json (Pass 5 Lane 0 rename audit).

Usage (from the repo root):
  python -I reference/_gen/generators/pass5/build_renames.py <kreuzwerker-v4.6.0-checkout> [out.json]

Inputs:
- kreuzwerker v4.6.0 tfplugindocs output (<checkout>/docs), parsed by attrs.py. Only read, never built or run.
- our schema before Pass 5: .claude/skills/terraform-provider-docker/reference/*.md (genskill output).
- the hand-written tables below: REN (our renames, previous -> Pass 5 name), UP (upstream source of every
  name we keep), KW (explicit mapping of kreuzwerker attributes that are not a plain same-name match).

Every kreuzwerker attribute must resolve (explicit KW entry, our rename, or a same-name attribute with an UP
source), otherwise the script fails. Python 3 stdlib only.
"""
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import attrs  # noqa: E402

REPO = os.path.abspath(os.path.join(HERE, "..", "..", "..", ".."))
REF = os.path.join(REPO, ".claude", "skills", "terraform-provider-docker", "reference")


def sw(defn, field=None):
    return "swagger #/definitions/%s%s" % (defn, (" " + field) if field else "")


def cs(path):
    return "compose-spec #/$defs/" + path


def cli(cmd, flag=None):
    return "docker-cli %s%s" % (cmd, (" " + flag) if flag else "")


OWN = "contract (provider's own setting, no upstream field)"

# Type renames: kind-prefixed names (data./action./ephemeral.); resources unprefixed.
TYPE_RENAMES = [
    {
        "kreuzwerker_type": "data.docker_logs",
        "previous_type": "data.docker_logs",
        "our_type": "data.docker_container_logs",
        "reason": "Q6: one logs data source per Engine operation (ContainerLogs / ServiceLogs / TaskLogs); "
        "the container one is named for its object, beside data.docker_service_logs and data.docker_task_logs.",
    },
]
PREV_TYPE = {t["previous_type"]: t["our_type"] for t in TYPE_RENAMES if t["previous_type"]}
KW_TYPE = {t["kreuzwerker_type"]: t["our_type"] for t in TYPE_RENAMES if t["kreuzwerker_type"]}

# ---------------------------------------------------------------------------------------------------------
# REN: our attributes that change in Pass 5. type (Pass 5 name) -> previous_attr -> (our_attr, transform,
# upstream, reason). our_attr None = removed. Same name with a type change is listed too.
# ---------------------------------------------------------------------------------------------------------
MOUNT_REN = {
    "mounts.tmpfs_size": ("mounts.tmpfs_options.size_bytes", "rename; flat attribute -> nested single block tmpfs_options",
                          sw("Mount", "TmpfsOptions.SizeBytes"), "General rule: Mount.TmpfsOptions.SizeBytes."),
}
SERVICE_MOUNT_REN = dict(MOUNT_REN)
SERVICE_MOUNT_REN.update({
    "mounts.volume_driver": ("mounts.volume_options.driver_config.name",
                             "rename; flat attribute -> nested blocks volume_options.driver_config",
                             sw("Mount", "VolumeOptions.DriverConfig.Name"), "General rule: Mount.VolumeOptions.DriverConfig.Name."),
    "mounts.volume_driver_opts": ("mounts.volume_options.driver_config.options",
                                  "rename; flat attribute -> nested blocks volume_options.driver_config",
                                  sw("Mount", "VolumeOptions.DriverConfig.Options"), "General rule: Mount.VolumeOptions.DriverConfig.Options."),
    "mounts.volume_labels": ("mounts.volume_options.labels", "rename; flat attribute -> nested single block volume_options",
                             sw("Mount", "VolumeOptions.Labels"), "General rule: Mount.VolumeOptions.Labels."),
})

SERVICE_REN = dict(SERVICE_MOUNT_REN)
SERVICE_REN.update({
    "dns": ("dns_config", "rename (single block)", sw("TaskSpec", "ContainerSpec.DNSConfig"),
            "General rule: ContainerSpec.DNSConfig; `dns` is the compose list of servers, not this block."),
    "dns.nameservers": ("dns_config.nameservers", "rename (parent block)", sw("TaskSpec", "ContainerSpec.DNSConfig.Nameservers"), "Parent block renamed to dns_config."),
    "dns.options": ("dns_config.options", "rename (parent block)", sw("TaskSpec", "ContainerSpec.DNSConfig.Options"), "Parent block renamed to dns_config."),
    "dns.search": ("dns_config.search", "rename (parent block)", sw("TaskSpec", "ContainerSpec.DNSConfig.Search"), "Parent block renamed to dns_config."),
    "networks": ("networks", "set(string) of network names -> map keyed by network name (NetworkAttachmentConfig); old values become the keys",
                 sw("TaskSpec", "Networks") + "; " + cs("service networks (map form)"),
                 "General rule + Q1 shape: one networks map replaces networks and networks_advanced (same as docker_container)."),
    "networks_advanced": ("networks", "list of blocks keyed by name -> map keyed by network name (merged into networks)",
                          sw("NetworkAttachmentConfig"), "networks_advanced matches no upstream name; TaskSpec.Networks is `networks` (Q1 shape)."),
    "networks_advanced.name": ("networks", "block attribute -> map key", sw("NetworkAttachmentConfig", "Target"), "The network name or ID is the map key."),
    "networks_advanced.aliases": ("networks.aliases", "rename (parent block -> map value)", sw("NetworkAttachmentConfig", "Aliases"), "Moves into the networks map."),
})


def prefixed(ren, prefix):
    return {prefix + k: (prefix + v[0] if v[0] else None,) + v[1:] for k, v in ren.items()}


REN = {
    "docker_container": dict(MOUNT_REN, **{
        "capabilities": (None, "single block {add, drop} -> flattened into top-level cap_add / cap_drop", sw("HostConfig", "CapAdd / CapDrop"),
                         "General rule: HostConfig has CapAdd/CapDrop, no capabilities object (compose cap_add/cap_drop; docker_service already uses them)."),
        "capabilities.add": ("cap_add", "rename; block attribute -> top-level set(string)", sw("HostConfig", "CapAdd"), "General rule: HostConfig.CapAdd (compose cap_add)."),
        "capabilities.drop": ("cap_drop", "rename; block attribute -> top-level set(string)", sw("HostConfig", "CapDrop"), "General rule: HostConfig.CapDrop (compose cap_drop)."),
        "devices.container_path": ("devices.path_in_container", "rename", sw("DeviceMapping", "PathInContainer"), "General rule: DeviceMapping.PathInContainer."),
        "devices.host_path": ("devices.path_on_host", "rename", sw("DeviceMapping", "PathOnHost"), "General rule: DeviceMapping.PathOnHost."),
        "devices.permissions": ("devices.cgroup_permissions", "rename", sw("DeviceMapping", "CgroupPermissions"), "General rule: DeviceMapping.CgroupPermissions."),
        "log_driver": ("logging.driver", "rename; flat attribute -> single block logging", cs("service logging.driver") + "; " + sw("HostConfig", "LogConfig.Type"),
                       "General rule: log_opts matches no upstream name; one logging block shape shared with docker_service (compose logging)."),
        "log_opts": ("logging.options", "rename; flat attribute -> single block logging", cs("service logging.options") + "; " + sw("HostConfig", "LogConfig.Config"),
                     "General rule: compose logging.options (CLI --log-opt is singular, Engine LogConfig.Config)."),
        "max_retry_count": ("maximum_retry_count", "rename", sw("RestartPolicy", "MaximumRetryCount"), "General rule: RestartPolicy.MaximumRetryCount."),
        "networks_advanced": ("networks", "list of blocks keyed by name -> map keyed by network name; connect/disconnect in place (NetworkConnect/NetworkDisconnect)",
                              sw("NetworkingConfig", "EndpointsConfig"), "Q1: map of EndpointSettings keyed by network name."),
        "networks_advanced.name": ("networks", "block attribute -> map key", sw("NetworkingConfig", "EndpointsConfig (map key)"), "Q1: the network name is the map key."),
        "networks_advanced.aliases": ("networks.aliases", "rename (parent block -> map value)", sw("EndpointSettings", "Aliases"), "Q1."),
        "networks_advanced.ipv4_address": ("networks.ipv4_address", "rename (parent block -> map value)", sw("EndpointIPAMConfig", "IPv4Address"), "Q1."),
        "networks_advanced.ipv6_address": ("networks.ipv6_address", "rename (parent block -> map value)", sw("EndpointIPAMConfig", "IPv6Address"), "Q1."),
        "ports.external": ("ports.host_port", "rename", sw("PortBinding", "HostPort"), "General rule: PortBinding.HostPort."),
        "ports.internal": ("ports.container_port", "rename", sw("PortMap", "key <port>/<protocol>"), "General rule: the PortMap key's port."),
        "ports.ip": ("ports.host_ip", "rename", sw("PortBinding", "HostIp"), "General rule: PortBinding.HostIp."),
        "upload": ("files", "rename (list of blocks)", "swagger PutContainerArchive", "Q9: copy-in block is `files`."),
        "upload.content": ("files.content", "rename (parent block)", "none (tar entry body)", "Q9."),
        "upload.content_base64": ("files.content_base64", "rename (parent block)", "none (tar entry body)", "Q9."),
        "upload.executable": ("files.mode", "bool -> mode: true = \"0744\" (unless permissions set); removed as a separate attribute",
                              "none (tar header Mode)", "Q9: mode is the one permission input."),
        "upload.file": ("files.path", "rename", "swagger PutContainerArchive path", "Q9: path is the container path."),
        "upload.group": ("files.gid", "rename", sw("ConfigReference", "File.GID") + " (tar header Gid)", "Q9 + General rule: uid/gid as in swarm file references (judgment: Q9 lists no owner fields)."),
        "upload.owner": ("files.uid", "rename", sw("ConfigReference", "File.UID") + " (tar header Uid)", "Q9 + General rule: uid/gid as in swarm file references (judgment: Q9 lists no owner fields)."),
        "upload.permissions": ("files.mode", "rename", sw("ConfigReference", "File.Mode") + " (tar header Mode)", "Q9: mode."),
        "upload.source": ("files.source", "rename (parent block)", "none (host path read by the provider)", "Q9: source is a host path."),
        "upload.source_hash": ("files.source_hash", "rename (parent block)", OWN, "Q9 keeps a re-upload trigger for source (judgment: not in the Q9 list)."),
    }),
    "docker_service": SERVICE_REN,
    "docker_stack": prefixed(SERVICE_REN, "service."),
    "docker_image": {
        "image_id": ("id", "removed; same value as id (ImageInspect.Id)", sw("ImageInspect", "Id"), "Q46: id is the image ID; image_id removed on docker_image."),
        "repo_digest": ("image_digest_ref", "rename; upstream RepoDigests list goes in the structured output", sw("ImageInspect", "RepoDigests"),
                        "Q44: contract handoff name, no alias."),
    },
    "data.docker_image": {
        "image_id": ("id", "removed; same value as id (ImageInspect.Id)", sw("ImageInspect", "Id"), "Q46."),
        "repo_digest": ("image_digest_ref", "rename; upstream RepoDigests list goes in the structured output", sw("ImageInspect", "RepoDigests"), "Q44."),
    },
    "docker_registry_image": {
        "sha256_digest": ("digest", "rename (image_digest_ref added beside it)", sw("DistributionInspect", "Descriptor.digest"), "Q45, no alias."),
    },
    "data.docker_registry_image": {
        "sha256_digest": ("digest", "rename (image_digest_ref added beside it)", sw("DistributionInspect", "Descriptor.digest"), "Q45, no alias."),
    },
    "data.docker_container_logs": {
        "logs_list_string": ("log_entries.message", "list(string) -> list(object); each line's text is .message (raw `logs` string and timestamp/stream/source added)",
                             "contract (Naming: Logs)", "Q6: logs contract (logs, log_entries)."),
        "show_stderr": ("stderr", "rename", "swagger ContainerLogs query stderr", "General rule: ContainerLogs query parameter."),
        "show_stdout": ("stdout", "rename", "swagger ContainerLogs query stdout", "General rule: ContainerLogs query parameter."),
        "timestamps": (None, "removed; timestamps are always requested", "swagger ContainerLogs query timestamps",
                       "Q6 logs contract: log_entries.timestamp always comes from timestamps=true."),
    },
    "docker_network": {
        "ipv6": ("enable_ipv6", "rename", sw("NetworkCreateRequest", "EnableIPv6") + "; " + cs("network enable_ipv6"), "General rule (CONTRACT.md example EnableIPv6)."),
    },
    "data.docker_network": {
        "ipv6": ("enable_ipv6", "rename", sw("Network", "EnableIPv6"), "General rule (CONTRACT.md example EnableIPv6)."),
    },
    "docker_compose": {
        "id": ("id", "identity; id = name", cs("top-level name"), "Q41: id = name."),
        "project_name": ("name", "rename", cs("top-level name"), "Q41: compose-spec top-level name."),
        "working_dir": ("project_directory", "rename", cli("compose", "--project-directory"),
                        "General rule: the compose CLI flag (also kreuzwerker's name); working_dir is a service key in compose-spec."),
    },
    "docker_buildx_builder": {
        "buildkit_config": ("buildkitd_config", "rename", cli("buildx create", "--buildkitd-config"), "General rule: flags drop dashes."),
        "buildkit_config_content": ("buildkitd_config_content", "rename", cli("buildx create", "--buildkitd-config") + " (inline content)",
                                    "Follows buildkitd_config."),
        "buildkit_flags": ("buildkitd_flags", "rename", cli("buildx create", "--buildkitd-flags"), "General rule: flags drop dashes."),
    },
    "docker_swarm_init": {
        "id": ("id", "identity; id = ClusterInfo.ID (already the case)", sw("ClusterInfo", "ID"), "Q38: id is the contract identity; cluster_id kept."),
    },
    "action.docker_container_export": {
        "container": ("container_id", "rename (accepts ID or name as before)", sw("ContainerSummary", "Id"), "Q40: object-named target input."),
        "path": ("output", "rename", cli("container export", "--output"), "General rule: CLI flag name."),
    },
    "action.docker_exec": {
        "container": ("container_id", "rename (accepts ID or name as before)", sw("ContainerSummary", "Id"), "Q40."),
        "service": ("service_id", "rename (accepts ID or name as before)", sw("Task", "ServiceID"), "Q40."),
    },
    "action.docker_image_load": {
        "path": ("input", "rename", cli("image load", "--input"), "General rule: CLI flag name."),
    },
    "action.docker_image_save": {
        "path": ("output", "rename", cli("image save", "--output"), "General rule: CLI flag name."),
    },
    "action.docker_node_activate": {
        "node": ("node_id", "rename (accepts ID or hostname as before)", sw("Task", "NodeID"), "Q40."),
    },
    "action.docker_node_drain": {
        "node": ("node_id", "rename (accepts ID or hostname as before)", sw("Task", "NodeID"), "Q40."),
    },
    "action.docker_service_force_update": {
        "service": ("service_id", "rename (accepts ID or name as before)", sw("Task", "ServiceID"), "Q40."),
    },
    "action.docker_service_rollback": {
        "service": ("service_id", "rename (accepts ID or name as before)", sw("Task", "ServiceID"), "Q40."),
    },
    "action.docker_service_scale": {
        "service": ("service_id", "rename (accepts ID or name as before)", sw("Task", "ServiceID"), "Q40."),
    },
    "action.docker_system_prune": {
        "all_images": ("all", "rename", cli("system prune", "--all"), "General rule: CLI flag name (kreuzwerker uses it too)."),
    },
}

# ---------------------------------------------------------------------------------------------------------
# UP: upstream source of names we keep (needed for every same-name kreuzwerker match).
# ---------------------------------------------------------------------------------------------------------
HC = lambda f: sw("HostConfig", f)  # noqa: E731
CC = lambda f: sw("ContainerConfig", f)  # noqa: E731
CSPEC = lambda f: sw("TaskSpec", "ContainerSpec." + f)  # noqa: E731
HEALTH = {
    "healthcheck": cs("service healthcheck") + "; " + sw("HealthConfig"),
    "healthcheck.interval": sw("HealthConfig", "Interval"),
    "healthcheck.retries": sw("HealthConfig", "Retries"),
    "healthcheck.start_period": sw("HealthConfig", "StartPeriod"),
    "healthcheck.test": sw("HealthConfig", "Test"),
    "healthcheck.timeout": sw("HealthConfig", "Timeout"),
}
SERVICE_UP = dict(HEALTH, **{
    "id": sw("Service", "ID"),
    "name": sw("ServiceSpec", "Name"),
    "labels": sw("ServiceSpec", "Labels"),
    "image": CSPEC("Image"), "args": CSPEC("Args"), "command": CSPEC("Command"), "dir": CSPEC("Dir"),
    "env": CSPEC("Env"), "hostname": CSPEC("Hostname"), "hosts": CSPEC("Hosts"), "user": CSPEC("User"),
    "sysctls": CSPEC("Sysctls"), "stop_grace_period": CSPEC("StopGracePeriod"),
    "cap_add": cs("service cap_add") + "; " + CSPEC("CapabilityAdd"),
    "cap_drop": cs("service cap_drop") + "; " + CSPEC("CapabilityDrop"),
    "container_labels": CSPEC("Labels") + " (prefixed: ServiceSpec.Labels is `labels`)",
    "force_update": sw("TaskSpec", "ForceUpdate"),
    "replicas": sw("ServiceSpec", "Mode.Replicated.Replicas"),
    "mode": cs("deploy mode") + "; " + sw("ServiceSpec", "Mode"),
    "configs": CSPEC("Configs"), "configs.config_id": sw("ConfigReference", "ConfigID"),
    "configs.config_name": sw("ConfigReference", "ConfigName"), "configs.file_name": sw("ConfigReference", "File.Name"),
    "configs.uid": sw("ConfigReference", "File.UID"), "configs.gid": sw("ConfigReference", "File.GID"),
    "configs.mode": sw("ConfigReference", "File.Mode"),
    "secrets": CSPEC("Secrets"), "secrets.secret_id": sw("SecretReference", "SecretID"),
    "secrets.secret_name": sw("SecretReference", "SecretName"), "secrets.file_name": sw("SecretReference", "File.Name"),
    "secrets.uid": sw("SecretReference", "File.UID"), "secrets.gid": sw("SecretReference", "File.GID"),
    "secrets.mode": sw("SecretReference", "File.Mode"),
    "converge_config": OWN, "converge_config.delay": OWN, "converge_config.timeout": OWN,
    "endpoint_spec": sw("EndpointSpec"), "endpoint_spec.mode": sw("EndpointSpec", "Mode"),
    "endpoint_spec.ports": sw("EndpointSpec", "Ports"), "endpoint_spec.ports.target_port": sw("EndpointPortConfig", "TargetPort"),
    "endpoint_spec.ports.published_port": sw("EndpointPortConfig", "PublishedPort"),
    "endpoint_spec.ports.protocol": sw("EndpointPortConfig", "Protocol"),
    "endpoint_spec.ports.publish_mode": sw("EndpointPortConfig", "PublishMode"),
    "logging": cs("service logging") + "; " + sw("TaskSpec", "LogDriver"),
    "logging.driver": cs("service logging.driver") + "; " + sw("TaskSpec", "LogDriver.Name"),
    "logging.options": cs("service logging.options") + "; " + sw("TaskSpec", "LogDriver.Options"),
    "mounts": CSPEC("Mounts"), "mounts.target": sw("Mount", "Target"), "mounts.source": sw("Mount", "Source"),
    "mounts.type": sw("Mount", "Type"), "mounts.read_only": sw("Mount", "ReadOnly"),
    "placement": sw("TaskSpec", "Placement"), "placement.constraints": sw("TaskSpec", "Placement.Constraints"),
    "placement.max_replicas": sw("TaskSpec", "Placement.MaxReplicas"), "placement.preferences": sw("TaskSpec", "Placement.Preferences"),
    "resources": sw("TaskSpec", "Resources"), "resources.limits": sw("TaskSpec", "Resources.Limits"),
    "resources.limits.memory_bytes": sw("Limit", "MemoryBytes"), "resources.limits.nano_cpus": sw("Limit", "NanoCPUs"),
    "resources.reservations": sw("TaskSpec", "Resources.Reservations"),
    "resources.reservations.memory_bytes": sw("ResourceObject", "MemoryBytes"),
    "resources.reservations.nano_cpus": sw("ResourceObject", "NanoCPUs"),
    "restart_policy": sw("TaskSpec", "RestartPolicy"), "restart_policy.condition": sw("TaskSpec", "RestartPolicy.Condition"),
    "restart_policy.delay": sw("TaskSpec", "RestartPolicy.Delay"), "restart_policy.max_attempts": sw("TaskSpec", "RestartPolicy.MaxAttempts"),
    "restart_policy.window": sw("TaskSpec", "RestartPolicy.Window"),
})
for blk, fld in (("update_config", "UpdateConfig"), ("rollback_config", "RollbackConfig")):
    SERVICE_UP[blk] = sw("ServiceSpec", fld)
    for a, f in (("delay", "Delay"), ("failure_action", "FailureAction"), ("max_failure_ratio", "MaxFailureRatio"),
                 ("monitor", "Monitor"), ("order", "Order"), ("parallelism", "Parallelism")):
        SERVICE_UP[blk + "." + a] = sw("ServiceSpec", fld + "." + f)

BUILD_UP = {
    "build": cs("service build"), "build.context": cs("build context"), "build.dockerfile": cs("build dockerfile"),
    "build.builder": cli("buildx build", "--builder"), "build.cache_from": cs("build cache_from"),
    "build.labels": cs("build labels"), "build.no_cache": cs("build no_cache"),
    "build.platform": "swagger ImageBuild query platform", "build.target": cs("build target"),
    "build.secrets": cs("build secrets") + "; " + cli("buildx build", "--secret"),
    "build.secrets.id": cli("buildx build", "--secret id="), "build.secrets.env": cli("buildx build", "--secret env="),
    "build.secrets.src": cli("buildx build", "--secret src="),
}

UP = {
    "docker_container": dict(HEALTH, **{
        "id": sw("ContainerInspectResponse", "Id"), "name": "swagger ContainerCreate query name", "image": CC("Image"),
        "command": cs("service command") + " (Engine ContainerConfig.Cmd)", "entrypoint": CC("Entrypoint"), "env": CC("Env"),
        "hostname": CC("Hostname"), "user": CC("User"), "working_dir": CC("WorkingDir"), "tty": CC("Tty"),
        "stop_signal": CC("StopSignal"), "stop_timeout": CC("StopTimeout"), "labels": CC("Labels"),
        "stdin_open": cs("service stdin_open") + " (Engine ContainerConfig.OpenStdin)",
        "cpu_shares": sw("Resources", "CpuShares"), "memory": sw("Resources", "Memory"), "memory_swap": sw("Resources", "MemorySwap"),
        "nano_cpus": sw("Resources", "NanoCpus"), "dns": HC("Dns"), "dns_search": HC("DnsSearch"), "extra_hosts": HC("ExtraHosts"),
        "privileged": HC("Privileged"), "security_opt": HC("SecurityOpt"), "sysctls": HC("Sysctls"),
        "read_only": cs("service read_only") + " (Engine HostConfig.ReadonlyRootfs)",
        "restart": cs("service restart") + "; " + cli("container run", "--restart"),
        "gpus": cs("service gpus") + "; " + cli("container run", "--gpus"),
        "must_run": OWN, "running": sw("ContainerState", "Running"),
        "wait": cli("compose up", "--wait"), "wait_timeout": cli("compose up", "--wait-timeout"),
        "devices": sw("Resources", "Devices"),
        "mounts": HC("Mounts"), "mounts.target": sw("Mount", "Target"), "mounts.source": sw("Mount", "Source"),
        "mounts.type": sw("Mount", "Type"), "mounts.read_only": sw("Mount", "ReadOnly"),
        "mounts.selinux_relabel": cli("container run", "--volume :z/:Z suffix") + " (no Mount field)",
        "ports": HC("PortBindings"), "ports.protocol": sw("PortMap", "key <port>/<protocol>"),
        "ulimits": sw("Resources", "Ulimits"), "ulimits.name": sw("Resources", "Ulimits.Name"),
        "ulimits.soft": sw("Resources", "Ulimits.Soft"), "ulimits.hard": sw("Resources", "Ulimits.Hard"),
    }),
    "docker_service": SERVICE_UP,
    "docker_image": dict(BUILD_UP, **{
        "id": sw("ImageInspect", "Id"), "name": "swagger ImageCreate query fromImage",
        "platform": "swagger ImageCreate query platform", "force_remove": OWN, "keep_locally": OWN, "triggers": OWN,
    }),
    "docker_registry_image": {
        "id": OWN + " (pushed reference)", "name": "swagger ImagePush path name", "insecure_skip_verify": OWN,
        "keep_remotely": OWN, "triggers": OWN,
    },
    "docker_buildx_builder": {
        "id": OWN + " (BuildKit container ID)", "name": cli("buildx create", "--name"), "driver": cli("buildx create", "--driver"),
        "bootstrap": cli("buildx create", "--bootstrap"), "endpoint": cli("buildx create", "[ENDPOINT]"),
        "docker_container": cli("buildx create", "--driver-opt (docker-container driver)"),
        "docker_container.env": cli("buildx create", "--driver-opt env.<key>"),
        "docker_container.image": cli("buildx create", "--driver-opt image"),
        "docker_container.memory": cli("buildx create", "--driver-opt memory"),
        "docker_container.network": cli("buildx create", "--driver-opt network"),
        "docker_container.restart_policy": cli("buildx create", "--driver-opt restart-policy"),
    },
    "docker_compose": {
        "profiles": cli("compose", "--profile"), "wait": cli("compose up", "--wait"),
        "wait_timeout": cli("compose up", "--wait-timeout"), "env_file": cli("compose", "--env-file"),
        "files": cli("compose", "--file") + " (repeatable; plural list)",
    },
    "docker_config": {
        "id": sw("Config", "ID"), "name": sw("ConfigSpec", "Name"), "data": sw("ConfigSpec", "Data"), "labels": sw("ConfigSpec", "Labels"),
    },
    "docker_secret": {
        "id": sw("Secret", "ID"), "name": sw("SecretSpec", "Name"), "data": sw("SecretSpec", "Data"), "labels": sw("SecretSpec", "Labels"),
    },
    "docker_network": {
        "id": sw("Network", "Id"), "name": sw("NetworkCreateRequest", "Name"), "driver": sw("NetworkCreateRequest", "Driver"),
        "attachable": sw("NetworkCreateRequest", "Attachable"), "ingress": sw("NetworkCreateRequest", "Ingress"),
        "internal": sw("NetworkCreateRequest", "Internal"), "ipam_driver": sw("IPAM", "Driver") + "; " + cli("network create", "--ipam-driver"),
        "ipam_config": sw("IPAM", "Config") + " (flattened prefix)", "ipam_config.aux_address": cli("network create", "--aux-address") + " (Engine IPAMConfig.AuxiliaryAddresses)",
        "ipam_config.gateway": sw("IPAMConfig", "Gateway"), "ipam_config.ip_range": sw("IPAMConfig", "IPRange"),
        "ipam_config.subnet": sw("IPAMConfig", "Subnet"), "labels": sw("NetworkCreateRequest", "Labels"),
        "options": sw("NetworkCreateRequest", "Options"), "scope": sw("Network", "Scope"),
    },
    "docker_volume": {
        "id": OWN + " (volume name)", "name": sw("VolumeCreateOptions", "Name"), "driver": sw("VolumeCreateOptions", "Driver"),
        "driver_opts": sw("VolumeCreateOptions", "DriverOpts"), "labels": sw("VolumeCreateOptions", "Labels"),
        "mountpoint": sw("Volume", "Mountpoint"),
    },
    "docker_plugin": {
        "id": sw("Plugin", "Id"), "name": sw("Plugin", "Name"), "alias": cli("plugin install", "--alias"),
        "enable_timeout": cli("plugin enable", "--timeout"), "enabled": sw("Plugin", "Enabled"), "env": sw("Plugin", "Settings.Env"),
        "force_destroy": cli("plugin rm", "--force"), "force_disable": cli("plugin disable", "--force"),
        "grant_all_permissions": cli("plugin install", "--grant-all-permissions"), "grant_permissions": sw("PluginPrivilege"),
        "grant_permissions.name": sw("PluginPrivilege", "Name"), "grant_permissions.value": sw("PluginPrivilege", "Value"),
        "plugin_reference": sw("Plugin", "PluginReference"),
    },
    "docker_tag": {
        "id": OWN, "source_image": cli("image tag", "SOURCE_IMAGE"), "target_image": cli("image tag", "TARGET_IMAGE"),
        "source_image_id": sw("ImageInspect", "Id"),
    },
    "data.docker_containers": {
        "containers": "swagger ContainerList response", "containers.command": sw("ContainerSummary", "Command"),
        "containers.created": sw("ContainerSummary", "Created"), "containers.id": sw("ContainerSummary", "Id"),
        "containers.image": sw("ContainerSummary", "Image"), "containers.image_id": sw("ContainerSummary", "ImageID"),
        "containers.labels": sw("ContainerSummary", "Labels"), "containers.names": sw("ContainerSummary", "Names"),
        "containers.state": sw("ContainerSummary", "State"), "containers.status": sw("ContainerSummary", "Status"),
    },
    "data.docker_image": {"id": sw("ImageInspect", "Id"), "name": "swagger ImageInspect path name"},
    "data.docker_container_logs": {
        "details": "swagger ContainerLogs query details", "since": "swagger ContainerLogs query since",
        "until": "swagger ContainerLogs query until", "tail": "swagger ContainerLogs query tail", "id": OWN,
    },
    "data.docker_network": {
        "id": sw("Network", "Id"), "name": sw("Network", "Name"), "driver": sw("Network", "Driver"), "internal": sw("Network", "Internal"),
        "ipam_config": sw("IPAM", "Config"), "ipam_config.aux_address": sw("IPAMConfig", "AuxiliaryAddresses") + " (CLI --aux-address)",
        "ipam_config.gateway": sw("IPAMConfig", "Gateway"), "ipam_config.ip_range": sw("IPAMConfig", "IPRange"),
        "ipam_config.subnet": sw("IPAMConfig", "Subnet"), "options": sw("Network", "Options"), "scope": sw("Network", "Scope"),
    },
    "data.docker_plugin": {
        "id": sw("Plugin", "Id"), "name": sw("Plugin", "Name"), "alias": cli("plugin install", "--alias"), "enabled": sw("Plugin", "Enabled"),
        "env": sw("Plugin", "Settings.Env"), "plugin_reference": sw("Plugin", "PluginReference"),
    },
    "data.docker_registry_image": {"id": OWN, "name": "swagger DistributionInspect path name"},
    "data.docker_registry_image_manifests": {
        "name": "registry API manifests/<reference>", "insecure_skip_verify": OWN,
        "manifests": "OCI image index manifests[]", "manifests.architecture": "OCI image index manifests[].platform.architecture",
        "manifests.os": "OCI image index manifests[].platform.os", "manifests.media_type": "OCI image index manifests[].mediaType",
        "manifests.digest": "OCI image index manifests[].digest",
    },
    "data.docker_registry_image_tags": {
        "name": "registry API <name>/tags/list", "insecure_skip_verify": OWN, "tags": "registry API tags/list tags",
    },
    "action.docker_exec": {
        "command": cli("container exec", "COMMAND") + " (Engine ExecConfig.Cmd)", "env": sw("ExecConfig", "Env"),
        "privileged": sw("ExecConfig", "Privileged"), "user": sw("ExecConfig", "User"),
        "workdir": cli("container exec", "--workdir") + " (Engine ExecConfig.WorkingDir)",
    },
    "action.docker_image_import": {
        "source": "swagger ImageCreate query fromSrc (" + cli("image import", "file|URL") + ")",
        "changes": "swagger ImageCreate query changes", "message": "swagger ImageCreate query message",
        "platform": "swagger ImageCreate query platform",
    },
    "action.docker_image_save": {"images": "swagger ImageGetAll query names"},
    "action.docker_system_prune": {"volumes": cli("system prune", "--volumes")},
}

# ---------------------------------------------------------------------------------------------------------
# KW: explicit mapping of kreuzwerker attributes. kreuzwerker type (kind-prefixed) -> kw path ->
#   ("map", our_attr, transform, upstream, reason[, previous_attr])  or  ("drop", upstream, reason)
# A gap reason names the upstream-derived attribute a later pass would add.
# ---------------------------------------------------------------------------------------------------------
def M(our, transform, upstream, reason, prev="auto"):
    return ("map", our, transform, upstream, reason, prev)


def D(upstream, reason):
    return ("drop", upstream, reason)


def GAP(name, upstream, extra=""):
    return D(upstream, "gap: not in our schema; if added, name it `%s`.%s" % (name, (" " + extra) if extra else ""))


BLOCKS_TO_MAP = "set of blocks {label, value} -> map(string)"
TIMEOUTS = D("none", "dropped: SDKv2 timeouts block not provided (no per-operation timeouts in our schema).")


def labels_map(target, upstream):
    return {
        "labels": M(target, BLOCKS_TO_MAP, upstream, "Blocks become a map.", prev=target),
        "labels.label": M(target, "block attribute -> map key", upstream, "Map key.", prev=target),
        "labels.value": M(target, "block attribute -> map value", upstream, "Map value.", prev=target),
    }


def mount_kw(prefix, has_volume_options):
    """kreuzwerker mounts.* sub-blocks (shared by container and service)."""
    p = prefix + "mounts."
    out = {
        p + "bind_options": GAP("mounts.bind_options", sw("Mount", "BindOptions")),
        p + "bind_options.propagation": GAP("mounts.bind_options.propagation", sw("Mount", "BindOptions.Propagation")),
        p + "tmpfs_options": M("mounts.tmpfs_options", "list (max 1) block -> single block", sw("Mount", "TmpfsOptions"),
                               "Block introduced by the tmpfs_size rename.", prev=None),
        p + "tmpfs_options.mode": GAP("mounts.tmpfs_options.mode", sw("Mount", "TmpfsOptions.Mode")),
        p + "tmpfs_options.size_bytes": M("mounts.tmpfs_options.size_bytes", "identity (our previous flat tmpfs_size)",
                                          sw("Mount", "TmpfsOptions.SizeBytes"), "Same upstream field.", prev="mounts.tmpfs_size"),
        p + "volume_options.no_copy": GAP("mounts.volume_options.no_copy", sw("Mount", "VolumeOptions.NoCopy")),
    }
    vo = {
        "volume_options": ("mounts.volume_options", sw("Mount", "VolumeOptions"), None),
        "volume_options.driver_name": ("mounts.volume_options.driver_config.name", sw("Mount", "VolumeOptions.DriverConfig.Name"), "mounts.volume_driver"),
        "volume_options.driver_options": ("mounts.volume_options.driver_config.options", sw("Mount", "VolumeOptions.DriverConfig.Options"), "mounts.volume_driver_opts"),
        "volume_options.labels": ("mounts.volume_options.labels", sw("Mount", "VolumeOptions.Labels"), "mounts.volume_labels"),
        "volume_options.labels.label": ("mounts.volume_options.labels", sw("Mount", "VolumeOptions.Labels"), "mounts.volume_labels"),
        "volume_options.labels.value": ("mounts.volume_options.labels", sw("Mount", "VolumeOptions.Labels"), "mounts.volume_labels"),
        "volume_options.subpath": ("mounts.volume_options.subpath", sw("Mount", "VolumeOptions.Subpath"), None),
    }
    for k, (our, up, prev) in vo.items():
        if has_volume_options and k != "volume_options.subpath":
            tr = {"volume_options": "list (max 1) block -> single block",
                  "volume_options.labels": BLOCKS_TO_MAP, "volume_options.labels.label": "block attribute -> map key",
                  "volume_options.labels.value": "block attribute -> map value"}.get(k, "rename")
            out[p + k] = M(our, tr, up, "Mount.VolumeOptions path.", prev=prev)
        else:
            out[p + k] = GAP(our, up)
    return out


C = "docker_container"
KW = {}
KW[C] = dict(labels_map("labels", CC("Labels")), **mount_kw("", False), **{
    "attach": D(sw("ContainerAttach"), "dropped: the resource never attaches; use wait for readiness and data.docker_container_logs for output."),
    "logs": D("none", "dropped: use data.docker_container_logs."),
    "container_logs": D("none", "dropped: use data.docker_container_logs (logs / log_entries)."),
    "start": D("none", "dropped: the container is always started; must_run keeps it running."),
    "rm": GAP("auto_remove", HC("AutoRemove")),
    "remove_volumes": D("swagger ContainerDelete query v", "dropped: destroy keeps volumes; manage them with docker_volume."),
    "destroy_grace_seconds": D("none", "dropped: destroy force-removes the container; stop_timeout covers graceful stops."),
    "container_read_refresh_timeout_milliseconds": D("none", "dropped: wait / wait_timeout cover waiting for the container."),
    "timeouts": TIMEOUTS, "timeouts.create": TIMEOUTS, "timeouts.delete": TIMEOUTS, "timeouts.update": TIMEOUTS,
    "bridge": D(sw("NetworkSettings", "Bridge"), "dropped: deprecated upstream field."),
    "exit_code": GAP("exit_code", sw("ContainerState", "ExitCode")),
    "cgroup_parent": GAP("cgroup_parent", sw("Resources", "CgroupParent")),
    "cgroupns_mode": GAP("cgroupns_mode", HC("CgroupnsMode")),
    "cpu_period": GAP("cpu_period", sw("Resources", "CpuPeriod")),
    "cpu_quota": GAP("cpu_quota", sw("Resources", "CpuQuota")),
    "cpu_set": GAP("cpuset_cpus", sw("Resources", "CpusetCpus")),
    "cpus": M("nano_cpus", "string decimal CPUs -> number nano CPUs (x 1e9)", sw("Resources", "NanoCpus"), "Same setting in Engine units."),
    "device_cgroup_rules": GAP("device_cgroup_rules", sw("Resources", "DeviceCgroupRules")),
    "dns_opts": GAP("dns_options", HC("DnsOptions")),
    "domainname": GAP("domainname", CC("Domainname")),
    "group_add": GAP("group_add", HC("GroupAdd")),
    "init": GAP("init", sw("Resources", "Init")),
    "ipc_mode": GAP("ipc_mode", HC("IpcMode")),
    "memory_reservation": GAP("memory_reservation", sw("Resources", "MemoryReservation")),
    "network_mode": GAP("network_mode", HC("NetworkMode")),
    "pid_mode": GAP("pid_mode", HC("PidMode")),
    "platform": GAP("platform", "swagger ContainerCreate query platform"),
    "publish_all_ports": GAP("publish_all_ports", HC("PublishAllPorts")),
    "runtime": GAP("runtime", HC("Runtime")),
    "shm_size": GAP("shm_size", HC("ShmSize"), "Bytes; kreuzwerker uses MB."),
    "storage_opts": GAP("storage_opt", HC("StorageOpt")),
    "tmpfs": GAP("tmpfs", HC("Tmpfs"), "Today: mounts with type = \"tmpfs\"."),
    "userns_mode": GAP("userns_mode", HC("UsernsMode")),
    "healthcheck.start_interval": GAP("healthcheck.start_interval", sw("HealthConfig", "StartInterval")),
    "env": M("env", "set(string) \"KEY=VALUE\" -> map(string)", CC("Env"), "Same field, map form."),
    "memory": M("memory", "number MB -> number bytes (x 1048576)", sw("Resources", "Memory"), "Engine units."),
    "memory_swap": M("memory_swap", "number MB -> number bytes (x 1048576; -1 stays -1)", sw("Resources", "MemorySwap"), "Engine units."),
    "wait_timeout": M("wait_timeout", "number seconds -> string Go duration (\"<n>s\")", cli("compose up", "--wait-timeout"), "Duration string."),
    "security_opts": M("security_opt", "rename", HC("SecurityOpt"), "General rule: HostConfig.SecurityOpt."),
    "host": M("extra_hosts", "set of blocks {host, ip} -> list(string) \"host:ip\"", HC("ExtraHosts"), "Same setting, CLI --add-host form."),
    "host.host": M("extra_hosts", "block attribute -> part before ':'", HC("ExtraHosts"), "Same setting."),
    "host.ip": M("extra_hosts", "block attribute -> part after ':'", HC("ExtraHosts"), "Same setting."),
    "ulimit": M("ulimits", "rename (set of blocks -> list of blocks)", sw("Resources", "Ulimits"), "General rule: Resources.Ulimits."),
    "ulimit.hard": M("ulimits.hard", "rename (parent block)", sw("Resources", "Ulimits.Hard"), "Parent renamed."),
    "ulimit.name": M("ulimits.name", "rename (parent block)", sw("Resources", "Ulimits.Name"), "Parent renamed."),
    "ulimit.soft": M("ulimits.soft", "rename (parent block)", sw("Resources", "Ulimits.Soft"), "Parent renamed."),
    "volumes": M("mounts", "set of blocks -> mounts entries (type bind when host_path, volume when volume_name)", HC("Mounts"), "Same setting as Mount objects."),
    "volumes.container_path": M("mounts.target", "rename", sw("Mount", "Target"), "Mount.Target."),
    "volumes.host_path": M("mounts.source", "rename; sets mounts.type = \"bind\"", sw("Mount", "Source"), "Mount.Source."),
    "volumes.volume_name": M("mounts.source", "rename; sets mounts.type = \"volume\"", sw("Mount", "Source"), "Mount.Source."),
    "volumes.read_only": M("mounts.read_only", "identity (moved to mounts)", sw("Mount", "ReadOnly"), "Mount.ReadOnly."),
    "volumes.selinux_relabel": M("mounts.selinux_relabel", "identity (moved to mounts)", UP[C]["mounts.selinux_relabel"], "Kept: no upstream Mount field."),
    "volumes.from_container": GAP("volumes_from", HC("VolumesFrom")),
    "networks_advanced.driver_opts": M("networks.driver_opts", "set(string) \"k=v\" -> map(string)", sw("EndpointSettings", "DriverOpts"), "Q1.", prev=None),
    "networks_advanced.gw_priority": M("networks.gw_priority", "rename (parent block -> map value)", sw("EndpointSettings", "GwPriority"), "Q1.", prev=None),
    "networks_advanced.link_local_ips": M("networks.link_local_ips", "rename (parent block -> map value)", sw("EndpointIPAMConfig", "LinkLocalIPs"), "Q1 (mirrors EndpointSettings).", prev=None),
    "networks_advanced.mac_address": M("networks.mac_address", "rename (parent block -> map value)", sw("EndpointSettings", "MacAddress"), "Q1.", prev=None),
    "network_data": M("networks", "computed list(object) keyed by network_name -> computed fields of the networks map", sw("NetworkSettings", "Networks"),
                      "Q1: networks mirrors EndpointSettings, including its read-only fields.", prev=None),
    "network_data.network_name": M("networks", "object attribute -> map key", sw("NetworkSettings", "Networks (map key)"), "Q1.", prev=None),
    "network_data.gateway": M("networks.gateway", "rename (computed)", sw("EndpointSettings", "Gateway"), "Q1.", prev=None),
    "network_data.global_ipv6_address": M("networks.global_ipv6_address", "rename (computed)", sw("EndpointSettings", "GlobalIPv6Address"), "Q1.", prev=None),
    "network_data.global_ipv6_prefix_length": M("networks.global_ipv6_prefix_len", "rename (computed)", sw("EndpointSettings", "GlobalIPv6PrefixLen"), "Q1.", prev=None),
    "network_data.ip_address": M("networks.ip_address", "rename (computed)", sw("EndpointSettings", "IPAddress"), "Q1.", prev=None),
    "network_data.ip_prefix_length": M("networks.ip_prefix_len", "rename (computed)", sw("EndpointSettings", "IPPrefixLen"), "Q1.", prev=None),
    "network_data.ipv6_gateway": M("networks.ipv6_gateway", "rename (computed)", sw("EndpointSettings", "IPv6Gateway"), "Q1.", prev=None),
    "network_data.mac_address": M("networks.mac_address", "rename (computed)", sw("EndpointSettings", "MacAddress"), "Q1.", prev=None),
})
for kind, fld in (("read_bps", "ReadBps"), ("read_iops", "ReadIOps"), ("write_bps", "WriteBps"), ("write_iops", "WriteIOps")):
    KW[C]["device_" + kind] = GAP("blkio_device_" + kind, sw("Resources", "BlkioDevice" + fld))
    KW[C]["device_" + kind + ".path"] = GAP("blkio_device_" + kind + ".path", sw("ThrottleDevice", "Path"))
    KW[C]["device_" + kind + ".rate"] = GAP("blkio_device_" + kind + ".rate", sw("ThrottleDevice", "Rate"))
KW[C]["device_requests"] = GAP("device_requests", sw("Resources", "DeviceRequests"), "gpus covers the NVIDIA case today.")
for a, f in (("capabilities", "Capabilities"), ("count", "Count"), ("device_ids", "DeviceIDs"), ("driver", "Driver"), ("options", "Options")):
    KW[C]["device_requests." + a] = GAP("device_requests." + a, sw("DeviceRequest", f))

# docker_service: kreuzwerker nests task_spec.container_spec; ours is flattened (ContainerSpec fields top-level).
S = "docker_service"
UP_MODE = cs("deploy mode") + "; " + sw("ServiceSpec", "Mode")
TS = "task_spec."
CSP = "task_spec.container_spec."
KW[S] = dict(labels_map("labels", sw("ServiceSpec", "Labels")), **mount_kw(CSP, True), **{
    "auth": D("none", "dropped: registry credentials come from the provider registry_auth blocks."),
    "auth.server_address": D("none", "dropped: provider registry_auth."),
    "auth.username": D("none", "dropped: provider registry_auth."),
    "auth.password": D("none", "dropped: provider registry_auth."),
    "endpoint_spec.ports.name": GAP("endpoint_spec.ports.name", sw("EndpointPortConfig", "Name")),
    "mode": M("mode", "block {global, replicated{replicas}} -> string \"replicated\" | \"global\" plus top-level replicas",
              UP_MODE, "Flattened mode."),
    "mode.global": M("mode", "bool true -> mode = \"global\"", UP_MODE, "Flattened mode."),
    "mode.replicated": M("mode", "block present -> mode = \"replicated\"", UP_MODE, "Flattened mode."),
    "mode.replicated.replicas": M("replicas", "moved to top level", sw("ServiceSpec", "Mode.Replicated.Replicas"), "Flattened mode."),
    TS + "force_update": M("force_update", "moved to top level", sw("TaskSpec", "ForceUpdate"), "Flattened task_spec."),
    TS + "runtime": GAP("runtime", sw("TaskSpec", "Runtime")),
    TS + "log_driver": M("logging", "list (max 1) block -> single block; renamed", SERVICE_UP["logging"], "compose logging (upstream name kept for docker_service)."),
    TS + "log_driver.name": M("logging.driver", "rename", SERVICE_UP["logging.driver"], "compose logging.driver."),
    TS + "log_driver.options": M("logging.options", "rename", SERVICE_UP["logging.options"], "compose logging.options."),
    TS + "networks_advanced": M("networks", "set of blocks -> map keyed by network name", sw("TaskSpec", "Networks"), "Q1 shape; same as our networks_advanced.", prev="networks_advanced"),
    TS + "networks_advanced.name": M("networks", "block attribute -> map key", sw("NetworkAttachmentConfig", "Target"), "Map key.", prev="networks_advanced.name"),
    TS + "networks_advanced.aliases": M("networks.aliases", "rename (map value)", sw("NetworkAttachmentConfig", "Aliases"), "Q1 shape.", prev="networks_advanced.aliases"),
    TS + "networks_advanced.driver_opts": GAP("networks.driver_opts", sw("NetworkAttachmentConfig", "DriverOpts")),
    TS + "networks_advanced.id": D(sw("NetworkAttachmentConfig", "Target"), "dropped: the map key (network name or ID) is the target."),
    TS + "placement.prefs": M("placement.preferences", "rename (set(string) -> list(string))", sw("TaskSpec", "Placement.Preferences"), "Upstream Preferences."),
    TS + "placement.platforms": GAP("placement.platforms", sw("TaskSpec", "Placement.Platforms")),
    TS + "placement.platforms.architecture": GAP("placement.platforms.architecture", sw("Platform", "Architecture")),
    TS + "placement.platforms.os": GAP("placement.platforms.os", sw("Platform", "OS")),
    TS + "resources.reservation": M("resources.reservations", "rename (list max 1 -> single block)", sw("TaskSpec", "Resources.Reservations"), "Upstream Reservations."),
    TS + "resources.reservation.memory_bytes": M("resources.reservations.memory_bytes", "rename (parent)", sw("ResourceObject", "MemoryBytes"), "Parent renamed."),
    TS + "resources.reservation.nano_cpus": M("resources.reservations.nano_cpus", "rename (parent)", sw("ResourceObject", "NanoCPUs"), "Parent renamed."),
    TS + "resources.reservation.generic_resources": GAP("resources.reservations.generic_resources", sw("ResourceObject", "GenericResources")),
    TS + "resources.reservation.generic_resources.discrete_resources_spec": GAP("resources.reservations.generic_resources.discrete_resource_spec", sw("GenericResources", "DiscreteResourceSpec")),
    TS + "resources.reservation.generic_resources.named_resources_spec": GAP("resources.reservations.generic_resources.named_resource_spec", sw("GenericResources", "NamedResourceSpec")),
    CSP + "dns_config": M("dns_config", "list (max 1) block -> single block", sw("TaskSpec", "ContainerSpec.DNSConfig"), "Same upstream name."),
    CSP + "dns_config.nameservers": M("dns_config.nameservers", "identity (flattened path)", sw("TaskSpec", "ContainerSpec.DNSConfig.Nameservers"), "Same upstream name."),
    CSP + "dns_config.options": M("dns_config.options", "identity (flattened path)", sw("TaskSpec", "ContainerSpec.DNSConfig.Options"), "Same upstream name."),
    CSP + "dns_config.search": M("dns_config.search", "identity (flattened path)", sw("TaskSpec", "ContainerSpec.DNSConfig.Search"), "Same upstream name."),
    CSP + "groups": GAP("groups", CSPEC("Groups")),
    CSP + "init": GAP("init", CSPEC("Init")),
    CSP + "isolation": GAP("isolation", CSPEC("Isolation")),
    CSP + "read_only": GAP("read_only", CSPEC("ReadOnly")),
    CSP + "stop_signal": GAP("stop_signal", CSPEC("StopSignal")),
    CSP + "sysctl": M("sysctls", "rename (flattened path)", CSPEC("Sysctls"), "Upstream Sysctls."),
    CSP + "hosts": M("hosts", "set of blocks {host, ip} -> list(string) \"<ip> <host>\"", CSPEC("Hosts"), "Upstream Hosts string form."),
    CSP + "hosts.host": M("hosts", "block attribute -> hostname part", CSPEC("Hosts"), "Upstream Hosts string form."),
    CSP + "hosts.ip": M("hosts", "block attribute -> IP part", CSPEC("Hosts"), "Upstream Hosts string form."),
    CSP + "labels": M("container_labels", BLOCKS_TO_MAP + " (flattened path)", SERVICE_UP["container_labels"], "ContainerSpec.Labels, prefixed."),
    CSP + "labels.label": M("container_labels", "block attribute -> map key", SERVICE_UP["container_labels"], "Map key."),
    CSP + "labels.value": M("container_labels", "block attribute -> map value", SERVICE_UP["container_labels"], "Map value."),
    CSP + "privileges": GAP("privileges", CSPEC("Privileges")),
    CSP + "privileges.credential_spec": GAP("privileges.credential_spec", CSPEC("Privileges.CredentialSpec")),
    CSP + "privileges.credential_spec.file": GAP("privileges.credential_spec.file", CSPEC("Privileges.CredentialSpec.File")),
    CSP + "privileges.credential_spec.registry": GAP("privileges.credential_spec.registry", CSPEC("Privileges.CredentialSpec.Registry")),
    CSP + "privileges.se_linux_context": GAP("privileges.selinux_context", CSPEC("Privileges.SELinuxContext"), "Acronym run SELinux stays whole."),
})
KW[S][TS[:-1]] = D("swagger #/definitions/TaskSpec", "flattened: task_spec fields are top-level attributes of docker_service (no task_spec block).")
KW[S][CSP[:-1]] = D(sw("TaskSpec", "ContainerSpec"), "flattened: container_spec fields are top-level attributes of docker_service.")
for a, f in (("disable", "Disable"), ("level", "Level"), ("role", "Role"), ("type", "Type"), ("user", "User")):
    KW[S][CSP + "privileges.se_linux_context." + a] = GAP("privileges.selinux_context." + a, CSPEC("Privileges.SELinuxContext." + f))
for ref in ("configs", "secrets"):
    for a, our in (("file_gid", "gid"), ("file_mode", "mode"), ("file_uid", "uid")):
        KW[S][CSP + ref + "." + a] = M(ref + "." + our, "rename (flattened path)", SERVICE_UP[ref + "." + our], "Leaf name of File.%s." % our.upper())

# docker_image / docker_registry_image build blocks.
I = "docker_image"
BUILD_GAPS = {
    "additional_contexts": ("build.additional_contexts", cs("build additional_contexts")),
    "cache_to": ("build.cache_to", cli("buildx build", "--cache-to")),
    "cgroup_parent": ("build.cgroup_parent", cs("build cgroup_parent")),
    "cpu_period": ("build.cpu_period", "swagger ImageBuild query cpuperiod"),
    "cpu_quota": ("build.cpu_quota", "swagger ImageBuild query cpuquota"),
    "cpu_set_cpus": ("build.cpuset_cpus", "swagger ImageBuild query cpusetcpus"),
    "cpu_set_mems": ("build.cpuset_mems", "Resources CpusetMems (no ImageBuild query parameter in API 1.56)"),
    "cpu_shares": ("build.cpu_shares", "swagger ImageBuild query cpushares"),
    "extra_hosts": ("build.extra_hosts", cs("build extra_hosts")),
    "force_remove": ("build.force_rm", "swagger ImageBuild query forcerm"),
    "isolation": ("build.isolation", cs("build isolation")),
    "memory": ("build.memory", "swagger ImageBuild query memory"),
    "memory_swap": ("build.memswap", "swagger ImageBuild query memswap"),
    "network_mode": ("build.network", cs("build network")),
    "provenance": ("build.provenance", cli("buildx build", "--provenance")),
    "remote_context": ("build.remote", "swagger ImageBuild query remote"),
    "sbom": ("build.sbom", cli("buildx build", "--sbom")),
    "shm_size": ("build.shm_size", cs("build shm_size")),
    "squash": ("build.squash", "swagger ImageBuild query squash"),
    "tag": ("build.tags", cs("build tags")),
    "ulimit": ("build.ulimits", cs("build ulimits")),
    "ulimit.hard": ("build.ulimits.hard", cs("build ulimits")),
    "ulimit.name": ("build.ulimits (map key)", cs("build ulimits")),
    "ulimit.soft": ("build.ulimits.soft", cs("build ulimits")),
}
BUILD_DROPS = {
    "auth_config": "dropped: registry credentials come from the provider registry_auth blocks.",
    "build_id": "dropped: build cancellation by ID is not exposed.",
    "build_log_file": "dropped: the build log is the build_log raw output (Q33), not a file.",
    "remove": "dropped: intermediate containers are always removed (ImageBuild rm=true).",
    "security_opt": "dropped: ImageBuild has no security options in API 1.56 (BuildKit entitlements are separate).",
    "session_id": "dropped: BuildKit session is managed by the provider.",
    "suppress_output": "dropped: the build log is always captured (build_log).",
    "use_legacy_builder": "dropped: the backend is chosen by build.builder.",
    "version": "dropped: the backend is chosen by build.builder (ImageBuild query version).",
}
for a in ("host_name", "auth", "email", "identity_token", "password", "registry_token", "server_address", "user_name"):
    BUILD_DROPS["auth_config." + a] = BUILD_DROPS["auth_config"]

KW[I] = {
    "id": M("id", "value change: kreuzwerker id = image ID + name; ours = image ID", sw("ImageInspect", "Id"), "Q46.", prev="id"),
    "image_id": M("id", "rename; image_id removed", sw("ImageInspect", "Id"), "Q46: id is the image ID.", prev="image_id"),
    "pull_triggers": M("triggers", "set(string) -> map(string) (any keys); config-only, no state to migrate", OWN, "One trigger map."),
    "timeouts": TIMEOUTS, "timeouts.create": TIMEOUTS, "timeouts.delete": TIMEOUTS, "timeouts.update": TIMEOUTS,
    "build": M("build", "set (max 1) of blocks -> single block", BUILD_UP["build"], "Same block."),
    "build.build_args": M("build.args", "rename", cs("build args"), "compose build.args."),
    "build.label": M("build.labels", "rename (deprecated kreuzwerker duplicate of labels)", cs("build labels"), "compose build.labels."),
    "build.pull_parent": M("build.pull", "rename", cs("build pull") + "; swagger ImageBuild query pull", "compose build.pull."),
    "build.secrets": M("build.secrets", "list of blocks (identity)", BUILD_UP["build.secrets"], "Same block."),
}
for a, (name, up) in BUILD_GAPS.items():
    KW[I]["build." + a] = GAP(name, up)
for a, why in BUILD_DROPS.items():
    KW[I]["build." + a] = D("none", why)

R = "docker_registry_image"
KW[R] = {
    "id": M("id", "value change: kreuzwerker id = digest; ours = pushed reference (name)", UP[R]["id"], "Identity is the reference."),
    "auth_config": D("none", "dropped: registry credentials come from the provider registry_auth blocks."),
    "auth_config.address": D("none", "dropped: provider registry_auth."),
    "auth_config.username": D("none", "dropped: provider registry_auth."),
    "auth_config.password": D("none", "dropped: provider registry_auth."),
    "timeouts": TIMEOUTS, "timeouts.create": TIMEOUTS, "timeouts.delete": TIMEOUTS, "timeouts.update": TIMEOUTS,
}
REG_BUILD_DROP = D("none", "dropped: docker_registry_image does not build; build with docker_image, then push with docker_registry_image.")

B = "docker_buildx_builder"
KW[B] = {
    "append": GAP("append", cli("buildx create", "--append")),
    "node": GAP("node", cli("buildx create", "--node")),
    "platform": GAP("platform", cli("buildx create", "--platform"), "Our platforms is computed from the node."),
    "use": D(cli("buildx create", "--use"), "dropped: no current-builder state; builds name their builder."),
    "driver_options": D(cli("buildx create", "--driver-opt"), "dropped: docker-container driver options are the docker_container block; other drivers not provided."),
    "endpoint": M("endpoint", "optional input -> computed", UP[B]["endpoint"], "Our endpoint is the computed BuildKit address."),
    "docker_container": M("docker_container", "list (max 1) block -> single block", UP[B]["docker_container"], "Same block."),
    "docker_container.memory": M("docker_container.memory", "string -> number (bytes)", UP[B]["docker_container.memory"], "Same option."),
}
for a, opt in (("cgroup_parent", "cgroup-parent"), ("cpu_period", "cpu-period"), ("cpu_quota", "cpu-quota"),
               ("cpu_shares", "cpu-shares"), ("cpuset_cpus", "cpuset-cpus"), ("cpuset_mems", "cpuset-mems"),
               ("default_load", "default-load"), ("memory_swap", "memory-swap")):
    KW[B]["docker_container." + a] = GAP("docker_container." + a, cli("buildx create", "--driver-opt " + opt))
KUBE_DROP = D("buildx kubernetes driver", "dropped: kubernetes driver not provided (the provider talks to Docker only).")
REMOTE = {"remote": "driver remote", "remote.cacert": "cacert", "remote.cert": "cert", "remote.key": "key",
          "remote.servername": "servername", "remote.default_load": "default-load"}

KW["docker_compose"] = {
    "config_paths": M("files", "rename", UP["docker_compose"]["files"], "compose CLI --file."),
    "env_files": M("env_file", "rename", cli("compose", "--env-file"), "compose CLI --env-file."),
    "project_directory": M("project_directory", "identity (our previous working_dir)", cli("compose", "--project-directory"), "CLI flag.", prev="working_dir"),
    "project_name": M("name", "rename", cs("top-level name"), "Q41."),
    "remove_orphans": GAP("remove_orphans", cli("compose up", "--remove-orphans")),
    "id": M("id", "identity; id = name", cs("top-level name"), "Q41: id = name."),
}
KW["docker_config"] = dict(labels_map("labels", sw("ConfigSpec", "Labels")), **{
    "data_raw": M("data", "plain text -> base64 (base64encode)", sw("ConfigSpec", "Data"), "One data input, base64 like the API."),
})
KW["docker_secret"] = labels_map("labels", sw("SecretSpec", "Labels"))
KW["docker_network"] = dict(labels_map("labels", sw("NetworkCreateRequest", "Labels")), **{
    "ipam_options": GAP("ipam_options", sw("IPAM", "Options") + "; " + cli("network create", "--ipam-opt")),
})
KW["docker_volume"] = labels_map("labels", sw("VolumeCreateOptions", "Labels"))
KW["docker_volume"]["cluster"] = GAP("cluster_volume_spec", sw("VolumeCreateOptions", "ClusterVolumeSpec"), "Cluster (CSI) volumes are not provided.")
for a in ("availability", "group", "limit_bytes", "required_bytes", "scope", "sharing", "topology_preferred", "topology_required", "type", "id"):
    KW["docker_volume"]["cluster." + a] = GAP("cluster_volume_spec.*", sw("ClusterVolumeSpec"), "Cluster (CSI) volumes are not provided.")
KW["docker_tag"] = {
    "tag_triggers": GAP("triggers", OWN),
    "id": M("id", "value change: kreuzwerker id = <source_image>.<target_image>; ours = <source_image_id>.<target_image>", OWN, "Same role."),
}
KW["data.docker_containers"] = {
    "id": D("none", "dropped: the data source has no id attribute."),
    "containers.created": M("containers.created", "number (unix seconds) -> string (RFC 3339)", sw("ContainerSummary", "Created"), "Readable timestamp."),
}
KW["data.docker_logs"] = {
    "name": M("container", "rename (ID or name)", "swagger ContainerLogs path id", "Q6: input container."),
    "logs_list_string_enabled": D("none", "dropped: log_entries is always populated (Q6 logs contract)."),
    "discard_headers": D("none", "dropped: stream headers are always demultiplexed into log_entries.stream."),
    "follow": D("swagger ContainerLogs query follow", "dropped: a data source cannot follow a stream."),
}
KW["data.docker_network"] = {
    "containers": GAP("containers", sw("Network", "Containers")),
    "containers.container_id": GAP("containers (map key)", sw("Network", "Containers (map key)")),
    "containers.endpoint_id": GAP("containers.endpoint_id", sw("NetworkContainer", "EndpointID")),
    "containers.ipv4_address": GAP("containers.ipv4_address", sw("NetworkContainer", "IPv4Address")),
    "containers.ipv6_address": GAP("containers.ipv6_address", sw("NetworkContainer", "IPv6Address")),
    "containers.mac_address": GAP("containers.mac_address", sw("NetworkContainer", "MacAddress")),
    "containers.name": GAP("containers.name", sw("NetworkContainer", "Name")),
}
KW["data.docker_plugin"] = {
    "grant_all_permissions": D(cli("plugin install", "--grant-all-permissions"), "dropped: an install-time flag, not readable from PluginInspect."),
}
KW["data.docker_registry_image"] = {
    "insecure_skip_verify": GAP("insecure_skip_verify", OWN),
}
KW["data.docker_registry_image_manifests"] = {
    "id": D("none", "dropped: the data source has no id attribute; name identifies it."),
    "auth_config": D("none", "dropped: registry credentials come from the provider registry_auth blocks."),
    "auth_config.address": D("none", "dropped: provider registry_auth."),
    "auth_config.password": D("none", "dropped: provider registry_auth."),
    "auth_config.username": D("none", "dropped: provider registry_auth."),
    "manifests.sha256_digest": M("manifests.digest", "rename", UP["data.docker_registry_image_manifests"]["manifests.digest"], "Q45 (already digest in ours).", prev="manifests.digest"),
}
KW["data.docker_registry_image_tags"] = {
    "id": D("none", "dropped: the data source has no id attribute; repository identifies it."),
    "strict_semver": D("none", "dropped: filter tags in Terraform (e.g. regex) instead."),
}
KW["action.docker_container_export"] = {
    "output": M("output", "identity (our previous path)", cli("container export", "--output"), "CLI flag.", prev="path"),
}
KW["action.docker_exec"] = {
    "detach": GAP("detach", cli("container exec", "--detach")),
    "env_file": GAP("env_file", cli("container exec", "--env-file")),
    "tty": GAP("tty", sw("ExecConfig", "Tty")),
    "env": M("env", "list(string) \"KEY=VALUE\" -> map(string)", sw("ExecConfig", "Env"), "Map form."),
}
KW["action.docker_image_import"] = {
    "reference": M("repository", "split \"repo:tag\" into repository and tag", "swagger ImageCreate query repo / tag", "Engine parameters."),
}
KW["action.docker_image_load"] = {
    "source": M("input", "rename (our previous path)", cli("image load", "--input"), "CLI flag.", prev="path"),
    "platform": GAP("platform", "swagger ImageLoad query platform"),
    "quiet": D("swagger ImageLoad query quiet", "dropped: progress goes to action progress messages."),
}
KW["action.docker_image_save"] = {
    "output": M("output", "identity (our previous path)", cli("image save", "--output"), "CLI flag.", prev="path"),
    "platform": GAP("platform", "swagger ImageGetAll query platform"),
}
KW["action.docker_system_prune"] = {
    "all": M("all", "identity (our previous all_images)", cli("system prune", "--all"), "CLI flag.", prev="all_images"),
    "filter": M("filters", "list(string) \"k=v\" -> map(string)", "swagger ContainerPrune/ImagePrune/... query filters", "Engine parameter name."),
}


def norm_kw(kind):
    k = kind.split(",")[0].strip()
    mx = "Max: 1" in kind
    table = {"String": "string", "Number": "number", "Boolean": "bool", "List of String": "list(string)",
             "Set of String": "set(string)", "Map of String": "map(string)", "List of Object": "list(object)",
             "Set of Object": "set(object)", "Attributes List": "list(object)", "Block List": "list of blocks",
             "Block Set": "set of blocks", "Block": "single block"}
    t = table.get(k, k)
    if mx and t in ("list of blocks", "set of blocks"):
        t = t.replace("of blocks", "(max 1) block")
    return t


def norm_ours(typ):
    if typ.startswith("block: "):
        d = typ[len("block: "):].split(",")[0]
        return d
    return typ


def main():
    kw_dir = sys.argv[1]
    out_path = sys.argv[2] if len(sys.argv) > 2 else os.path.join(REPO, "reference", "_gen", "renames.json")
    kw = attrs.kreuzwerker_attrs(os.path.join(kw_dir, "docs"))
    ours_rows = attrs.our_attrs(REF)
    ours = {}
    for typ, path, ty, _status in ours_rows:
        ours.setdefault(PREV_TYPE.get(typ, typ), {})[path] = norm_ours(ty)

    # reverse of REN: our_attr -> previous
    rev = {t: {} for t in REN}
    for t, m in REN.items():
        for prev, v in m.items():
            if v[0]:
                rev[t].setdefault(v[0], prev)

    entries = []
    covered = set()
    used = set()
    errors = []
    for ktype, path, kind in kw:
        typ = KW_TYPE.get(ktype, ktype)
        prevs = ours.get(typ, {})
        ren = REN.get(typ, {})
        up = UP.get(typ, {})
        spec = KW.get(ktype, {}).get(path)
        if spec is not None:
            used.add((ktype, path))
        if spec is None and typ == "docker_service":
            # flattened kreuzwerker paths: strip task_spec.container_spec. / task_spec.
            for pre in (CSP, TS):
                if path.startswith(pre):
                    flat = path[len(pre):]
                    if flat in ren:
                        spec = M(ren[flat][0], ren[flat][1] + " (flattened path)", ren[flat][2], ren[flat][3], prev=flat)
                    elif flat in prevs:
                        spec = M(flat, "identity (flattened path)", up.get(flat, "MISSING"), "Flattened: same leaf name.", prev=flat)
                    break
        if spec is None and typ == R and path.startswith("build"):
            spec = REG_BUILD_DROP
        if spec is None and typ == B and path.startswith("kubernetes"):
            spec = KUBE_DROP
        if spec is None and typ == B and path.startswith("remote"):
            spec = D(cli("buildx create", "--driver remote --driver-opt " + REMOTE[path].split()[-1]),
                     "dropped: remote driver not provided (our endpoint is the computed address of the docker-container builder).")
        if spec is None:
            if path in ren:
                o, tr, u, why = ren[path]
                spec = M(o, tr, u, why, prev=path)
            elif path in prevs:
                if path not in up:
                    errors.append("no UP source for %s %s" % (typ, path))
                spec = M(path, "identity", up.get(path, "MISSING"), "Same name as upstream.", prev=path)
            else:
                errors.append("unmapped kreuzwerker attribute %s %s (%s)" % (ktype, path, kind))
                continue
        if spec[0] == "drop":
            e = {"type": typ, "kreuzwerker_attr": path, "previous_attr": None, "our_attr": None,
                 "transform": "dropped", "upstream": spec[1], "reason": spec[2]}
        else:
            _, our, tr, u, why, prev = spec
            if prev == "auto":
                prev = rev.get(typ, {}).get(our) or (our if our in prevs else None)
            if prev is not None and prev not in prevs:
                errors.append("previous_attr %s not in our schema for %s" % (prev, typ))
            kt = norm_kw(kind)
            ot = prevs.get(prev) if prev else None
            if tr == "identity" and our != path:
                tr = "rename"
            if ot and ot != kt and "->" not in tr:
                tr = "%s; %s -> %s" % (tr, kt, ot)
            e = {"type": typ, "kreuzwerker_attr": path, "previous_attr": prev, "our_attr": our,
                 "transform": tr, "upstream": u, "reason": why}
            if prev is not None:
                covered.add((typ, prev, our))
        entries.append(e)

    for typ, m in REN.items():
        prevs = ours.get(typ, {})
        for prev, (our, tr, u, why) in m.items():
            if prev not in prevs:
                errors.append("REN previous_attr %s missing in our schema for %s" % (prev, typ))
            if (typ, prev, our) in covered:
                continue
            entries.append({"type": typ, "kreuzwerker_attr": None, "previous_attr": prev, "our_attr": our,
                            "transform": tr, "upstream": u, "reason": why})

    for kt, m in KW.items():
        for k in m:
            if (kt, k) not in used and not k.endswith("volume_options.subpath"):
                errors.append("unused KW entry %s %s" % (kt, k))
    if errors:
        for e in errors:
            print("ERROR:", e, file=sys.stderr)
        sys.exit(1)

    entries.sort(key=lambda e: (e["type"], e["kreuzwerker_attr"] or e["previous_attr"] or "", e["previous_attr"] or "", e["our_attr"] or ""))
    try:
        commit = subprocess.run(["git", "-C", kw_dir, "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout.strip()
    except Exception:  # noqa: BLE001
        commit = "1e48613cd36acab30e9146aae6f036ea8e40a288"
    doc = {
        "about": (
            "Pass 5 rename table (Lane 0 audit), generated by reference/_gen/generators/pass5/build_renames.py from the "
            "kreuzwerker v4.6.0 docs, our genskill reference and the hand-written tables in that script; regenerate, do not "
            "hand-edit. Readers: the implementation lanes (rename our Go schema attributes where previous_attr != our_attr), "
            "the migration lane (ResourceWithMoveState from registry.terraform.io/kreuzwerker/docker, using kreuzwerker_attr -> "
            "our_attr and transform), and tools/parity (every kreuzwerker v4.6.0 resource, data source and action attribute must "
            "appear here, mapped or dropped). Type names are kind-prefixed for non-resources (data., action., ephemeral.). "
            "Fields: type = our type after Pass 5; kreuzwerker_attr = dotted path in kreuzwerker v4.6.0 (null for our own "
            "renames); previous_attr = dotted path in our schema before Pass 5 (null when we had no such attribute); "
            "our_attr = dotted path after Pass 5 (null = dropped/removed; for a map-valued attribute such as networks the "
            "map key is the attribute itself and its values are networks.<field>); transform = identity, rename, or the "
            "type/shape/value change to apply; upstream = the upstream source of our_attr (swagger = Engine API 1.56, "
            "compose-spec, docker-cli, buildx; 'contract' when there is none); reason = one line. A reason starting with "
            "'gap:' marks a kreuzwerker attribute we do not have yet and names the upstream-derived attribute to add."
        ),
        "kreuzwerker": {"version": "v4.6.0", "commit": commit, "source": "registry.terraform.io/kreuzwerker/docker"},
        "type_renames": TYPE_RENAMES,
        "renames": entries,
    }
    with open(out_path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(doc, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print("wrote %s: %d entries" % (out_path, len(entries)))


if __name__ == "__main__":
    main()
