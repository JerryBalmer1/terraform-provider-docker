package provider

// Pass 5 Lane A (WP img): docker_image build options. The classic Engine
// build options, the build attributes added in Pass 5 (renames.json `gap:`
// rows for docker_image: additional_contexts, cache_to, cgroup_parent,
// cpu_*, cpuset_*, extra_hosts, force_rm, isolation, memory, memswap,
// network, provenance, remote, sbom, shm_size, squash, tags, ulimits) and
// the call into the BuildKit seam (seams.go, implemented by Lane E).

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// imgUlimitModel is one build.ulimits value.
type imgUlimitModel struct {
	Soft types.Int64 `tfsdk:"soft"`
	Hard types.Int64 `tfsdk:"hard"`
}

var imgUlimitAttrTypes = map[string]attr.Type{"soft": types.Int64Type, "hard": types.Int64Type}

// imgBuildGapAttributes are the build block attributes added in Pass 5.
func imgBuildGapAttributes() map[string]schema.Attribute {
	nonNeg := []validator.Int64{int64validator.AtLeast(0)}
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, MarkdownDescription: desc + " Forces replacement."}
	}
	i64 := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Validators: nonNeg, MarkdownDescription: desc + " Forces replacement."}
	}
	return map[string]schema.Attribute{
		"additional_contexts": schema.MapAttribute{
			Optional: true, ElementType: types.StringType,
			MarkdownDescription: "BuildKit only. Named build contexts (`--build-context name=value`): a local directory, `docker-image://ref` or a URL, " +
				"used by `FROM name` / `COPY --from=name`. Forces replacement.",
		},
		"cache_to": schema.ListAttribute{
			Optional: true, ElementType: types.StringType,
			MarkdownDescription: "BuildKit only. Cache export specs (`--cache-to`), e.g. `type=inline` or `type=registry,ref=registry.example.com/app:cache`. Forces replacement.",
		},
		"cgroup_parent": str("Classic builder only. Parent cgroup for the `RUN` containers (`--cgroup-parent`)."),
		"cpu_period":    i64("Classic builder only. CPU CFS period in microseconds for `RUN` containers (`--cpu-period`)."),
		"cpu_quota":     i64("Classic builder only. CPU CFS quota in microseconds for `RUN` containers (`--cpu-quota`)."),
		"cpuset_cpus":   str("Classic builder only. CPUs the `RUN` containers may use, e.g. `0-3` or `0,1` (`--cpuset-cpus`)."),
		"cpuset_mems":   str("Classic builder only. Memory nodes the `RUN` containers may use (`--cpuset-mems`)."),
		"cpu_shares":    i64("Classic builder only. Relative CPU weight of the `RUN` containers (`--cpu-shares`)."),
		"extra_hosts": schema.ListAttribute{
			Optional: true, ElementType: types.StringType,
			MarkdownDescription: "Extra `/etc/hosts` entries for `RUN`, as `host:ip` (`--add-host`). Forces replacement.",
		},
		"force_rm": schema.BoolAttribute{
			Optional: true,
			MarkdownDescription: "Classic builder: always remove intermediate containers, even after a failed step (`--force-rm`). " +
				"Default `true` (the provider never leaves build containers behind). Forces replacement.",
		},
		"isolation": schema.StringAttribute{
			Optional:            true,
			Validators:          []validator.String{stringvalidator.OneOf("default", "process", "hyperv")},
			MarkdownDescription: "Classic builder only. Isolation technology of the `RUN` containers on Windows daemons: `default`, `process` or `hyperv` (`--isolation`). Forces replacement.",
		},
		"memory":  i64("Classic builder only. Memory limit in bytes for `RUN` containers (`--memory`)."),
		"memswap": schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.AtLeast(-1)}, MarkdownDescription: "Classic builder only. Memory plus swap limit in bytes for `RUN` containers; `-1` means unlimited swap (`--memory-swap`). Forces replacement."},
		"network": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Network mode for `RUN` instructions (`--network`): `default`, `none`, `host`, or (classic builder) a network name. Forces replacement.",
		},
		"provenance": schema.StringAttribute{
			Optional:            true,
			Validators:          []validator.String{stringvalidator.OneOf("min", "max", "false")},
			MarkdownDescription: "BuildKit only. Provenance attestation mode (`--provenance`): `min`, `max` or `false`. Forces replacement.",
		},
		"remote": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Classic builder only. Remote build context the daemon fetches instead of a local `context`: a Git URL " +
				"(`https://github.com/org/repo.git#ref:dir`) or the URL of a tarball or plain Dockerfile (`docker build <URL>`). " +
				"`dockerfile` is then relative to the remote context. Exactly one of `context` and `remote`. Forces replacement.",
		},
		"sbom": schema.BoolAttribute{
			Optional:            true,
			MarkdownDescription: "BuildKit only. Add an SBOM attestation (`--sbom`). Forces replacement.",
		},
		"shm_size": i64("Size of `/dev/shm` in bytes for `RUN` (`--shm-size`)."),
		"squash": schema.BoolAttribute{
			Optional:            true,
			MarkdownDescription: "Classic builder only (experimental daemons). Squash the new layers into one (`--squash`). Forces replacement.",
		},
		"tags": schema.ListAttribute{
			Optional: true, ElementType: types.StringType,
			MarkdownDescription: "Extra references to tag the built image with, besides `name` (`-t`, repeated). They are untagged on destroy unless " +
				"`keep_locally` is set. Forces replacement.",
		},
		"ssh": schema.ListNestedAttribute{
			Optional:            true,
			MarkdownDescription: "BuildKit only. SSH agent sockets or keys exposed to `RUN --mount=type=ssh` (`--ssh id=paths`). Forces replacement.",
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"id": schema.StringAttribute{Required: true, MarkdownDescription: "SSH ID, usually `default`. Forces replacement."},
				"paths": schema.ListAttribute{Optional: true, ElementType: types.StringType,
					MarkdownDescription: "Agent socket or private key paths; empty uses `SSH_AUTH_SOCK` of the provider process. Forces replacement."},
			}},
		},
		"no_cache_filter": schema.ListAttribute{
			Optional: true, ElementType: types.StringType,
			MarkdownDescription: "BuildKit only. Stages to build without cache (`--no-cache-filter`). Forces replacement.",
		},
		"outputs": schema.ListAttribute{
			Optional: true, ElementType: types.StringType,
			MarkdownDescription: "BuildKit only. Extra exporters besides loading the image into the daemon (`--output`), e.g. `type=local,dest=out` " +
				"or `type=registry` (push `name` and `tags`). Forces replacement.",
		},
		"attestations": schema.ListAttribute{
			Optional: true, ElementType: types.StringType,
			MarkdownDescription: "BuildKit only. Extra attestation specs (`--attest`), e.g. `type=sbom,generator=image`; `provenance` and `sbom` " +
				"cover the common cases. Forces replacement.",
		},
		"ulimits": schema.MapNestedAttribute{
			Optional:            true,
			MarkdownDescription: "Resource limits for `RUN`, keyed by limit name such as `nofile` or `nproc` (`--ulimit name=soft:hard`). Forces replacement.",
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"soft": schema.Int64Attribute{Required: true, MarkdownDescription: "Soft limit. Forces replacement."},
				"hard": schema.Int64Attribute{Required: true, MarkdownDescription: "Hard limit. Forces replacement."},
			}},
		},
	}
}

