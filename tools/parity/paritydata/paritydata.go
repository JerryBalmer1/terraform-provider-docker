// Package paritydata is the hand-maintained list of kreuzwerker/docker v4.6.0
// types and this provider's coverage of each, plus the types only this
// provider has. tools/parity prints and checks it; tools/genskill uses it for
// the docker-cli-map skill. Update it whenever a type lands or changes status.
//
// Source of the kreuzwerker list (tag v4.6.0, commit 1e48613c):
//
//	https://github.com/kreuzwerker/terraform-provider-docker/tree/v4.6.0/docs
//	https://github.com/kreuzwerker/terraform-provider-docker/blob/v4.6.0/internal/provider/provider.go
//	https://github.com/kreuzwerker/terraform-provider-docker/blob/v4.6.0/internal/provider/framework_provider.go
//	https://registry.terraform.io/providers/kreuzwerker/docker/4.6.0/docs
//
// kreuzwerker v4.6.0 registers no ephemeral resources, list resources or
// provider functions.
package paritydata

// Status of a kreuzwerker type in this provider.
type Status string

const (
	Have    Status = "have"
	Partial Status = "partial"
	Missing Status = "missing"
)

// Kind of a Terraform type.
type Kind string

const (
	Resource   Kind = "resource"
	DataSource Kind = "data source"
	Action     Kind = "action"
	Ephemeral  Kind = "ephemeral"
	List       Kind = "list"
	Function   Kind = "function"
)

// Entry is one kreuzwerker v4.6.0 type and how this provider covers it.
type Entry struct {
	Kind   Kind
	Name   string
	Status Status
	Note   string
}

// Extra is a type this provider has that kreuzwerker v4.6.0 does not.
type Extra struct {
	Kind Kind
	Name string
	Note string
}

// Kreuzwerker is every type kreuzwerker/docker v4.6.0 registers (12
// resources, 8 data sources, 6 actions), with this provider's status as of
// the Pass 3 plan.
var Kreuzwerker = []Entry{
	// Resources (docs/resources at v4.6.0).
	{Resource, "docker_buildx_builder", Partial, "docker-container driver only (no kubernetes/remote drivers)"},
	{Resource, "docker_compose", Partial, "parsed in-process with compose-go into Engine API objects (local or swarm mode), no compose plugin; no build/develop/watch/hooks; see also tools/compose2stack"},
	{Resource, "docker_config", Have, ""},
	{Resource, "docker_container", Have, ""},
	{Resource, "docker_image", Have, ""},
	{Resource, "docker_network", Have, ""},
	{Resource, "docker_plugin", Have, ""},
	{Resource, "docker_registry_image", Have, ""},
	{Resource, "docker_secret", Have, ""},
	{Resource, "docker_service", Have, ""},
	{Resource, "docker_tag", Have, ""},
	{Resource, "docker_volume", Have, ""},

	// Data sources (docs/data-sources at v4.6.0).
	{DataSource, "docker_containers", Have, ""},
	{DataSource, "docker_image", Have, ""},
	{DataSource, "docker_logs", Have, ""},
	{DataSource, "docker_network", Have, ""},
	{DataSource, "docker_plugin", Have, ""},
	{DataSource, "docker_registry_image", Have, ""},
	{DataSource, "docker_registry_image_manifests", Have, ""},
	{DataSource, "docker_registry_image_tags", Have, ""},

	// Actions (docs/actions at v4.6.0).
	{Action, "docker_container_export", Have, ""},
	{Action, "docker_exec", Have, ""},
	{Action, "docker_image_import", Have, ""},
	{Action, "docker_image_load", Have, ""},
	{Action, "docker_image_save", Have, ""},
	{Action, "docker_system_prune", Have, ""},
}

// Extras are the types only this provider has.
var Extras = []Extra{
	{Resource, "docker_stack", "Swarm stack from API primitives"},
	{Resource, "docker_swarm_init", "initialise a Swarm on the provider's daemon"},
	{Resource, "docker_swarm_node", "join another host as worker or manager"},
	{DataSource, "docker_node", "one Swarm node"},
	{DataSource, "docker_nodes", "all Swarm nodes"},
	{DataSource, "docker_swarm_tokens", "join tokens of the Swarm"},
	{DataSource, "docker_volume", "look up a volume"},
	{DataSource, "docker_services", "list Swarm services"},
	{DataSource, "docker_secrets", "list Swarm secrets"},
	{DataSource, "docker_configs", "list Swarm configs"},
	{Action, "docker_image_prune", "prune images only"},
	{Action, "docker_swarm_rotate_tokens", "rotate worker/manager join tokens"},
	{Action, "docker_node_drain", "drain a node and wait for its tasks to leave"},
	{Action, "docker_node_activate", "set a node back to active"},
	{Action, "docker_service_scale", "scale a Swarm service without changing configuration"},
	{Action, "docker_service_force_update", "force a rolling restart and wait for converge"},
	{Action, "docker_service_rollback", "roll a service back to its previous spec"},
	{Ephemeral, "docker_swarm_tokens", "join tokens, never in state"},
	{Ephemeral, "docker_registry_token", "registry bearer token"},
	{Ephemeral, "docker_exec_output", "stdout of a command as an ephemeral value"},
	{Function, "parse_image_ref", "split an image reference"},
	{Function, "image_ref", "build an image reference"},
	{Function, "compose_env", "map to sorted K=V list"},
	{Function, "port_spec", "parse a -p port spec"},
	{List, "docker_container", "terraform query"},
	{List, "docker_image", "terraform query"},
	{List, "docker_network", "terraform query"},
	{List, "docker_volume", "terraform query"},
	{List, "docker_swarm_node", "terraform query"},
	{List, "docker_service", "terraform query"},
	{List, "docker_secret", "terraform query"},
	{List, "docker_config", "terraform query"},
}
