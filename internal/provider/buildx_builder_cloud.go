package provider

// Pass 6c Lane C: docker_buildx_builder with driver = "cloud" (`docker
// buildx create --driver cloud <org>/<builder>`), a Docker Build Cloud
// builder. Like the remote driver nothing is created on the Docker daemon:
// Create logs in and lists the builder's workers (when `bootstrap`), Read
// reports them, Delete forgets the builder (buildx's cloud driver Rm does
// nothing either, driver/cloud/driver.go:130-132). The connection and its
// token are in buildkit_cloud.go; state holds the endpoint, the id
// cloud://<org>/<builder> and the reported workers, never the token.
//
// PARTIAL(pass5) buildx:builder not verified against the live service (driver = "cloud", Docker Build Cloud)

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

// bxbValidateCloud checks the arguments of a cloud builder.
func bxbValidateCloud(m bxbModel, diags *diag.Diagnostics) {
	if m.Endpoint.IsNull() {
		diags.AddAttributeError(path.Root("endpoint"), "Missing endpoint",
			"driver = \"cloud\" needs endpoint, the Docker Build Cloud builder as <org>/<builder>.")
	} else if ep, ok := knownString(m.Endpoint); ok {
		if _, err := bkCloudBuilder(ep); err != nil {
			diags.AddAttributeError(path.Root("endpoint"), "Invalid endpoint",
				fmt.Sprintf("%q: a Docker Build Cloud %s (cloud://<account>/<builder> is also accepted).", ep, err.Error()))
		}
	}
	for name, set := range map[string]bool{
		"docker_container": m.DockerContainer != nil, "remote": m.Remote != nil, "tcp_port": !m.TCPPort.IsNull(),
		"buildkitd_flags": !m.BuildkitdFlags.IsNull(), "buildkitd_config": !m.BuildkitdConfig.IsNull(),
		"buildkitd_config_content": !m.BuildkitdConfigContent.IsNull(),
		"append":                   m.Append.ValueBool(), "node": !m.Node.IsNull() && !m.Node.IsUnknown(),
	} {
		if set {
			diags.AddAttributeError(path.Root(name), "Not used by the cloud driver",
				fmt.Sprintf("%s configures a BuildKit the provider runs or dials itself; a Docker Build Cloud builder is run by Docker "+
					"(buildx's cloud driver rejects config files and buildkitd flags too).", name))
		}
	}
}
