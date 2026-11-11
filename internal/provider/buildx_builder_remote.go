package provider

// Pass 5.3 Lane B: docker_buildx_builder with driver = "remote" (`docker
// buildx create --driver remote <endpoint>`): a BuildKit daemon the user
// runs, reached directly at a tcp:// or unix:// address, with the remote
// driver's TLS options (`--driver-opt cacert=,cert=,key=,servername=`).
// Nothing is created on the Docker daemon: Create checks the endpoint
// (ListWorkers, when `bootstrap`), Read reports its workers, Delete only
// forgets it. Builds select it by its endpoint (`docker_image.build.builder
// = docker_buildx_builder.x.endpoint`, `docker_buildx_bake.builder`), since
// buildx's client-side instance store, where buildx keeps the name, is not
// written.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	bkclient "github.com/moby/buildkit/client"
)

const bxbDriverRemote = "remote"

// bxbRemoteModel is the remote block (remote driver options).
type bxbRemoteModel struct {
	CACert     types.String `tfsdk:"cacert"`
	Cert       types.String `tfsdk:"cert"`
	Key        types.String `tfsdk:"key"`
	ServerName types.String `tfsdk:"servername"`
}

// bxbRemoteBlock is the schema of the remote block.
func bxbRemoteBlock() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		MarkdownDescription: "TLS settings of a `remote` driver builder (`--driver-opt cacert=,cert=,key=,servername=`). Only with " +
			"`driver = \"remote\"`. Changing anything here forces a new resource.",
		PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
		Attributes: map[string]schema.Attribute{
			"cacert": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Path of the CA certificate that signed the BuildKit server certificate (`cacert=`)."},
			"cert": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Path of the client certificate presented to BuildKit (`cert=`); needs `key`."},
			"key": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Path of the client certificate's private key (`key=`); needs `cert`."},
			"servername": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Server name the BuildKit certificate is checked against (`servername=`). Default: the endpoint's host."},
		},
	}
}

// bxbRemoteDriverOpts returns the remote block as buildx driver options.
func bxbRemoteDriverOpts(r *bxbRemoteModel) map[string]string {
	out := map[string]string{}
	if r == nil {
		return out
	}
	for k, v := range map[string]types.String{"cacert": r.CACert, "cert": r.Cert, "key": r.Key, "servername": r.ServerName} {
		if s, ok := knownString(v); ok && s != "" {
			out[k] = expandHome(s)
		}
	}
	return out
}

// bkTLSOpts turns remote driver options into BuildKit client options.
func bkTLSOpts(opts map[string]string) []bkclient.ClientOpt {
	var out []bkclient.ClientOpt
	if opts["cacert"] != "" || opts["servername"] != "" {
		out = append(out, bkclient.WithServerConfig(opts["servername"], opts["cacert"]))
	}
	if opts["cert"] != "" || opts["key"] != "" {
		out = append(out, bkclient.WithCredentials(opts["cert"], opts["key"]))
	}
	return out
}

// bxbValidateRemote checks the driver-specific arguments.
func bxbValidateRemote(m bxbModel, diags *diag.Diagnostics) {
	if m.Driver.IsUnknown() {
		return
	}
	if m.Driver.ValueString() == bxbDriverCloud {
		bxbValidateCloud(m, diags)
		return
	}
	remote := m.Driver.ValueString() == bxbDriverRemote
	if remote {
		ep, ok := knownString(m.Endpoint)
		if ok && !(strings.HasPrefix(ep, "tcp://") || strings.HasPrefix(ep, "unix://") || strings.HasPrefix(ep, "npipe://")) {
			diags.AddAttributeError(path.Root("endpoint"), "Invalid endpoint",
				fmt.Sprintf("%q: a remote builder endpoint is tcp://host:port, unix:///path or npipe:////./pipe/name.", ep))
		}
		if m.Endpoint.IsNull() {
			diags.AddAttributeError(path.Root("endpoint"), "Missing endpoint", "driver = \"remote\" needs endpoint, the BuildKit address.")
		}
		for name, set := range map[string]bool{
			"docker_container": m.DockerContainer != nil, "tcp_port": !m.TCPPort.IsNull(), "buildkitd_flags": !m.BuildkitdFlags.IsNull(),
			"buildkitd_config": !m.BuildkitdConfig.IsNull(), "buildkitd_config_content": !m.BuildkitdConfigContent.IsNull(),
			"append": m.Append.ValueBool(), "node": !m.Node.IsNull() && !m.Node.IsUnknown(),
		} {
			if set {
				diags.AddAttributeError(path.Root(name), "Not used by the remote driver",
					fmt.Sprintf("%s configures the docker-container driver's node container; a remote builder is a BuildKit daemon run elsewhere.", name))
			}
		}
		if r := m.Remote; r != nil && r.Cert.IsNull() != r.Key.IsNull() && !r.Cert.IsUnknown() && !r.Key.IsUnknown() {
			diags.AddAttributeError(path.Root("remote"), "Incomplete client certificate", "Set both remote.cert and remote.key, or neither.")
		}
		return
	}
	if !m.Endpoint.IsNull() && !m.Endpoint.IsUnknown() {
		diags.AddAttributeError(path.Root("endpoint"), "Endpoint is computed for docker-container",
			"endpoint is only set with driver = \"remote\" or \"cloud\"; a docker-container builder reports docker-container://buildx_buildkit_<node>.")
	}
	if m.Remote != nil {
		diags.AddAttributeError(path.Root("remote"), "Only for the remote driver", "The remote block configures driver = \"remote\".")
	}
}

