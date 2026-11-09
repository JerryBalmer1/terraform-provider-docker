package provider

// Pass 5 Lane A (WP obj): docker_plugin_create (Q8: PluginCreate from a
// host directory holding config.json and rootfs/) and docker_plugin_push.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure = (*objPluginCreateAction)(nil)
	_ action.ActionWithConfigure = (*objPluginPushAction)(nil)
)

// objTarDir writes dir as an uncompressed tar stream with paths relative to
// dir, keeping file modes and symlinks (a plugin rootfs needs both).
func objTarDir(dir string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// objCheckPluginDir checks that dir holds config.json and a rootfs directory.
func objCheckPluginDir(dir string) error {
	if fi, err := os.Stat(filepath.Join(dir, "config.json")); err != nil || fi.IsDir() {
		return fmt.Errorf("%s has no config.json file", dir)
	}
	if fi, err := os.Stat(filepath.Join(dir, "rootfs")); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s has no rootfs directory", dir)
	}
	return nil
}

// ---------------------------------------------------------------------------
// docker_plugin_create

// NewPluginCreateAction returns the docker_plugin_create action.
func NewPluginCreateAction() action.Action { return &objPluginCreateAction{} }

type objPluginCreateAction struct{ actBase }

type objPluginCreateModel struct {
	Name          types.String `tfsdk:"name"`
	ContextDir    types.String `tfsdk:"context_dir"`
	Enable        types.Bool   `tfsdk:"enable"`
	EnableTimeout types.Int64  `tfsdk:"enable_timeout"`
	ResultPath    types.String `tfsdk:"result_path"`
}

func (a *objPluginCreateAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin_create"
}

