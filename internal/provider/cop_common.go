package provider

// Pass 5 Lane A (WP cop): helpers shared by the container operation data
// sources and actions (docker_container data source, changes / file / stats /
// top, the container verb actions, wait, cp).

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// copDSBase is the Configure half every cop data source shares.
type copDSBase struct {
	pd *providerData
}

func (d *copDSBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// ready reports whether the provider is configured, adding an error if not.
func (d *copDSBase) ready(diags *diag.Diagnostics) bool {
	if d.pd == nil || d.pd.Client == nil {
		diags.AddError("Provider not configured", "The docker provider was not configured.")
		return false
	}
	return true
}

// copContainerInputAttr is the `container` input (ID or name) of the
// container data sources.
func copContainerInputAttr() schema.StringAttribute {
	return schema.StringAttribute{Required: true, MarkdownDescription: "The container to read, by ID or name."}
}

// copContainerIDAttr is the computed `container_id` identity output.
func copContainerIDAttr(what string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the container " + what + "."}
}

// copInspect inspects a container by ID or name.
func copInspect(ctx context.Context, c *client.Client, ref string) (container.InspectResponse, []byte, error) {
	res, err := c.ContainerInspect(ctx, ref, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, nil, err
	}
	return res.Container, res.Raw, nil
}

// copName returns a container name without the leading slash.
func copName(n string) string { return strings.TrimPrefix(n, "/") }

// copShortID shortens an ID for messages.
func copShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// copInspectErr reports a failed container lookup.
func copInspectErr(diags *diag.Diagnostics, typ, ref string, err error) {
	diags.AddError(typ+": cannot find container", fmt.Sprintf("container %q: %v", ref, err))
}
