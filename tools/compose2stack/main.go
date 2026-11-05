// Command compose2stack converts a Docker Compose file into Terraform HCL for
// this provider: a docker_stack resource (-mode swarm, the default) or
// docker_network, docker_volume and docker_container resources (-mode local).
// Anything compose supports that the provider cannot express is kept as a
// "# TODO:" comment next to where it would go.
//
//	go run ./tools/compose2stack -in docker-compose.yml -out stack.tf [-mode swarm|local] [-project name] [-profiles a,b]
//
// Interpolation uses the process environment, like `docker compose config`.
// Relative paths are written relative to the output file's module
// (${path.module}), so keep the .tf file next to the compose file or adjust
// them.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	in := flag.String("in", "", "compose file to convert (required)")
	out := flag.String("out", "", "output .tf file (default: stdout)")
	mode := flag.String("mode", modeSwarm, "swarm (docker_stack) or local (docker_container/docker_network/docker_volume)")
	project := flag.String("project", "", "project name (default: the file's name: or its directory)")
	profiles := flag.String("profiles", "", "comma-separated compose profiles to enable")
	flag.Parse()
	if *in == "" {
		fmt.Fprintln(os.Stderr, "compose2stack: -in is required")
		flag.Usage()
		os.Exit(2)
	}
	opts := Options{Mode: *mode, Project: *project, Env: os.Environ()}
	if *profiles != "" {
		opts.Profiles = strings.Split(*profiles, ",")
	}
	b, err := Convert(context.Background(), *in, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compose2stack:", err)
		os.Exit(1)
	}
	if *out == "" {
		_, _ = os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "compose2stack:", err)
		os.Exit(1)
	}
}