func (a *objPluginCreateAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates a local plugin from a host directory holding `config.json` and a `rootfs/` directory, like " +
			"`docker plugin create` (`POST /plugins/create`). The plugin is created disabled unless `enable = true`; push it with " +
			"`docker_plugin_push`. To install a plugin from a registry and manage it, use the `docker_plugin` resource instead. " +
			"Creating a plugin whose name already exists fails.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true,
				MarkdownDescription: "Plugin name with an optional tag, e.g. `registry.example.com/org/myplugin:1.0` (`latest` when untagged)."},
			"context_dir": schema.StringAttribute{Required: true,
				MarkdownDescription: "Host directory holding the plugin's `config.json` and its `rootfs/` directory; it is sent as a tar stream."},
			"enable": schema.BoolAttribute{Optional: true,
				MarkdownDescription: "Enable the plugin after creating it. Default `false`."},
			"enable_timeout": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Seconds the daemon waits for the plugin to start when `enable = true`. Default `0` (the daemon's default)."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *objPluginCreateAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m objPluginCreateModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() || !a.pd.gateItemAPI(ctx, "engine:plugin-create", &resp.Diagnostics) {
		return
	}
	dir := expandHome(m.ContextDir.ValueString())
	if err := objCheckPluginDir(dir); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("context_dir"), "Invalid plugin directory", err.Error())
		return
	}
	buf, err := objTarDir(dir)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("context_dir"), "Cannot read plugin directory", err.Error())
		return
	}
	name := m.Name.ValueString()
	actProgress(ctx, resp, "creating plugin %s from %s (%s)", name, dir, actHumanSize(uint64(buf.Len())))
	if _, err := c.PluginCreate(ctx, buf, client.PluginCreateOptions{RepoName: name}); err != nil {
		resp.Diagnostics.AddError("docker_plugin_create failed", fmt.Sprintf("%s: %v", name, err))
		return
	}
	pRes, err := c.PluginInspect(ctx, name, client.PluginInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("docker_plugin_create failed", fmt.Sprintf("inspecting created plugin %s: %v", name, err))
		return
	}
	p := pRes.Plugin
	enabled := p.Enabled
	if m.Enable.ValueBool() && !enabled {
		if _, err := c.PluginEnable(ctx, p.ID, client.PluginEnableOptions{Timeout: int(m.EnableTimeout.ValueInt64())}); err != nil {
			resp.Diagnostics.AddError("docker_plugin_create failed", fmt.Sprintf("plugin %s was created but cannot be enabled: %v", name, err))
			return
		}
		enabled = true
	}
	actProgress(ctx, resp, "docker_plugin_create: created %s (%s), enabled=%t", p.Name, p.ID, enabled)
	actFinishResult(ctx, resp, a.pd, "engine:plugin-create", map[string]any{
		"name":   p.Name,
		"id":     p.ID,
		"result": map[string]any{"enabled": enabled},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_plugin_push

// NewPluginPushAction returns the docker_plugin_push action.
func NewPluginPushAction() action.Action { return &objPluginPushAction{} }

type objPluginPushAction struct{ actBase }

type objPluginPushModel struct {
	Name       types.String `tfsdk:"name"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *objPluginPushAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin_push"
}

func (a *objPluginPushAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Pushes a local plugin to its registry, like `docker plugin push` (`POST /plugins/{name}/push`), using the " +
			"provider's `registry_auth` credentials for the plugin's registry. The pushed digest is reported as `digest`.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true,
				MarkdownDescription: "Name of the local plugin to push, e.g. `registry.example.com/org/myplugin:1.0`; the registry is taken from it."},
			"result_path": actResultPathAttribute(),
		},
	}
}

var objPushDigestRe = regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`)

// objPushMessage is one line of a push progress stream.
type objPushMessage struct {
	Status      string `json:"status"`
	Error       string `json:"error"`
	ErrorDetail *struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
	Aux json.RawMessage `json:"aux"`
}

// objReadPushStream drains a push stream and returns the digest and the
// last status line. An error message in the stream is returned as an error.
func objReadPushStream(r io.Reader) (digest, status string, err error) {
	dec := json.NewDecoder(r)
	for {
		var msg objPushMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return digest, status, fmt.Errorf("reading push stream: %w", err)
		}
		if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
			return digest, status, errors.New(msg.ErrorDetail.Message)
		}
		if msg.Error != "" {
			return digest, status, errors.New(msg.Error)
		}
		if len(msg.Aux) > 0 {
			var aux struct {
				Digest string `json:"Digest"`
			}
			if json.Unmarshal(msg.Aux, &aux) == nil && aux.Digest != "" {
				digest = aux.Digest
			}
		}
		if s := strings.TrimSpace(msg.Status); s != "" {
			status = s
			if mm := objPushDigestRe.FindStringSubmatch(s); mm != nil {
				digest = mm[1]
			}
		}
	}
	return digest, status, nil
}

func (a *objPluginPushAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m objPluginPushModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() || !a.pd.gateItemAPI(ctx, "engine:plugin-push", &resp.Diagnostics) {
		return
	}
	name := m.Name.ValueString()
	auth, err := a.pd.registryAuthFor(name)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid plugin name", err.Error())
		return
	}
	actProgress(ctx, resp, "pushing plugin %s", name)
	rc, err := c.PluginPush(ctx, name, client.PluginPushOptions{RegistryAuth: auth})
	if err != nil {
		resp.Diagnostics.AddError("docker_plugin_push failed", fmt.Sprintf("%s: %v", name, err))
		return
	}
	defer rc.Close()
	digest, status, err := objReadPushStream(rc)
	if err != nil {
		resp.Diagnostics.AddError("docker_plugin_push failed", fmt.Sprintf("%s: %v", name, err))
		return
	}
	actProgress(ctx, resp, "docker_plugin_push: pushed %s %s", name, digest)
	actFinishResult(ctx, resp, a.pd, "engine:plugin-push", map[string]any{
		"name":   name,
		"digest": digest,
		"result": map[string]any{"push_status": status},
	}, m.ResultPath)
}
