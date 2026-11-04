package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/JerryBalmer1/terraform-provider-docker/internal/provider"
)

// version is set by GoReleaser (-X main.version=...); the default is the
// release this source tree is.
var version = "0.1.0"

func main() {
	var debug, showVersion bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.BoolVar(&showVersion, "version", false, "print the provider version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		return
	}

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/JerryBalmer1/docker",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
