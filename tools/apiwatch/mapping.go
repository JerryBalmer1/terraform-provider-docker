package main

import (
	"regexp"
	"strings"
)

// Target is the provider type that should cover an API change: Kind is
// resource, data, action, ephemeral, function, list, provider or skip (with
// Reason), or unmapped when no rule matches (decide and add a rule).
type Target struct {
	Kind, Type, Reason string
}

func (t Target) String() string {
	switch t.Kind {
	case "skip":
		return "skip: " + t.Reason
	case "unmapped":
		return "unmapped: decide resource / data / action / ephemeral / function / list, or skip with a reason, and add a rule to tools/apiwatch/mapping.go"
	case "provider":
		return "provider " + t.Type
	}
	return t.Kind + " " + t.Type
}

type endpointRule struct {
	method string // "" matches any
	re     *regexp.Regexp
	target Target
}

func rule(method, pattern, kind, typ, reason string) endpointRule {
	return endpointRule{method, regexp.MustCompile("^" + pattern), Target{kind, typ, reason}}
}

// endpointRules are checked in order; the first match wins. Paths use the
// spec's own placeholders ({id}, {name}).
var endpointRules = []endpointRule{
	rule("", `/containers/\{id\}/exec`, "action", "docker_exec", ""),
	rule("", `/exec/`, "action", "docker_exec", ""),
	rule("", `/containers/\{id\}/export`, "action", "docker_container_export", ""),
	rule("", `/containers/\{id\}/logs`, "data", "docker_logs", ""),
	rule("", `/containers/\{id\}/archive`, "resource", "docker_container", ""),
	rule("", `/containers/json`, "data", "docker_containers", ""),
	rule("", `/containers/prune`, "action", "docker_system_prune", ""),
	rule("", `/containers/\{id\}/(attach|wait|kill|pause|unpause|top|stats|resize|changes|checkpoints)`, "skip", "", "transient process control or live stats, not managed state"),
	rule("", `/containers`, "resource", "docker_container", ""),
	rule("", `/images/\{name\}/push`, "resource", "docker_registry_image", ""),
	rule("", `/images/\{name\}/tag`, "resource", "docker_tag", ""),
	rule("", `/images/(get|\{name\}/get)`, "action", "docker_image_save", ""),
	rule("", `/images/load`, "action", "docker_image_load", ""),
	rule("", `/images/prune`, "action", "docker_image_prune", ""),
	rule("", `/images/search`, "skip", "", "registry search is not managed state"),
	rule("", `/images/json`, "list", "docker_image", ""),
	rule("", `/images`, "resource", "docker_image", ""),
	rule("", `/build/prune`, "action", "docker_system_prune", ""),
	rule("", `/build/cancel`, "skip", "", "build cancellation is internal to docker_image builds"),
	rule("", `/build`, "resource", "docker_image", ""),
	rule("", `/session`, "resource", "docker_image", ""),
	rule("", `/grpc`, "resource", "docker_image", ""),
	rule("", `/commit`, "skip", "", "snapshotting a container is not declarative; docker_image builds from a Dockerfile"),
	rule("", `/networks/prune`, "action", "docker_system_prune", ""),
	rule("", `/networks`, "resource", "docker_network", ""),
	rule("", `/volumes/prune`, "action", "docker_system_prune", ""),
	rule("", `/volumes`, "resource", "docker_volume", ""),
	rule("", `/swarm/(join|leave)`, "resource", "docker_swarm_node", ""),
	rule("", `/swarm`, "resource", "docker_swarm_init", ""),
	rule("", `/nodes`, "resource", "docker_swarm_node", ""),
	rule("", `/services/\{id\}/logs`, "skip", "", "aggregated service logs have no provider equivalent (docker_logs reads one container)"),
	rule("", `/services`, "resource", "docker_service", ""),
	rule("", `/tasks`, "skip", "", "tasks are observed by docker_service convergence, not managed"),
	rule("", `/secrets`, "resource", "docker_secret", ""),
	rule("", `/configs`, "resource", "docker_config", ""),
	rule("", `/plugins`, "resource", "docker_plugin", ""),
	rule("", `/distribution`, "data", "docker_registry_image", ""),
	rule("", `/auth`, "provider", "registry_auth", ""),
	rule("", `/(_ping|version|info|events|system/df)`, "skip", "", "diagnostics or the provider's connection handshake"),
}