// imgBuildKitOnly and imgClassicOnly list the attributes tied to one builder.
var (
	imgBuildKitOnly = []string{"additional_contexts", "cache_to", "provenance", "sbom", "ssh", "no_cache_filter", "outputs", "attestations"}
	imgClassicOnly  = []string{"cgroup_parent", "cpu_period", "cpu_quota", "cpuset_cpus", "cpuset_mems", "cpu_shares", "isolation", "memory", "memswap", "remote", "squash"}
)

// imgBuildAttrSet reports which of the Pass 5 attributes are set (known and
// not null).
func imgBuildAttrSet(b *imageBuildModel) map[string]bool {
	set := func(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }
	return map[string]bool{
		"additional_contexts": set(b.AdditionalContexts), "cache_to": set(b.CacheTo), "provenance": set(b.Provenance), "sbom": set(b.SBOM),
		"cgroup_parent": set(b.CgroupParent), "cpu_period": set(b.CPUPeriod), "cpu_quota": set(b.CPUQuota), "cpuset_cpus": set(b.CPUSetCPUs),
		"cpuset_mems": set(b.CPUSetMems), "cpu_shares": set(b.CPUShares), "isolation": set(b.Isolation), "memory": set(b.Memory),
		"memswap": set(b.Memswap), "remote": set(b.Remote), "squash": set(b.Squash),
		"ssh": len(b.SSH) > 0, "no_cache_filter": set(b.NoCacheFilter), "outputs": set(b.Outputs), "attestations": set(b.Attestations),
	}
}

