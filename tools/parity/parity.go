// Command parity prints which kreuzwerker/docker v4.6.0 types this provider
// has, has partially or is missing, plus the types only this provider has.
// The list is maintained by hand: update it whenever a type lands or changes
// status.
//
//	go run ./tools/parity                          # print the table
//	go run ./tools/parity -check BUILD_REPORT.md   # also exit 1 if a missing type
//	                                               # has no [BLOCKED] line naming it
//
// Source of the kreuzwerker list (tag v4.6.0, commit 1e48613c):
//
//	https://github.com/kreuzwerker/terraform-provider-docker/tree/v4.6.0/docs
//	  docs/resources, docs/data-sources, docs/actions
//	https://github.com/kreuzwerker/terraform-provider-docker/blob/v4.6.0/internal/provider/provider.go
//	  (SDKv2 ResourcesMap / DataSourcesMap)
//	https://github.com/kreuzwerker/terraform-provider-docker/blob/v4.6.0/internal/provider/framework_provider.go
//	  (framework Resources / DataSources / Actions)
//	https://registry.terraform.io/providers/kreuzwerker/docker/4.6.0/docs
//
// kreuzwerker v4.6.0 registers no ephemeral resources, list resources or
// provider functions.
package main

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
)

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

// kreuzwerker is every type kreuzwerker/docker v4.6.0 registers (12
// resources, 8 data sources, 6 actions), with this provider's status as of
// the Pass 3 plan.
var kreuzwerker = []Entry{
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

// extras are the types only this provider has.
var extras = []Extra{
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

// Print writes the parity table and a summary.
func Print(w io.Writer, entries []Entry, extra []Extra) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tTYPE (kreuzwerker/docker v4.6.0)\tSTATUS\tNOTE")
	counts := map[Status]int{}
	for _, e := range entries {
		counts[e.Status]++
		note := e.Note
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Kind, e.Name, e.Status, note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%d kreuzwerker types: %d have, %d partial, %d missing\n",
		len(entries), counts[Have], counts[Partial], counts[Missing])
	if len(extra) == 0 {
		return nil
	}
	fmt.Fprintf(w, "\nOnly in this provider (%d):\n\n", len(extra))
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tTYPE\tNOTE")
	for _, x := range extra {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", x.Kind, x.Name, x.Note)
	}
	return tw.Flush()
}

// Unblocked returns the missing entries that no line of report both marks
// [BLOCKED] and names (as a whole word, so docker_image does not match
// docker_image_save).
func Unblocked(entries []Entry, report string) []Entry {
	var blocked []string
	for _, l := range strings.Split(report, "\n") {
		if strings.Contains(l, "[BLOCKED]") {
			blocked = append(blocked, l)
		}
	}
	var out []Entry
	for _, e := range entries {
		if e.Status != Missing {
			continue
		}
		re := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(e.Name) + `([^A-Za-z0-9_]|$)`)
		found := false
		for _, l := range blocked {
			if re.MatchString(l) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, e)
		}
	}
	return out
}

// Validate checks the static lists: known kinds and statuses, docker_
// prefixes, no duplicates, sorted within a kind, and no extra that is also a
// kreuzwerker type.
func Validate(entries []Entry, extra []Extra) []string {
	var errs []string
	kinds := map[Kind]bool{Resource: true, DataSource: true, Action: true, Ephemeral: true, List: true, Function: true}
	seen := map[string]bool{}
	byKind := map[Kind][]string{}
	for _, e := range entries {
		key := string(e.Kind) + " " + e.Name
		switch {
		case !kinds[e.Kind]:
			errs = append(errs, key+": unknown kind")
		case e.Status != Have && e.Status != Partial && e.Status != Missing:
			errs = append(errs, fmt.Sprintf("%s: unknown status %q", key, e.Status))
		case e.Kind != Function && !strings.HasPrefix(e.Name, "docker_"):
			errs = append(errs, key+": name lacks docker_ prefix")
		case e.Status == Partial && e.Note == "":
			errs = append(errs, key+": partial needs a note saying what is missing")
		}
		if seen[key] {
			errs = append(errs, key+": duplicate")
		}
		seen[key] = true
		byKind[e.Kind] = append(byKind[e.Kind], e.Name)
	}
	for k, names := range byKind {
		if !sort.StringsAreSorted(names) {
			errs = append(errs, string(k)+": entries not sorted by name")
		}
	}
	for _, x := range extra {
		key := string(x.Kind) + " " + x.Name
		if !kinds[x.Kind] {
			errs = append(errs, key+": unknown kind")
		}
		if seen[key] {
			errs = append(errs, key+": listed as an extra but is a kreuzwerker type (or a duplicate)")
		}
		seen[key] = true
	}
	sort.Strings(errs)
	return errs
}
