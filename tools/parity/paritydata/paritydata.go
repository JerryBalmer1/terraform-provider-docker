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
	{DataSource, "docker_logs", Have, "renamed data docker_container_logs in Pass 5 (Q6)"},
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
	{Resource, "docker_node", "Pass 5: Labels, availability and role of an already-joined swarm node (NodeUpdate), without joining or leaving (Q4)"},
	{Resource, "docker_registry_manifest_list", "Pass 5: Build a manifest list / OCI index from existing per-platform images in a registry, annotate entries..."},
	{DataSource, "docker_buildx_builder", "Pass 5: docker buildx inspect: driver, nodes, endpoint, status, BuildKit version, platforms, GC policy of a builder"},
	{DataSource, "docker_buildx_builders", "Pass 5: docker buildx ls: builders and their nodes, status, platforms"},
	{DataSource, "docker_buildx_disk_usage", "Pass 5: docker buildx du: build cache records and sizes on a builder"},
	{DataSource, "docker_buildx_history", "Pass 5: docker buildx history inspect: details of one build record (args, platforms, outputs, materials, errors)"},
	{DataSource, "docker_buildx_history_records", "Pass 5: docker buildx history ls: build records on a builder (ref, name, status, duration, cache ratio)"},
	{DataSource, "docker_compose_config", "Pass 5: Parse, interpolate, merge and render the compose model in canonical form (no Engine call)"},
	{DataSource, "docker_compose_containers", "Pass 5: List containers of a project. Engine: GET /containers/json with com.docker.compose.project label filter"},
	{DataSource, "docker_compose_images", "Pass 5: List images used by project containers. Engine: GET /containers/json + /images/{id}/json"},
	{DataSource, "docker_compose_logs", "Pass 5: Read logs of service containers. Engine: GET /containers/{id}/logs"},
	{DataSource, "docker_compose_port", "Pass 5: Public port for a service port binding. Engine: GET /containers/{id}/json NetworkSettings.Ports"},
	{DataSource, "docker_compose_projects", "Pass 5: List compose projects on the host. Engine: GET /containers/json grouped by project label"},
	{DataSource, "docker_compose_top", "Pass 5: Processes running in service containers. Engine: GET /containers/{id}/top"},
	{DataSource, "docker_compose_volumes", "Pass 5: List volumes of a project. Engine: GET /volumes with project label filter"},
	{DataSource, "docker_config", "Pass 5: Inspect one swarm config"},
	{DataSource, "docker_container", "Pass 5: Look up one container (inspect) that Terraform does not manage"},
	{DataSource, "docker_container_changes", "Pass 5: Filesystem changes of a container against its image"},
	{DataSource, "docker_container_file", "Pass 5: Read a file/directory stat and content out of a container (HEAD + GET archive)"},
	{DataSource, "docker_container_logs", "Pass 5: kreuzwerker's data docker_logs, renamed (Q6); container logs with the logs contract"},
	{DataSource, "docker_container_stats", "Pass 5: One-shot resource usage sample (stream=false, one-shot=true)"},
	{DataSource, "docker_container_top", "Pass 5: Processes running in a container"},
	{DataSource, "docker_dockerfile", "Reads, parses and renders one Dockerfile from content, a local path, an https url, an image history (approximate) or ordered stage/instruction blocks"},
	{DataSource, "docker_image_attestations", "Pass 5: In-toto attestations (SBOM, provenance) stored with a local image"},
	{DataSource, "docker_image_history", "Pass 5: Layer history of a local image"},
	{DataSource, "docker_image_search", "Pass 5: Search Docker Hub through the daemon"},
	{DataSource, "docker_images", "Pass 5: List local images with filters"},
	{DataSource, "docker_info", "Pass 5: Daemon and host information (docker info)"},
	{DataSource, "docker_networks", "Pass 5: List networks with filters"},
	{DataSource, "docker_plugins", "Pass 5: List installed plugins"},
	{DataSource, "docker_secret", "Pass 5: Inspect one secret's metadata"},
	{DataSource, "docker_service", "Pass 5: Inspect one service"},
	{DataSource, "docker_service_logs", "Pass 5: Snapshot of a service's aggregated task logs"},
	{DataSource, "docker_stack", "Pass 5: docker stack services: the services of one stack (GET /services filtered by namespace label), with replica..."},
	{DataSource, "docker_stacks", "Pass 5: docker stack ls: stacks derived by grouping services by the com.docker.stack.namespace label (name,..."},
	{DataSource, "docker_swarm", "Pass 5: Inspect the swarm (ID, spec, join tokens, CA, data path port)"},
	{DataSource, "docker_system_df", "Pass 5: Disk usage by images, containers, volumes and build cache"},
	{DataSource, "docker_task", "Pass 5: Inspect one swarm task"},
	{DataSource, "docker_task_logs", "Pass 5: Snapshot of one task's logs"},
	{DataSource, "docker_tasks", "Pass 5: List swarm tasks (filter by service, node, desired-state)"},
	{DataSource, "docker_version", "Pass 5: Engine version, API version and components"},
	{DataSource, "docker_volumes", "Pass 5: List volumes with filters"},
	{Action, "docker_builder_prune", "Pass 5: Remove build cache"},
	{Action, "docker_buildx_bake", "Pass 5: docker buildx bake / docker bake: build one or more targets from a bake file (HCL/JSON/compose) in one..."},
	{Action, "docker_compose_build", "Pass 5: Build every service with a `build:` section. Engine: POST /build or BuildKit session"},
	{Action, "docker_compose_commit", "Pass 5: Create an image from a service container. Engine: POST /commit"},
	{Action, "docker_compose_cp", "Pass 5: Copy files between a service container and the local filesystem. Engine: PUT/GET /containers/{id}/archive"},
	{Action, "docker_compose_exec", "Pass 5: Exec a command in a service container. Engine: POST /containers/{id}/exec + /exec/{id}/start"},
	{Action, "docker_compose_export", "Pass 5: Export a service container filesystem as a tar. Engine: GET /containers/{id}/export"},
	{Action, "docker_compose_kill", "Pass 5: Send a signal to service containers, selected by project/service labels. Engine: GET /containers/json..."},
	{Action, "docker_compose_pause", "Pass 5: Pause service containers, selected by project/service labels. Engine: GET /containers/json (label filter)..."},
	{Action, "docker_compose_publish", "Pass 5: Publish the compose application as an OCI artifact to a registry"},
	{Action, "docker_compose_pull", "Pass 5: Pull all service images. Engine: POST /images/create"},
	{Action, "docker_compose_push", "Pass 5: Push all service images. Engine: POST /images/{name}/push"},
	{Action, "docker_compose_restart", "Pass 5: Restart service containers, selected by project/service labels. Engine: GET /containers/json (label..."},
	{Action, "docker_compose_run", "Pass 5: Run a one-off container for a service or job. Engine: container create/start/wait/delete"},
	{Action, "docker_compose_scale", "Pass 5: Set the replica count of services. Engine: container create/start/stop/delete (local)"},
	{Action, "docker_compose_start", "Pass 5: Start existing service containers, selected by project/service labels. Engine: GET /containers/json (label..."},
	{Action, "docker_compose_stop", "Pass 5: Stop service containers, selected by project/service labels. Engine: GET /containers/json (label filter) +..."},
	{Action, "docker_compose_unpause", "Pass 5: Unpause service containers, selected by project/service labels. Engine: GET /containers/json (label..."},
	{Action, "docker_compose_wait", "Pass 5: Block until service containers stop. Engine: POST /containers/{id}/wait"},
	{Action, "docker_container_cp", "Pass 5: Copy files into an existing (running) container at any time, not only before first start"},
	{Action, "docker_container_kill", "Pass 5: Send a signal (default SIGKILL) to a container"},
	{Action, "docker_container_pause", "Pass 5: Freeze all processes of a container"},
	{Action, "docker_container_prune", "Pass 5: Remove stopped containers"},
	{Action, "docker_container_restart", "Pass 5: Restart a container"},
	{Action, "docker_container_run", "Pass 5.3: Run a one-shot container to completion and remove it (docker run --rm), capturing exit code, stdout and stderr"},
	{Action, "docker_container_start", "Pass 5: Start a stopped container (docker_container with state = \"created\" never starts it)"},
	{Action, "docker_container_stop", "Pass 5: Stop a running container"},
	{Action, "docker_container_unpause", "Pass 5: Resume a paused container"},
	{Action, "docker_container_wait", "Pass 5: Block until a container reaches a condition (not-running/next-exit/removed) and report its exit code"},
	{Action, "docker_image_commit", "Pass 5: Create an image from a container's changes"},
	{Action, "docker_network_prune", "Pass 5: Remove unused networks"},
	{Action, "docker_node_demote", "Pass 5: Demote a manager to worker"},
	{Action, "docker_node_promote", "Pass 5: Promote a worker to manager"},
	{Action, "docker_plugin_create", "Pass 5: Create a plugin from a rootfs + config.json tarball"},
	{Action, "docker_plugin_push", "Pass 5: Push a locally created plugin to a registry"},
	{Action, "docker_swarm_rotate_ca", "Pass 5: Rotate the swarm root CA (or install an external one)"},
	{Action, "docker_swarm_rotate_unlock_key", "Pass 5: Rotate the manager unlock key"},
	{Action, "docker_swarm_unlock", "Pass 5: Unlock a locked (autolock) manager after a daemon restart"},
	{Action, "docker_volume_prune", "Pass 5: Remove unused volumes"},
	{Ephemeral, "docker_registry_login", "Pass 5: Validate registry credentials through the daemon and return an identity token when the registry issues one"},
}
