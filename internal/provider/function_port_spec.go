package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/go-connections/nat"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var portSpecAttrTypes = map[string]attr.Type{
	"host_ip":        types.StringType,
	"host_port":      types.StringType,
	"container_port": types.StringType,
	"protocol":       types.StringType,
}

type portSpecParts struct {
	HostIP, HostPort, ContainerPort, Protocol string
}

// parsePortSpec parses docker `-p` syntax: [host_ip:][host_port:]container_port[/protocol].
// host_ip may be a bracketed IPv6 address; ports may be ranges (`8000-8010`).
// Validation is the daemon's own (go-connections nat.ParsePortSpec).
func parsePortSpec(spec string) (portSpecParts, error) {
	if strings.TrimSpace(spec) != spec || spec == "" {
		return portSpecParts{}, fmt.Errorf("port spec %q must be non-empty and have no surrounding spaces", spec)
	}
	if _, err := nat.ParsePortSpec(spec); err != nil {
		return portSpecParts{}, err
	}
	proto, rest := nat.SplitProtoPort(spec)
	if strings.Count(spec, "/") > 1 {
		return portSpecParts{}, fmt.Errorf("port spec %q has more than one '/'", spec)
	}
	p := portSpecParts{Protocol: strings.ToLower(proto)}
	parts := strings.Split(rest, ":")
	switch n := len(parts); n {
	case 1:
		p.ContainerPort = parts[0]
	case 2:
		p.HostPort, p.ContainerPort = parts[0], parts[1]
	default:
		p.HostIP, p.HostPort, p.ContainerPort = strings.Join(parts[:n-2], ":"), parts[n-2], parts[n-1]
	}
	p.HostIP = strings.TrimSuffix(strings.TrimPrefix(p.HostIP, "["), "]")
	return p, nil
}

var _ function.Function = (*portSpecFunction)(nil)

// NewPortSpecFunction returns provider::docker::port_spec.
func NewPortSpecFunction() function.Function { return &portSpecFunction{} }

type portSpecFunction struct{}

func (f *portSpecFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "port_spec"
}

func (f *portSpecFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary: "Parse a docker -p port publishing spec.",
		MarkdownDescription: "Parses a `docker run -p` port spec, `[host_ip:][host_port:]container_port[/protocol]`, into `host_ip`, `host_port`, " +
			"`container_port` and `protocol`. IPv6 host addresses go in brackets (`[::1]:8080:80`), ports may be ranges (`8000-8010:80-90`), " +
			"and the protocol defaults to `tcp`. Absent host parts are empty strings; an invalid spec is an error.",
		Parameters: []function.Parameter{
			function.StringParameter{Name: "spec", MarkdownDescription: "Port spec, e.g. `8080:80`, `127.0.0.1:53:53/udp` or `[::1]:8443:443`."},
		},
		Return: function.ObjectReturn{AttributeTypes: portSpecAttrTypes},
	}
}

func (f *portSpecFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var spec string
	resp.Error = req.Arguments.Get(ctx, &spec)
	if resp.Error != nil {
		return
	}
	p, err := parsePortSpec(spec)
	if err != nil {
		resp.Error = function.NewArgumentFuncError(0, fmt.Sprintf("invalid port spec %q: %v", spec, err))
		return
	}
	obj, diags := types.ObjectValue(portSpecAttrTypes, map[string]attr.Value{
		"host_ip":        types.StringValue(p.HostIP),
		"host_port":      types.StringValue(p.HostPort),
		"container_port": types.StringValue(p.ContainerPort),
		"protocol":       types.StringValue(p.Protocol),
	})
	if diags.HasError() {
		resp.Error = function.FuncErrorFromDiags(ctx, diags)
		return
	}
	resp.Error = resp.Result.Set(ctx, obj)
}