// imgValidateBuildGaps checks the builder-specific Pass 5 attributes.
func imgValidateBuildGaps(_ context.Context, b *imageBuildModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if b == nil || b.Builder.IsUnknown() {
		return diags
	}
	root := path.Root("build")
	set := imgBuildAttrSet(b)
	bk := imgUsesBuildKit(b)
	for _, a := range imgBuildKitOnly {
		if set[a] && !bk {
			diags.AddAttributeError(root.AtName(a), "BuildKit-only attribute",
				fmt.Sprintf("build.%s is only supported with builder = %q or a buildx builder name.", a, imgBuilderBuildKit))
		}
	}
	for _, a := range imgClassicOnly {
		if set[a] && bk {
			diags.AddAttributeError(root.AtName(a), "Classic-builder-only attribute",
				fmt.Sprintf("build.%s is only supported by the classic builder (builder unset or %q); BuildKit does not take it.", a, imgBuilderClassic))
		}
	}
	return diags
}

// imgUlimits returns build.ulimits sorted by name.
func imgUlimits(ctx context.Context, b *imageBuildModel, diags *diag.Diagnostics) []*container.Ulimit {
	if b.Ulimits.IsNull() || b.Ulimits.IsUnknown() {
		return nil
	}
	m := map[string]imgUlimitModel{}
	diags.Append(b.Ulimits.ElementsAs(ctx, &m, false)...)
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]*container.Ulimit, 0, len(names))
	for _, n := range names {
		out = append(out, &container.Ulimit{Name: n, Soft: m[n].Soft.ValueInt64(), Hard: m[n].Hard.ValueInt64()})
	}
	return out
}

// imgBuildOptions maps the plan to ImageBuildOptions (no auth, no context).
func imgBuildOptions(ctx context.Context, plan *imageResourceModel, dockerfile string, diags *diag.Diagnostics) client.ImageBuildOptions {
	b := plan.Build
	platform := b.Platform.ValueString()
	if platform == "" {
		platform = plan.Platform.ValueString()
	}
	args := map[string]*string{}
	for k, v := range stringMapFrom(ctx, b.Args, diags) {
		args[k] = &v
	}
	tags := []string{plan.Name.ValueString()}
	for _, t := range stringListFrom(ctx, b.Tags, diags) {
		if t != plan.Name.ValueString() {
			tags = append(tags, t)
		}
	}
	forceRm := true
	if !b.ForceRm.IsNull() && !b.ForceRm.IsUnknown() {
		forceRm = b.ForceRm.ValueBool()
	}
	return client.ImageBuildOptions{
		Tags:         tags,
		Dockerfile:   dockerfile,
		BuildArgs:    args,
		Target:       b.Target.ValueString(),
		Platforms:    platformsFromString(platform),
		Labels:       mergeLabels(stringMapFrom(ctx, b.Labels, diags), map[string]string{labelManagedBy: labelManagedByValue}),
		NoCache:      b.NoCache.ValueBool(),
		PullParent:   b.Pull.ValueBool(),
		Remove:       true,
		ForceRemove:  forceRm,
		Version:      build.BuilderV1,
		CgroupParent: b.CgroupParent.ValueString(),
		CPUPeriod:    b.CPUPeriod.ValueInt64(),
		CPUQuota:     b.CPUQuota.ValueInt64(),
		CPUSetCPUs:   b.CPUSetCPUs.ValueString(),
		CPUSetMems:   b.CPUSetMems.ValueString(),
		CPUShares:    b.CPUShares.ValueInt64(),
		ExtraHosts:   stringListFrom(ctx, b.ExtraHosts, diags),
		Isolation:    container.Isolation(b.Isolation.ValueString()),
		Memory:       b.Memory.ValueInt64(),
		MemorySwap:   b.Memswap.ValueInt64(),
		NetworkMode:  b.Network.ValueString(),
		ShmSize:      b.ShmSize.ValueInt64(),
		Squash:       b.Squash.ValueBool(),
		Ulimits:      imgUlimits(ctx, b, diags),
	}
}

