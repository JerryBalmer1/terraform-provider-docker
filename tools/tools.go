//go:build tools

// Package tools pins build-time tools and the libraries the lanes rely on in
// go.mod, so `go mod tidy` keeps them before every lane imports them. Run
// tfplugindocs with `go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs`.
package tools

import (
	_ "github.com/docker/go-connections/nat"
	_ "github.com/docker/go-units"
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
	_ "github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	_ "github.com/hashicorp/terraform-plugin-log/tflog"
	_ "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	_ "github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/session"
	_ "github.com/moby/buildkit/session/filesync"
	_ "github.com/moby/buildkit/session/secrets/secretsprovider"
	_ "github.com/moby/patternmatcher"
	_ "github.com/moby/patternmatcher/ignorefile"
)