// bxbDirectDriver reports a driver whose BuildKit runs elsewhere (remote,
// cloud): nothing is created on the daemon, Create checks it, Read reports
// it, Delete forgets it.
func bxbDirectDriver(driver string) bool {
	return driver == bxbDriverRemote || driver == bxbDriverCloud
}

// bxbRemoteNode is the bkNode of a remote or cloud builder.
func bxbRemoteNode(m bxbModel) bkNode {
	name := m.Name.ValueString()
	if m.Driver.ValueString() == bxbDriverCloud {
		if n, err := bkCloudNode(name, m.Endpoint.ValueString()); err == nil {
			return n
		}
	}
	return bkNode{
		Builder: name, Name: name + "0", Driver: bkDriverRemote, Endpoint: m.Endpoint.ValueString(),
		Status: bkStatusRunning, DriverOpts: bxbRemoteDriverOpts(m.Remote),
	}
}

// bxbRemoteWorkers lists the workers of a remote builder.
func bxbRemoteWorkers(ctx context.Context, pd *providerData, n bkNode) ([]*bkclient.WorkerInfo, error) {
	c, err := bkOpen(ctx, pd, n)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.ListWorkers(ctx)
}

// bxbRemoteFill sets the computed values of a remote builder.
func (r *buildxBuilderResource) bxbRemoteFill(ctx context.Context, m *bxbModel, ws []*bkclient.WorkerInfo, haveWS bool) diag.Diagnostics {
	n := bxbRemoteNode(*m)
	m.ID = types.StringValue(n.Endpoint)
	m.Node = types.StringValue(n.Name)
	m.ContainerName = types.StringNull()
	m.VolumeName = types.StringNull()
	m.TCPAddress = types.StringNull()
	if m.Append.IsNull() || m.Append.IsUnknown() {
		m.Append = types.BoolValue(false)
	}
	known := map[string][]*bkclient.WorkerInfo{}
	if haveWS {
		known[n.Name] = ws
		if ws == nil {
			known[n.Name] = []*bkclient.WorkerInfo{}
		}
	}
	view := bxBuilderViewOf(ctx, r.pd, []bkNode{n}, known)
	nv := view.first()
	m.Platforms = stringListValue(bxStrings(nv.Platforms), false)
	m.Labels = stringMapValue(bxStringMap(nv.Labels), false)
	return m.contractFill(ctx, r.pd, contractFillInput{
		Raw:             map[string][]byte{"inspect_json": view.json()},
		Structured:      map[string]any{"builder": view.structured()},
		BuildKitVersion: nv.Version,
	})
}

// bxbRemoteCreate is Create for driver = "remote".
func (r *buildxBuilderResource) bxbRemoteCreate(ctx context.Context, plan *bxbModel) diag.Diagnostics {
	var diags diag.Diagnostics
	n := bxbRemoteNode(*plan)
	var ws []*bkclient.WorkerInfo
	if plan.Bootstrap.ValueBool() {
		var err error
		ws, err = bxbRemoteWorkers(ctx, r.pd, n)
		if err != nil {
			what := "Cannot reach the remote BuildKit"
			if n.Driver == bkDriverCloud {
				what = "Cannot reach the Docker Build Cloud builder"
			}
			diags.AddAttributeError(path.Root("endpoint"), what, fmt.Sprintf("%s: %v", n.Endpoint, err))
			return diags
		}
	}
	diags.Append(r.bxbRemoteFill(ctx, plan, ws, plan.Bootstrap.ValueBool())...)
	return diags
}