// imgSeamRequest maps the build block to a BuildKit seam request.
func imgSeamRequest(ctx context.Context, plan *imageResourceModel, opts client.ImageBuildOptions, diags *diag.Diagnostics) seamBuildRequest {
	b := plan.Build
	dir, _ := filepath.Abs(b.Context.ValueString())
	args := map[string]string{}
	for k, v := range opts.BuildArgs {
		if v != nil {
			args[k] = *v
		}
	}
	platforms := stringListFrom(ctx, b.Platforms, diags)
	if len(platforms) == 0 {
		if p := platformsString(opts.Platforms); p != "" {
			platforms = []string{p}
		}
	}
	var secrets []seamBuildSecret
	for _, s := range b.Secrets {
		secrets = append(secrets, seamBuildSecret{ID: s.ID.ValueString(), Src: s.Src.ValueString(), Env: s.Env.ValueString()})
	}
	var ulimits []string
	for _, u := range opts.Ulimits {
		ulimits = append(ulimits, fmt.Sprintf("%s=%d:%d", u.Name, u.Soft, u.Hard))
	}
	var ssh []seamBuildSSH
	for _, e := range b.SSH {
		ssh = append(ssh, seamBuildSSH{ID: e.ID.ValueString(), Paths: stringListFrom(ctx, e.Paths, diags)})
	}
	return seamBuildRequest{
		ContextDir:    dir,
		Dockerfile:    b.Dockerfile.ValueString(),
		Target:        opts.Target,
		Platforms:     platforms,
		BuildArgs:     args,
		Labels:        opts.Labels,
		Secrets:       secrets,
		CacheFrom:     stringListFrom(ctx, b.CacheFrom, diags),
		CacheTo:       stringListFrom(ctx, b.CacheTo, diags),
		Outputs:       seamBuildOutputs{Names: opts.Tags, Load: true, Raw: stringListFrom(ctx, b.Outputs, diags)},
		Network:       opts.NetworkMode,
		NoCache:       opts.NoCache,
		Pull:          opts.PullParent,
		ExtraHosts:    opts.ExtraHosts,
		ShmSize:       opts.ShmSize,
		Ulimits:       ulimits,
		NoCacheFilter: stringListFrom(ctx, b.NoCacheFilter, diags),
		SSH:           ssh,
		Builder:       imgSeamBuilder(b),
		Attestations: seamBuildAttestations{
			Provenance: b.Provenance.ValueString(),
			SBOM:       b.SBOM.ValueBool(),
			Extra:      stringListFrom(ctx, b.Attestations, diags),
		},
		Progress: func(line string) { tflog.Debug(ctx, line) },
	}
}

// imgBuildViaSeam runs a BuildKit build through seamBuildKitImpl (Lane E).
// The result carries the image ID, the pushed digest, the platforms and
// the build metadata.
func imgBuildViaSeam(ctx context.Context, pd *providerData, plan *imageResourceModel, opts client.ImageBuildOptions) (*seamBuildResult, diag.Diagnostics) {
	var diags diag.Diagnostics
	b := plan.Build
	req := imgSeamRequest(ctx, plan, opts, &diags)
	// Owner label (Pass 5.3): build labels become image labels.
	req.Labels = pd.ownerInject(req.Labels)
	if !b.AdditionalContexts.IsNull() && !b.AdditionalContexts.IsUnknown() {
		req.Contexts = stringMapFrom(ctx, b.AdditionalContexts, &diags)
	}
	if diags.HasError() {
		return nil, diags
	}
	tflog.Info(ctx, "building image with BuildKit", map[string]any{"name": plan.Name.ValueString(), "context": req.ContextDir,
		"platforms": req.Platforms, "builder": req.Builder.Name})
	res, err := seamBuildKitImpl.Build(ctx, pd, req)
	if err != nil {
		diags.AddError("Image build failed", fmt.Sprintf("%s: %v", plan.Name.ValueString(), err))
		return nil, diags
	}
	if res == nil {
		diags.AddError("Image build failed", fmt.Sprintf("%s: the BuildKit build returned no result", plan.Name.ValueString()))
	}
	return res, diags
}
