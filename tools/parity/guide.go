package main

// The migration guide template (templates/guides/migrating-from-kreuzwerker.md.tmpl)
// is generated from reference/_gen/renames.json: the prose below plus one
// table set per kreuzwerker type. Regenerate it after renames.json changes:
//
//	go run ./tools/parity -guide templates/guides/migrating-from-kreuzwerker.md.tmpl
//	go run ./tools/parity -guide-check templates/guides/migrating-from-kreuzwerker.md.tmpl

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
)

// guideMoveSchema is the kreuzwerker v4.6.0 SDKv2 schema version per
// resource type; a state at a lower version must be refreshed by kreuzwerker
// first (internal/provider/move_*.go reject it).
var guideMoveSchema = map[string]int{
	"docker_container": 2, "docker_service": 2, "docker_network": 1, "docker_secret": 1, "docker_volume": 1,
}

// guideMoveNotes are the per-type notes on values that change on the move.
var guideMoveNotes = map[string]string{
	"docker_container":      "`network_data` is merged into the `networks` entries that `networks_advanced` declared; `volumes` blocks become `mounts` entries (type `bind` with a `host_path`, `volume` otherwise); `memory` / `memory_swap` / `shm_size` MB become bytes; `cpus` becomes `nano_cpus`; `env` becomes a map; `upload.executable = true` becomes `files.mode = \"0744\"`.",
	"docker_service":        "`task_spec.container_spec.*` and `task_spec.*` move to top-level attributes; `mode` becomes `mode = \"replicated\" | \"global\"` plus `replicas`; `networks_advanced` becomes the `networks` map (key = `name`, or `id` when `name` is empty).",
	"docker_image":          "`id` becomes the image ID (kreuzwerker's `image_id`; kreuzwerker's `id` is image ID + name); `build.label` keys are merged into `build.labels`; `pull_triggers` is not moved (set it as `triggers` in configuration).",
	"docker_registry_image": "`id` becomes the pushed reference (`name`; kreuzwerker's `id` is the digest); `image_digest_ref` is set to `<repository>@<digest>`.",
	"docker_tag":            "`id` becomes `<source_image_id>.<target_image>` (kreuzwerker: `<source_image>.<target_image>`).",
	"docker_compose":        "`id` and `name` are the compose project name.",
	"docker_config":         "`data_raw` (plain text) is base64-encoded into `data`.",
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

func code(s string) string { return "`" + s + "`" }

// GuideTemplate renders the migration guide template from renames.json.
func GuideTemplate(doc RenamesDoc) string {
	type rows struct{ renamed, same, dropped, gaps []RenameRow }
	byType := map[string]*rows{}
	for _, r := range doc.Renames {
		if r.KreuzwerkerAttr == nil {
			continue
		}
		g := byType[r.Type]
		if g == nil {
			g = &rows{}
			byType[r.Type] = g
		}
		switch {
		case r.OurAttr == nil && strings.HasPrefix(strings.TrimSpace(r.Reason), "gap:"):
			g.gaps = append(g.gaps, r)
		case r.OurAttr == nil:
			g.dropped = append(g.dropped, r)
		case *r.OurAttr == *r.KreuzwerkerAttr && strings.HasPrefix(r.Transform, "identity") && !strings.Contains(r.Transform, "->"):
			g.same = append(g.same, r)
		default:
			g.renamed = append(g.renamed, r)
		}
	}
	var types []string
	for t := range byType {
		types = append(types, t)
	}
	// Resources first, then data sources, then actions.
	rank := func(t string) int {
		switch {
		case strings.HasPrefix(t, "data."):
			return 1
		case strings.HasPrefix(t, "action."):
			return 2
		}
		return 0
	}
	sort.Slice(types, func(i, j int) bool {
		if rank(types[i]) != rank(types[j]) {
			return rank(types[i]) < rank(types[j])
		}
		return types[i] < types[j]
	})
	kwType := map[string]string{}
	for _, tr := range doc.TypeRenames {
		kwType[tr.OurType] = tr.KreuzwerkerType
	}

	var b bytes.Buffer
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("%s", guideIntro)

	w("## Moved blocks per resource type\n\n")
	w("Every managed type below accepts a cross-provider `moved` block from kreuzwerker/docker. Rename the resource label (the `from` and `to` addresses must differ), keep the old address only in the `moved` block, and rewrite the resource body with the attribute names in the tables further down.\n\n")
	w("| kreuzwerker type | moves to | kreuzwerker state schema version | values that change on the move |\n|---|---|---|---|\n")
	for _, t := range types {
		if rank(t) != 0 {
			continue
		}
		note := guideMoveNotes[t]
		if note == "" {
			note = "-"
		}
		w("| %s | %s | %d | %s |\n", code(t), code(t), guideMoveSchema[t], mdCell(note))
	}
	w("\n```terraform\n")
	for _, t := range types {
		if rank(t) != 0 {
			continue
		}
		w("moved {\n  from = %s.web\n  to   = %s.web_moved\n}\n\n", t, t)
	}
	b.Truncate(b.Len() - 1)
	w("```\n\n")

	w("## Attribute mapping\n\n")
	w("Generated from `reference/_gen/renames.json` by `go run ./tools/parity -guide`. Per type: renamed or reshaped attributes, attributes kept as they are, attributes dropped with the reason, and kreuzwerker attributes this provider does not support yet. Data sources and actions have no state to move: rewrite their configuration with the same tables.\n\n")
	for _, t := range types {
		g := byType[t]
		title := t
		if k, ok := kwType[t]; ok {
			title = fmt.Sprintf("%s (kreuzwerker %s)", t, k)
		}
		w("### %s\n\n", code(title))
		if len(g.renamed) > 0 {
			w("Renamed or reshaped:\n\n| kreuzwerker | this provider | change |\n|---|---|---|\n")
			for _, r := range g.renamed {
				w("| %s | %s | %s |\n", code(*r.KreuzwerkerAttr), code(*r.OurAttr), mdCell(r.Transform))
			}
			w("\n")
		}
		if len(g.same) > 0 {
			var names []string
			for _, r := range g.same {
				names = append(names, code(*r.KreuzwerkerAttr))
			}
			w("Unchanged: %s.\n\n", strings.Join(names, ", "))
		}
		if len(g.dropped) > 0 {
			w("Dropped:\n\n| kreuzwerker | reason |\n|---|---|\n")
			for _, r := range g.dropped {
				w("| %s | %s |\n", code(*r.KreuzwerkerAttr), mdCell(r.Reason))
			}
			w("\n")
		}
		if len(g.gaps) > 0 {
			w("Not supported yet (the move skips them; remove them from the configuration):\n\n| kreuzwerker | note |\n|---|---|\n")
			for _, r := range g.gaps {
				w("| %s | %s |\n", code(*r.KreuzwerkerAttr), mdCell(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.Reason), "gap:"))))
			}
			w("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// writeGuide writes (or, with check, compares) the guide template.
func writeGuide(path string, doc RenamesDoc, check bool) error {
	want := GuideTemplate(doc)
	if !check {
		return os.WriteFile(path, []byte(want), 0o644)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
		return fmt.Errorf("%s is stale: run go run ./tools/parity -guide %s", path, path)
	}
	return nil
}

const guideIntro = `---
page_title: "Migrating from kreuzwerker/docker"
subcategory: ""
description: |-
  Move existing kreuzwerker/docker v4.6.0 resources into this provider with moved blocks, without recreating the Docker objects.
---

# Migrating from kreuzwerker/docker

This provider moves the state of every kreuzwerker/docker v4.6.0 managed resource with a ` + "`moved`" + ` block: the Docker objects stay as they are, Terraform hands the kreuzwerker state to this provider, and this provider rewrites it into its own schema (renamed attributes, MB to bytes, blocks to maps, ...). Data sources and actions hold no state; only their configuration changes.

## Requirements

- **Terraform 1.8 or later.** See the next section.
- **kreuzwerker/docker v4.6.0 state.** The move reads the state at kreuzwerker's latest schema version for each type (the table below). Run ` + "`terraform apply -refresh-only`" + ` once with kreuzwerker/docker v4.6.0 before migrating so older states are upgraded; a move from an older schema version fails with that instruction.
- **Different from and to addresses.** Terraform rejects a ` + "`moved`" + ` block whose ` + "`from`" + ` and ` + "`to`" + ` are the same address ("Redundant move statement"), so the resource label changes (` + "`docker_container.web`" + ` to ` + "`docker_container.web_moved`" + `). You can move it back to the old label later with an ordinary ` + "`moved`" + ` block (same provider, same type).

## Terraform 1.8 or later

Moving state between resource types or providers uses the MoveResourceState protocol call, added in Terraform 1.8. Terraform treats a ` + "`moved`" + ` block as a cross-provider move whenever the provider of the ` + "`to`" + ` resource differs from the provider recorded in state for the ` + "`from`" + ` resource, even when the type name (` + "`docker_container`" + `) is the same. Terraform reports the source provider as **` + "`registry.terraform.io/kreuzwerker/docker`" + `**, the address kreuzwerker/docker was installed from.

## How Terraform picks the providers

A ` + "`moved`" + ` block has no provider argument. The source provider is the one recorded in state for the ` + "`from`" + ` address (` + "`registry.terraform.io/kreuzwerker/docker`" + `); the target is the provider of the ` + "`to`" + ` resource in configuration. Both type names start with ` + "`docker_`" + `, so both providers want the default local name ` + "`docker`" + `. Give this provider the local name ` + "`docker`" + ` and kreuzwerker another local name while the migration runs:

` + "```terraform" + `
terraform {
  required_version = ">= 1.8"
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
    # Only while resources still use kreuzwerker: pins the version that wrote
    # the state (terraform init installs the providers named in state anyway).
    kreuzwerker = {
      source  = "kreuzwerker/docker"
      version = "4.6.0"
    }
  }
}

provider "docker" {}

# Not migrated yet: still managed by kreuzwerker.
resource "docker_network" "app" {
  provider = kreuzwerker
  name     = "app"
}
` + "```" + `

Resources without a ` + "`provider`" + ` argument use the local name ` + "`docker`" + `, which is now this provider.

## Steps

1. Run ` + "`terraform apply -refresh-only`" + ` with kreuzwerker/docker v4.6.0 so every state is at its latest schema version.
2. Change ` + "`required_providers`" + ` as above and run ` + "`terraform init -upgrade`" + `.
3. For each resource (or a few at a time): give it a new label, rewrite its body with this provider's attribute names (tables below), drop any ` + "`provider = kreuzwerker`" + ` argument, and add a ` + "`moved`" + ` block from the old address.
4. Run ` + "`terraform plan`" + `. Terraform moves the state, refreshes it, and plans only real differences. Expect in-place updates where kreuzwerker stored an SDKv2 empty value ("" or an empty list) that this provider keeps as null, or where a default differs; a plan to replace a resource means its configuration differs in a "Forces replacement." attribute: fix the configuration rather than applying a replacement.
5. ` + "`terraform apply`" + `. When no resource uses kreuzwerker any more, remove it from ` + "`required_providers`" + ` and remove the ` + "`moved`" + ` blocks once every workspace has applied them.

` + "```terraform" + `
# Before (kreuzwerker/docker v4.6.0)
resource "docker_container" "web" {
  name       = "web"
  image      = docker_image.nginx.image_id
  memory     = 512 # MB
  log_driver = "json-file"
  env        = ["APP_ENV=prod"]
  ports {
    internal = 80
    external = 8080
  }
  networks_advanced {
    name    = "app_net"
    aliases = ["web"]
  }
}

# After (JerryBalmer1/docker)
moved {
  from = docker_container.web
  to   = docker_container.web_moved
}

resource "docker_container" "web_moved" {
  name   = "web"
  image  = docker_image.nginx.id
  memory = 536870912 # bytes
  logging {
    driver = "json-file"
  }
  env = { APP_ENV = "prod" }
  ports {
    container_port = 80
    host_port      = 8080
  }
  networks = {
    app_net = { aliases = ["web"] }
  }
}
` + "```" + `

## Type rename

kreuzwerker's ` + "`data \"docker_logs\"`" + ` is ` + "`data \"docker_container_logs\"`" + ` here, beside ` + "`data \"docker_service_logs\"`" + ` and ` + "`data \"docker_task_logs\"`" + ` (one logs data source per Engine operation). Data sources hold no state: rename the block and its references.

## logging

Containers and services take one ` + "`logging { driver, options }`" + ` block. It is the same pair in all three upstream shapes:

| this provider | Engine container (HostConfig) | Swarm service (TaskTemplate) | compose |
|---|---|---|---|
| ` + "`logging.driver`" + ` | ` + "`LogConfig.Type`" + ` | ` + "`LogDriver.Name`" + ` | ` + "`logging.driver`" + ` |
| ` + "`logging.options`" + ` | ` + "`LogConfig.Config`" + ` | ` + "`LogDriver.Options`" + ` | ` + "`logging.options`" + ` |

kreuzwerker's ` + "`docker_container`" + ` ` + "`log_driver`" + ` / ` + "`log_opts`" + ` and ` + "`docker_service`" + ` ` + "`task_spec.log_driver { name, options }`" + ` both move into it.

## Service networks

` + "`docker_service`" + ` takes a ` + "`networks`" + ` map keyed by network name (or ID), each value ` + "`{ aliases, driver_opts }`" + `: one entry of the Swarm ` + "`TaskTemplate.Networks[]`" + ` list (` + "`Target`" + ` = the key, ` + "`Aliases`" + `, ` + "`DriverOpts`" + `). kreuzwerker's ` + "`task_spec.networks_advanced`" + ` blocks move into it (key = ` + "`name`" + `, or ` + "`id`" + ` when ` + "`name`" + ` is empty; ` + "`driver_opts`" + ` ` + "`\"k=v\"`" + ` strings become a map). ` + "`docker_container`" + ` takes the same kind of map for its networks (` + "`networks_advanced`" + ` plus the computed ` + "`network_data`" + `).

`