// MapEndpoint returns the provider type that should cover method + path.
func MapEndpoint(method, path string) Target {
	for _, r := range endpointRules {
		if (r.method == "" || r.method == method) && r.re.MatchString(path) {
			return r.target
		}
	}
	return Target{Kind: "unmapped"}
}

type defRule struct {
	prefixes []string
	target   Target
}

// defRules map definition names (by prefix, first match wins) to the type
// whose schema exposes their fields.
var defRules = []defRule{
	{[]string{"ContainersDiskUsage", "ImagesDiskUsage", "VolumesDiskUsage", "BuildCacheDiskUsage", "ContainerStats", "NRIInfo"}, Target{"skip", "", "daemon diagnostics (system df, stats, NRI), not managed state"}},
	{[]string{"ContainerConfig", "HostConfig", "ContainerCreate", "ContainerInspect", "ContainerState", "ContainerSummary", "Mount", "RestartPolicy", "Resources", "DeviceRequest", "DeviceMapping", "ThrottleDevice", "HealthConfig", "Health", "PortBinding", "Port", "EndpointSettings", "NetworkingConfig", "NetworkSettings", "EndpointIPAMConfig", "ContainerUpdate", "Storage", "RootFSStorage"}, Target{"resource", "docker_container", ""}},
	{[]string{"ServiceSpec", "Service", "TaskSpec", "EndpointSpec", "EndpointPortConfig", "Limit", "ResourceObject", "GenericResources"}, Target{"resource", "docker_service", ""}},
	{[]string{"Task"}, Target{"skip", "", "tasks are observed by docker_service convergence, not managed"}},
	{[]string{"SwarmSpec", "Swarm", "ClusterInfo", "JoinTokens", "TLSInfo"}, Target{"resource", "docker_swarm_init", ""}},
	{[]string{"NodeSpec", "Node", "ManagerStatus", "PeerNode"}, Target{"resource", "docker_swarm_node", ""}},
	{[]string{"Network", "IPAM", "Address", "EndpointResource", "SubnetStatus"}, Target{"resource", "docker_network", ""}},
	{[]string{"ClusterVolume", "Volume"}, Target{"resource", "docker_volume", ""}},
	{[]string{"SecretSpec", "Secret"}, Target{"resource", "docker_secret", ""}},
	{[]string{"ConfigSpec", "Config"}, Target{"resource", "docker_config", ""}},
	{[]string{"Plugin"}, Target{"resource", "docker_plugin", ""}},
	{[]string{"Image", "BuildInfo", "BuildCache", "BuildPrune", "ImageID", "ImageManifest", "ImageDelete", "OCI", "Descriptor", "Identity", "BuildIdentity", "PullIdentity", "Signature", "SignerIdentity", "KnownSignerIdentity", "AttestationStatement"}, Target{"resource", "docker_image", ""}},
	{[]string{"DistributionInspect"}, Target{"data", "docker_registry_image", ""}},
	{[]string{"AuthConfig", "AuthResponse"}, Target{"provider", "registry_auth", ""}},
	{[]string{"ExecConfig", "ExecInspect", "ExecStart", "Process"}, Target{"action", "docker_exec", ""}},
	{[]string{"SystemInfo", "SystemVersion", "EventMessage", "EventActor", "Runtime", "Commit", "IndexInfo", "RegistryServiceConfig", "PluginsInfo", "SystemDataUsage", "ContainerdInfo", "FirewallInfo", "DeviceInfo"}, Target{"skip", "", "daemon diagnostics, not managed state"}},
	{[]string{"ErrorResponse", "ErrorDetail", "ProgressDetail", "IdResponse", "ObjectVersion", "CreateImageInfo", "PushImageInfo"}, Target{"skip", "", "protocol plumbing"}},
}

// MapDefinition returns the provider type whose schema covers a definition.
func MapDefinition(name string) Target {
	for _, r := range defRules {
		for _, p := range r.prefixes {
			if strings.HasPrefix(name, p) {
				return r.target
			}
		}
	}
	return Target{Kind: "unmapped"}
}
