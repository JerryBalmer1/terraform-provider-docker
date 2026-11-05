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

	// Pass 3: Terraform 1.14 surface (action, ephemeral, function, list ship in
	// terraform-plugin-framework v1.19.0) and the mux server matching plugin-go.
	_ "github.com/hashicorp/terraform-plugin-framework/action"
	_ "github.com/hashicorp/terraform-plugin-framework/ephemeral"
	_ "github.com/hashicorp/terraform-plugin-framework/function"
	_ "github.com/hashicorp/terraform-plugin-framework/list"
	_ "github.com/hashicorp/terraform-plugin-mux/tf6muxserver"

	// Pass 3 Lane A: pure-Go SSH transport, docker CLI contexts and credentials.
	_ "github.com/docker/cli/cli/config"
	_ "github.com/docker/cli/cli/config/credentials"
	_ "github.com/docker/cli/cli/context/docker"
	_ "github.com/docker/cli/cli/context/store"
	_ "golang.org/x/crypto/ssh"
	_ "golang.org/x/crypto/ssh/agent"
	_ "golang.org/x/crypto/ssh/knownhosts"

	// Pass 3 Lane B: compose file parsing.
	_ "github.com/compose-spec/compose-go/v2/cli"
	_ "github.com/compose-spec/compose-go/v2/loader"
	_ "github.com/compose-spec/compose-go/v2/types"
)
