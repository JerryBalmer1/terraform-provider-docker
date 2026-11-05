package main

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

const (
	modeSwarm = "swarm"
	modeLocal = "local"
)

// Options control a conversion.
type Options struct {
	Mode     string
	Project  string
	Profiles []string
	// Env is the interpolation environment as KEY=VALUE pairs.
	Env []string
}

// Convert reads the compose file at in and returns formatted HCL.
func Convert(ctx context.Context, in string, o Options) ([]byte, error) {
	if o.Mode == "" {
		o.Mode = modeSwarm
	}
	if o.Mode != modeSwarm && o.Mode != modeLocal {
		return nil, fmt.Errorf("-mode must be %q or %q, got %q", modeSwarm, modeLocal, o.Mode)
	}
	p, err := load(ctx, in, o)
	if err != nil {
		return nil, err
	}
	c := &converter{p: p, dir: p.WorkingDir, mode: o.Mode}
	f := hclwrite.NewEmptyFile()
	body := f.Body()
	comment(body, "converted from "+filepath.ToSlash(in))
	comment(body, fmt.Sprintf("by tools/compose2stack -mode %s (project %q)", o.Mode, p.Name))
	body.AppendNewline()
	for _, name := range sortedKeys(p.DisabledServices) {
		comment(body, fmt.Sprintf("TODO: service %s is not converted: its profiles %v are not enabled (rerun with -profiles)", name, p.DisabledServices[name].Profiles))
	}
	if len(p.DisabledServices) > 0 {
		body.AppendNewline()
	}
	if o.Mode == modeSwarm {
		c.stack(body)
	} else {
		c.local(body)
	}
	for _, v := range c.variables {
		blk := body.AppendNewBlock("variable", []string{v})
		blk.Body().SetAttributeRaw("type", raw("string"))
		blk.Body().SetAttributeValue("sensitive", cty.True)
		blk.Body().SetAttributeValue("description", cty.StringVal("Value of the compose environment variable "+v+"."))
		body.AppendNewline()
	}
	out := hclwrite.Format(f.Bytes())
	return append([]byte(strings.TrimRight(string(out), "\n")), '\n'), nil
}

func load(ctx context.Context, in string, o Options) (*ctypes.Project, error) {
	abs, err := filepath.Abs(in)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range o.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	details := ctypes.ConfigDetails{WorkingDir: filepath.Dir(abs), ConfigFiles: []ctypes.ConfigFile{{Filename: abs}}, Environment: env}
	opt := func(name string) func(*loader.Options) {
		return func(lo *loader.Options) {
			if name != "" {
				lo.SetProjectName(name, true)
			}
			lo.Profiles = o.Profiles
			lo.ResolvePaths = true
		}
	}
	p, err := loader.LoadWithContext(ctx, details, opt(o.Project))
	if err != nil && o.Project == "" && strings.Contains(err.Error(), "project name must not be empty") {
		p, err = loader.LoadWithContext(ctx, details, opt(loader.NormalizeProjectName(filepath.Base(filepath.Dir(abs)))))
	}
	if err != nil {
		return nil, err
	}
	return p.WithoutUnnecessaryResources(), nil
}

// ---------------------------------------------------------------------------
// helpers

type converter struct {
	p         *ctypes.Project
	dir       string
	mode      string
	variables []string
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func comment(b *hclwrite.Body, text string) {
	b.AppendUnstructuredTokens(hclwrite.Tokens{{Type: hclsyntax.TokenComment, Bytes: []byte("# " + text + "\n")}})
}

func todo(b *hclwrite.Body, format string, args ...any) {
	comment(b, "TODO: "+fmt.Sprintf(format, args...))
}

// raw is an expression written verbatim.
func raw(expr string) hclwrite.Tokens {
	return hclwrite.Tokens{{Type: hclsyntax.TokenIdent, Bytes: []byte(expr)}}
}

var identRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// ident turns a compose key into a Terraform resource name.
func ident(s string) string {
	s = identRe.ReplaceAllString(s, "_")
	if s == "" || (s[0] >= '0' && s[0] <= '9') || s[0] == '-' {
		s = "_" + s
	}
	return s
}

func setStr(b *hclwrite.Body, k, v string) {
	if v != "" {
		b.SetAttributeValue(k, cty.StringVal(v))
	}
}

func setBool(b *hclwrite.Body, k string, v bool) {
	if v {
		b.SetAttributeValue(k, cty.True)
	}
}

func setInt(b *hclwrite.Body, k string, v int64) {
	if v != 0 {
		b.SetAttributeValue(k, cty.NumberIntVal(v))
	}
}

func setList(b *hclwrite.Body, k string, v []string) {
	if len(v) == 0 {
		return
	}
	vals := make([]cty.Value, len(v))
	for i, s := range v {
		vals[i] = cty.StringVal(s)
	}
	b.SetAttributeValue(k, cty.ListVal(vals))
}

func setMap(b *hclwrite.Body, k string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	vals := map[string]cty.Value{}
	for kk, v := range m {
		vals[kk] = cty.StringVal(v)
	}
	b.SetAttributeValue(k, cty.MapVal(vals))
}

func dur(d *ctypes.Duration) string {
	if d == nil {
		return ""
	}
	return time.Duration(*d).String()
}

// modulePath renders an absolute path inside the project directory as a
// ${path.module}-relative expression; paths outside it stay absolute.
func (c *converter) modulePath(p string, abs bool) hclwrite.Tokens {
	rel, err := filepath.Rel(c.dir, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return hclwrite.TokensForValue(cty.StringVal(filepath.ToSlash(p)))
	}
	expr := fmt.Sprintf(`"${path.module}/%s"`, filepath.ToSlash(rel))
	if rel == "." {
		expr = `path.module`
	}
	if abs {
		expr = "abspath(" + expr + ")"
	}
	return raw(expr)
}

func envMap(env ctypes.MappingWithEquals) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

func (c *converter) variable(name string) string {
	v := ident(name)
	for _, x := range c.variables {
		if x == v {
			return v
		}
	}
	c.variables = append(c.variables, v)
	return v
}

// blobData is the data expression of a secret/config (base64), or "" when
// it cannot be expressed.
func (c *converter) blobData(o ctypes.FileObjectConfig) hclwrite.Tokens {
	switch {
	case o.File != "":
		return hclwrite.TokensForFunctionCall("filebase64", c.modulePath(o.File, false))
	case o.Environment != "":
		return raw(fmt.Sprintf("base64encode(var.%s)", c.variable(o.Environment)))
	case o.Content != "":
		return hclwrite.TokensForFunctionCall("base64encode", hclwrite.TokensForValue(cty.StringVal(o.Content)))
	}
	return nil
}

func healthcheck(b *hclwrite.Body, h *ctypes.HealthCheckConfig) {
	if h == nil {
		return
	}
	hb := b.AppendNewBlock("healthcheck", nil).Body()
	if h.Disable {
		setList(hb, "test", []string{"NONE"})
		return
	}
	setList(hb, "test", []string(h.Test))
	setStr(hb, "interval", dur(h.Interval))
	setStr(hb, "timeout", dur(h.Timeout))
	if h.Retries != nil {
		setInt(hb, "retries", int64(*h.Retries))
	}
	setStr(hb, "start_period", dur(h.StartPeriod))
	if h.StartInterval != nil {
		todo(hb, "start_interval %s is not supported", dur(h.StartInterval))
	}
}

func ulimits(b *hclwrite.Body, u map[string]*ctypes.UlimitsConfig) {
	for _, name := range sortedKeys(u) {
		l := u[name]
		soft, hard := l.Soft, l.Hard
		if l.Single != 0 {
			soft, hard = l.Single, l.Single
		}
		ub := b.AppendNewBlock("ulimits", nil).Body()
		ub.SetAttributeValue("name", cty.StringVal(name))
		ub.SetAttributeValue("soft", cty.NumberIntVal(int64(soft)))
		ub.SetAttributeValue("hard", cty.NumberIntVal(int64(hard)))
	}
}

// commonTodos lists service attributes neither target supports.
func (c *converter) commonTodos(b *hclwrite.Body, s ctypes.ServiceConfig) {
	if s.Build != nil {
		ctxDir := s.Build.Context
		if rel, err := filepath.Rel(c.dir, ctxDir); err == nil && !strings.HasPrefix(rel, "..") {
			ctxDir = "./" + filepath.ToSlash(rel)
		}
		todo(b, "build (context %s) is not supported: build the image with docker_image and reference it", ctxDir)
	}
	if s.Develop != nil {
		todo(b, "develop is not supported")
	}
	if len(s.PostStart)+len(s.PreStop)+len(s.PreStart) > 0 {
		todo(b, "lifecycle hooks (pre_start/post_start/pre_stop) are not supported")
	}
	if s.Init != nil && *s.Init {
		todo(b, "init: true is not supported")
	}
	if len(s.GroupAdd) > 0 {
		todo(b, "group_add %v is not supported", s.GroupAdd)
	}
	if s.PidsLimit != 0 {
		todo(b, "pids_limit %d is not supported", s.PidsLimit)
	}
	if s.Platform != "" {
		todo(b, "platform %s is not supported", s.Platform)
	}
	if s.PullPolicy != "" && s.PullPolicy != ctypes.PullPolicyMissing {
		todo(b, "pull_policy %s is not supported", s.PullPolicy)
	}
	if len(s.Links) > 0 || len(s.ExternalLinks) > 0 {
		todo(b, "links/external_links are not supported; services reach each other by name on shared networks")
	}
}

func (c *converter) ports(s ctypes.ServiceConfig, b *hclwrite.Body, emit func(ctypes.ServicePortConfig, int64)) {
	for _, pc := range s.Ports {
		var pub int64
		if pc.Published != "" {
			n, err := strconv.ParseInt(pc.Published, 10, 32)
			if err != nil {
				todo(b, "published port range %s -> %d is not supported; list each port", pc.Published, pc.Target)
				continue
			}
			pub = n
		}
		emit(pc, pub)
	}
}

// ---------------------------------------------------------------------------
// swarm: one docker_stack

func (c *converter) stack(body *hclwrite.Body) {
	p := c.p
	blk := body.AppendNewBlock("resource", []string{"docker_stack", ident(p.Name)})
	b := blk.Body()
	b.SetAttributeValue("name", cty.StringVal(p.Name))

	for _, key := range sortedKeys(p.Networks) {
		n := p.Networks[key]
		if n.External {
			continue // services reference external networks by name
		}
		b.AppendNewline()
		nb := b.AppendNewBlock("network", nil).Body()
		nb.SetAttributeValue("name", cty.StringVal(key))
		if n.Name != p.Name+"_"+key {
			todo(nb, "compose names this network %q; docker_stack always names it %s_%s", n.Name, p.Name, key)
		}
		if n.Driver != "" && n.Driver != "overlay" {
			setStr(nb, "driver", n.Driver)
		}
		setBool(nb, "attachable", n.Attachable)
		setBool(nb, "internal", n.Internal)
		setMap(nb, "labels", n.Labels)
		setMap(nb, "options", n.DriverOpts)
		if len(n.Ipam.Config) > 0 || n.Ipam.Driver != "" {
			todo(nb, "ipam (driver %q, %d pools) is not supported by docker_stack networks; create the network with docker_network and mark it external", n.Ipam.Driver, len(n.Ipam.Config))
		}
		if n.EnableIPv6 != nil && *n.EnableIPv6 {
			todo(nb, "enable_ipv6 is not supported by docker_stack networks")
		}
	}
	for _, key := range sortedKeys(p.Volumes) {
		v := p.Volumes[key]
		if v.External {
			continue
		}
		b.AppendNewline()
		vb := b.AppendNewBlock("volume", nil).Body()
		vb.SetAttributeValue("name", cty.StringVal(key))
		if v.Name != p.Name+"_"+key {
			todo(vb, "compose names this volume %q; docker_stack always names it %s_%s", v.Name, p.Name, key)
		}
		setStr(vb, "driver", v.Driver)
		setMap(vb, "driver_opts", v.DriverOpts)
		setMap(vb, "labels", v.Labels)
	}
	blobs := func(kind string, objs map[string]ctypes.FileObjectConfig) {
		for _, key := range sortedKeys(objs) {
			o := objs[key]
			if o.External {
				continue
			}
			b.AppendNewline()
			bb := b.AppendNewBlock(kind, nil).Body()
			bb.SetAttributeValue("name", cty.StringVal(key))
			data := c.blobData(o)
			if data == nil {
				todo(bb, "%s %s has no file, content or environment source", kind, key)
				data = raw(`base64encode("")`)
			}
			bb.SetAttributeRaw("data", data)
			setMap(bb, "labels", o.Labels)
			if o.Driver != "" || o.TemplateDriver != "" {
				todo(bb, "driver/template_driver are not supported")
			}
		}
	}
	secrets := map[string]ctypes.FileObjectConfig{}
	for k, v := range p.Secrets {
		secrets[k] = ctypes.FileObjectConfig(v)
	}
	configs := map[string]ctypes.FileObjectConfig{}
	for k, v := range p.Configs {
		configs[k] = ctypes.FileObjectConfig(v)
	}
	blobs("secret", secrets)
	blobs("config", configs)

	for _, name := range sortedKeys(p.Services) {
		b.AppendNewline()
		c.stackService(b.AppendNewBlock("service", nil).Body(), p.Services[name])
	}
	body.AppendNewline()
}

// stackRef is how a stack service names a network/secret/config: the short
// key for objects the stack creates, the full name for external ones.
func stackRef(key, name string, external bool) string {
	if external {
		return name
	}
	return key
}

func (c *converter) stackService(b *hclwrite.Body, s ctypes.ServiceConfig) {
	p := c.p
	b.SetAttributeValue("name", cty.StringVal(s.Name))
	b.SetAttributeValue("image", cty.StringVal(s.Image))
	if s.Image == "" {
		todo(b, "no image: swarm services need an image from a registry")
	}
	setList(b, "command", []string(s.Entrypoint))
	setList(b, "args", []string(s.Command))
	setMap(b, "env", envMap(s.Environment))
	setStr(b, "hostname", s.Hostname)
	setStr(b, "user", s.User)
	setStr(b, "dir", s.WorkingDir)
	setMap(b, "container_labels", s.Labels)
	setMap(b, "sysctls", s.Sysctls)
	setStr(b, "stop_grace_period", dur(s.StopGracePeriod))
	if len(s.CapAdd) > 0 {
		setList(b, "cap_add", s.CapAdd)
	}
	if len(s.CapDrop) > 0 {
		setList(b, "cap_drop", s.CapDrop)
	}
	var hosts []string
	for _, h := range sortedKeys(s.ExtraHosts) {
		for _, ip := range s.ExtraHosts[h] {
			hosts = append(hosts, h+":"+ip)
		}
	}
	setList(b, "hosts", hosts)

	d := s.Deploy
	if d == nil {
		d = &ctypes.DeployConfig{}
	}
	switch d.Mode {
	case "global":
		b.SetAttributeValue("mode", cty.StringVal("global"))
	case "replicated-job":
		b.SetAttributeValue("mode", cty.StringVal("replicated_job"))
		setInt(b, "total_completions", int64(s.GetScale()))
		setInt(b, "max_concurrent", int64(s.GetScale()))
	case "global-job":
		b.SetAttributeValue("mode", cty.StringVal("global_job"))
	default:
		if n := s.GetScale(); n != 1 {
			b.SetAttributeValue("replicas", cty.NumberIntVal(int64(n)))
		}
	}
	setMap(b, "labels", d.Labels)

	var nets []string
	for _, key := range s.NetworksByPriority() {
		n := p.Networks[key]
		nets = append(nets, stackRef(key, n.Name, bool(n.External)))
		if nc := s.Networks[key]; nc != nil && (len(nc.Aliases) > 0 || nc.Ipv4Address != "" || nc.Ipv6Address != "") {
			todo(b, "network %s: aliases/static addresses %v %s %s are not converted (networks_advanced takes aliases)", key, nc.Aliases, nc.Ipv4Address, nc.Ipv6Address)
		}
	}
	if len(nets) > 0 {
		sort.Strings(nets)
		setList(b, "networks", nets)
	}

	// todos
	c.commonTodos(b, s)
	if len(s.DependsOn) > 0 {
		todo(b, "depends_on %v: swarm starts services independently; add healthchecks and retry logic instead", sortedKeys(s.DependsOn))
	}
	if s.ContainerName != "" {
		todo(b, "container_name %s is not supported in swarm (tasks are named by the service)", s.ContainerName)
	}
	for _, u := range []unsupported{
		{"network_mode", s.NetworkMode != ""}, {"privileged", s.Privileged}, {"devices", len(s.Devices) > 0}, {"ipc", s.Ipc != ""}, {"pid", s.Pid != ""},
		{"userns_mode", s.UserNSMode != ""}, {"security_opt", len(s.SecurityOpt) > 0}, {"shm_size", s.ShmSize != 0}, {"volumes_from", len(s.VolumesFrom) > 0},
		{"gpus", len(s.Gpus) > 0}, {"read_only", s.ReadOnly}, {"tty", s.Tty}, {"stdin_open", s.StdinOpen}, {"stop_signal", s.StopSignal != ""},
		{"expose", len(s.Expose) > 0}, {"tmpfs", len(s.Tmpfs) > 0}, {"cgroup_parent", s.CgroupParent != ""},
	} {
		if u.set {
			todo(b, "%s is not supported by docker_stack services", u.attr)
		}
	}

	if len(s.DNS)+len(s.DNSSearch)+len(s.DNSOpts) > 0 {
		db := b.AppendNewBlock("dns", nil).Body()
		setList(db, "nameservers", []string(s.DNS))
		setList(db, "search", []string(s.DNSSearch))
		setList(db, "options", s.DNSOpts)
	}
	healthcheck(b, s.HealthCheck)
	if s.Logging != nil && s.Logging.Driver != "" {
		lb := b.AppendNewBlock("logging", nil).Body()
		setStr(lb, "driver", s.Logging.Driver)
		setMap(lb, "options", s.Logging.Options)
	}

	for _, v := range s.Volumes {
		mb := b.AppendNewBlock("mounts", nil).Body()
		switch v.Type {
		case ctypes.VolumeTypeBind:
			mb.SetAttributeValue("type", cty.StringVal("bind"))
			mb.SetAttributeRaw("source", c.modulePath(v.Source, true))
		case ctypes.VolumeTypeTmpfs:
			mb.SetAttributeValue("type", cty.StringVal("tmpfs"))
			if v.Tmpfs != nil {
				setInt(mb, "tmpfs_size", int64(v.Tmpfs.Size))
			}
		case ctypes.VolumeTypeVolume, "":
			mb.SetAttributeValue("type", cty.StringVal("volume"))
			if v.Source != "" {
				vol := p.Volumes[v.Source]
				mb.SetAttributeValue("source", cty.StringVal(stackRef(v.Source, vol.Name, bool(vol.External))))
			}
		default:
			mb.SetAttributeValue("type", cty.StringVal("volume"))
			todo(mb, "mount type %s is not supported", v.Type)
		}
		mb.SetAttributeValue("target", cty.StringVal(v.Target))
		setBool(mb, "read_only", v.ReadOnly)
	}
	for _, ref := range s.Secrets {
		obj := p.Secrets[ref.Source]
		sb := b.AppendNewBlock("secrets", nil).Body()
		sb.SetAttributeValue("secret_id", cty.StringVal(stackRef(ref.Source, obj.Name, bool(obj.External))))
		target := strings.TrimPrefix(ref.Target, "/run/secrets/")
		if target != "" && target != ref.Source {
			sb.SetAttributeValue("file_name", cty.StringVal(target))
		}
		fileRef(sb, ctypes.FileReferenceConfig(ref))
	}
	for _, ref := range s.Configs {
		obj := p.Configs[ref.Source]
		cb := b.AppendNewBlock("configs", nil).Body()
		cb.SetAttributeValue("config_id", cty.StringVal(stackRef(ref.Source, obj.Name, bool(obj.External))))
		target := ref.Target
		if target == "" {
			target = "/" + ref.Source
		}
		cb.SetAttributeValue("file_name", cty.StringVal(target))
		fileRef(cb, ctypes.FileReferenceConfig(ref))
	}
	ulimits(b, s.Ulimits)

	if pl := d.Placement; len(pl.Constraints)+len(pl.Preferences) > 0 || pl.MaxReplicas > 0 {
		pb := b.AppendNewBlock("placement", nil).Body()
		setList(pb, "constraints", pl.Constraints)
		var prefs []string
		for _, pp := range pl.Preferences {
			prefs = append(prefs, "spread="+pp.Spread)
		}
		setList(pb, "preferences", prefs)
		setInt(pb, "max_replicas", int64(pl.MaxReplicas))
	}
	lim, res := d.Resources.Limits, d.Resources.Reservations
	limCPU, limMem := int64(0), int64(s.MemLimit)
	if lim != nil {
		limCPU, limMem = int64(float64(lim.NanoCPUs)*1e9), int64(lim.MemoryBytes)
	}
	if limCPU == 0 {
		limCPU = int64(float64(s.CPUS) * 1e9)
	}
	if limCPU != 0 || limMem != 0 || res != nil {
		rb := b.AppendNewBlock("resources", nil).Body()
		if limCPU != 0 || limMem != 0 {
			lb := rb.AppendNewBlock("limits", nil).Body()
			setInt(lb, "nano_cpus", limCPU)
			setInt(lb, "memory_bytes", limMem)
		}
		if res != nil {
			lb := rb.AppendNewBlock("reservations", nil).Body()
			setInt(lb, "nano_cpus", int64(float64(res.NanoCPUs)*1e9))
			setInt(lb, "memory_bytes", int64(res.MemoryBytes))
			if len(res.Devices)+len(res.GenericResources) > 0 {
				todo(lb, "reservation devices/generic_resources are not supported")
			}
		}
	}
	if rp := d.RestartPolicy; rp != nil {
		rb := b.AppendNewBlock("restart_policy", nil).Body()
		setStr(rb, "condition", rp.Condition)
		setStr(rb, "delay", dur(rp.Delay))
		if rp.MaxAttempts != nil {
			setInt(rb, "max_attempts", int64(*rp.MaxAttempts))
		}
		setStr(rb, "window", dur(rp.Window))
	} else if s.Restart != "" {
		name, retries, _ := strings.Cut(s.Restart, ":")
		cond := map[string]string{"no": "none", "always": "any", "unless-stopped": "any", "on-failure": "on-failure"}[name]
		rb := b.AppendNewBlock("restart_policy", nil).Body()
		setStr(rb, "condition", cond)
		if n, err := strconv.ParseInt(retries, 10, 64); err == nil {
			setInt(rb, "max_attempts", n)
		}
	}
	update := func(kind string, u *ctypes.UpdateConfig) {
		if u == nil {
			return
		}
		ub := b.AppendNewBlock(kind, nil).Body()
		if u.Parallelism != nil {
			ub.SetAttributeValue("parallelism", cty.NumberUIntVal(*u.Parallelism))
		}
		if u.Delay != 0 {
			setStr(ub, "delay", time.Duration(u.Delay).String())
		}
		setStr(ub, "failure_action", u.FailureAction)
		if u.Monitor != 0 {
			setStr(ub, "monitor", time.Duration(u.Monitor).String())
		}
		if u.MaxFailureRatio != 0 {
			ub.SetAttributeValue("max_failure_ratio", cty.NumberFloatVal(float64(u.MaxFailureRatio)))
		}
		setStr(ub, "order", u.Order)
	}
	update("update_config", d.UpdateConfig)
	update("rollback_config", d.RollbackConfig)

	if len(s.Ports) > 0 || d.EndpointMode != "" {
		eb := b.AppendNewBlock("endpoint_spec", nil).Body()
		setStr(eb, "mode", d.EndpointMode)
		c.ports(s, eb, func(pc ctypes.ServicePortConfig, pub int64) {
			pb := eb.AppendNewBlock("ports", nil).Body()
			pb.SetAttributeValue("target_port", cty.NumberIntVal(int64(pc.Target)))
			setInt(pb, "published_port", pub)
			if pc.Protocol != "" && pc.Protocol != "tcp" {
				setStr(pb, "protocol", pc.Protocol)
			}
			if pc.Mode == "host" {
				setStr(pb, "publish_mode", "host")
			}
			if pc.HostIP != "" {
				todo(pb, "host_ip %s is not supported in swarm", pc.HostIP)
			}
		})
	}
}

func fileRef(b *hclwrite.Body, ref ctypes.FileReferenceConfig) {
	setStr(b, "uid", ref.UID)
	setStr(b, "gid", ref.GID)
	if ref.Mode != nil {
		b.SetAttributeValue("mode", cty.NumberIntVal(int64(*ref.Mode)))
	}
}

// ---------------------------------------------------------------------------
// local: docker_network, docker_volume, docker_container

func (c *converter) local(body *hclwrite.Body) {
	p := c.p
	for _, key := range sortedKeys(p.Networks) {
		n := p.Networks[key]
		if n.External {
			continue
		}
		b := body.AppendNewBlock("resource", []string{"docker_network", ident(key)}).Body()
		b.SetAttributeValue("name", cty.StringVal(n.Name))
		setStr(b, "driver", n.Driver)
		setBool(b, "internal", n.Internal)
		setBool(b, "attachable", n.Attachable)
		if n.EnableIPv6 != nil && *n.EnableIPv6 {
			b.SetAttributeValue("ipv6", cty.True)
		}
		setStr(b, "ipam_driver", n.Ipam.Driver)
		setMap(b, "labels", mergeMaps(n.Labels, map[string]string{"com.docker.compose.project": p.Name, "com.docker.compose.network": key}))
		setMap(b, "options", n.DriverOpts)
		for _, pool := range n.Ipam.Config {
			if pool == nil {
				continue
			}
			ib := b.AppendNewBlock("ipam_config", nil).Body()
			setStr(ib, "subnet", pool.Subnet)
			setStr(ib, "gateway", pool.Gateway)
			setStr(ib, "ip_range", pool.IPRange)
			setMap(ib, "aux_address", pool.AuxiliaryAddresses)
		}
		body.AppendNewline()
	}
	for _, key := range sortedKeys(p.Volumes) {
		v := p.Volumes[key]
		if v.External {
			continue
		}
		b := body.AppendNewBlock("resource", []string{"docker_volume", ident(key)}).Body()
		b.SetAttributeValue("name", cty.StringVal(v.Name))
		setStr(b, "driver", v.Driver)
		setMap(b, "driver_opts", v.DriverOpts)
		setMap(b, "labels", mergeMaps(v.Labels, map[string]string{"com.docker.compose.project": p.Name, "com.docker.compose.volume": key}))
		body.AppendNewline()
	}
	for _, name := range sortedKeys(p.Services) {
		c.container(body.AppendNewBlock("resource", []string{"docker_container", ident(name)}).Body(), p.Services[name])
		body.AppendNewline()
	}
}

func mergeMaps(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func (c *converter) container(b *hclwrite.Body, s ctypes.ServiceConfig) {
	p := c.p
	scale := s.GetScale()
	if s.Deploy != nil && (s.Deploy.Mode == "global" || s.Deploy.Mode == "global-job") {
		scale = 1
	}
	number := "1"
	if scale > 1 && s.ContainerName == "" {
		b.SetAttributeValue("count", cty.NumberIntVal(int64(scale)))
		b.SetAttributeRaw("name", raw(fmt.Sprintf(`"%s-%s-${count.index + 1}"`, p.Name, s.Name)))
		number = "${count.index + 1}"
	} else if s.ContainerName != "" {
		b.SetAttributeValue("name", cty.StringVal(s.ContainerName))
		if scale > 1 {
			todo(b, "container_name with %d replicas is not possible; one container is created", scale)
		}
	} else {
		b.SetAttributeValue("name", cty.StringVal(fmt.Sprintf("%s-%s-1", p.Name, s.Name)))
	}
	b.SetAttributeValue("image", cty.StringVal(s.Image))
	if s.Image == "" {
		todo(b, "no image: build it with docker_image and set image")
	}
	setList(b, "command", []string(s.Command))
	setList(b, "entrypoint", []string(s.Entrypoint))
	setMap(b, "env", envMap(s.Environment))
	labels := mergeMaps(s.Labels, map[string]string{"com.docker.compose.project": p.Name, "com.docker.compose.service": s.Name})
	b.SetAttributeRaw("labels", labelsTokens(labels, number))
	setStr(b, "hostname", s.Hostname)
	setStr(b, "user", s.User)
	setStr(b, "working_dir", s.WorkingDir)
	setBool(b, "privileged", s.Privileged)
	setBool(b, "read_only", s.ReadOnly)
	setBool(b, "tty", s.Tty)
	setBool(b, "stdin_open", s.StdinOpen)
	setStr(b, "stop_signal", s.StopSignal)
	if s.StopGracePeriod != nil {
		setInt(b, "stop_timeout", int64(time.Duration(*s.StopGracePeriod).Seconds()))
	}
	restart := s.Restart
	if restart == "" && s.Deploy != nil && s.Deploy.RestartPolicy != nil {
		restart = map[string]string{"none": "no", "on-failure": "on-failure", "any": "always", "": "always"}[s.Deploy.RestartPolicy.Condition]
	}
	if restart != "" {
		name, retries, _ := strings.Cut(restart, ":")
		setStr(b, "restart", name)
		if n, err := strconv.ParseInt(retries, 10, 64); err == nil {
			setInt(b, "max_retry_count", n)
		}
	}
	mem, cpus := int64(s.MemLimit), int64(float64(s.CPUS)*1e9)
	if s.Deploy != nil && s.Deploy.Resources.Limits != nil {
		if mem == 0 {
			mem = int64(s.Deploy.Resources.Limits.MemoryBytes)
		}
		if cpus == 0 {
			cpus = int64(float64(s.Deploy.Resources.Limits.NanoCPUs) * 1e9)
		}
	}
	setInt(b, "memory", mem)
	setInt(b, "memory_swap", int64(s.MemSwapLimit))
	setInt(b, "nano_cpus", cpus)
	setInt(b, "cpu_shares", s.CPUShares)
	setList(b, "dns", []string(s.DNS))
	setList(b, "dns_search", []string(s.DNSSearch))
	var hosts []string
	for _, h := range sortedKeys(s.ExtraHosts) {
		for _, ip := range s.ExtraHosts[h] {
			hosts = append(hosts, h+":"+ip)
		}
	}
	setList(b, "extra_hosts", hosts)
	setMap(b, "sysctls", s.Sysctls)
	if len(s.SecurityOpt) > 0 {
		vals := make([]cty.Value, len(s.SecurityOpt))
		for i, o := range s.SecurityOpt {
			vals[i] = cty.StringVal(o)
		}
		b.SetAttributeValue("security_opt", cty.SetVal(vals))
	}
	if s.Logging != nil && s.Logging.Driver != "" {
		setStr(b, "log_driver", s.Logging.Driver)
		setMap(b, "log_opts", s.Logging.Options)
	}
	if len(s.Gpus) > 0 {
		b.SetAttributeValue("gpus", cty.StringVal("all"))
		todo(b, "gpus converted to \"all\"; check the device selection")
	}

	var deps []string
	for _, d := range sortedKeys(s.DependsOn) {
		if _, ok := p.Services[d]; ok {
			deps = append(deps, "docker_container."+ident(d))
			if cond := s.DependsOn[d].Condition; cond != "" && cond != ctypes.ServiceConditionStarted {
				todo(b, "depends_on %s condition %s: Terraform only orders creation (docker_container waits for its own healthcheck when wait = true)", d, cond)
			}
		}
	}
	if len(deps) > 0 {
		b.SetAttributeRaw("depends_on", raw("["+strings.Join(deps, ", ")+"]"))
	}

	c.commonTodos(b, s)
	if s.NetworkMode != "" {
		todo(b, "network_mode %s is not supported by docker_container", s.NetworkMode)
	}
	for _, u := range []unsupported{
		{"ipc", s.Ipc != ""}, {"pid", s.Pid != ""}, {"userns_mode", s.UserNSMode != ""}, {"volumes_from", len(s.VolumesFrom) > 0},
		{"expose", len(s.Expose) > 0}, {"shm_size", s.ShmSize != 0}, {"cgroup_parent", s.CgroupParent != ""}, {"dns_opt", len(s.DNSOpts) > 0}, {"domainname", s.DomainName != ""},
		{"mem_reservation", s.MemReservation != 0}, {"oom_score_adj", s.OomScoreAdj != 0}, {"runtime", s.Runtime != ""}, {"device_cgroup_rules", len(s.DeviceCgroupRules) > 0},
	} {
		if u.set {
			todo(b, "%s is not supported by docker_container", u.attr)
		}
	}
	if s.Deploy != nil {
		if pl := s.Deploy.Placement; len(pl.Constraints)+len(pl.Preferences) > 0 {
			todo(b, "deploy.placement only applies to swarm (use -mode swarm)")
		}
		if s.Deploy.UpdateConfig != nil || s.Deploy.RollbackConfig != nil {
			todo(b, "deploy.update_config/rollback_config only apply to swarm")
		}
	}

	if len(s.CapAdd)+len(s.CapDrop) > 0 {
		cb := b.AppendNewBlock("capabilities", nil).Body()
		for _, kv := range []struct {
			k string
			v []string
		}{{"add", s.CapAdd}, {"drop", s.CapDrop}} {
			k, v := kv.k, kv.v
			if len(v) > 0 {
				vals := make([]cty.Value, len(v))
				for i, x := range v {
					vals[i] = cty.StringVal(x)
				}
				cb.SetAttributeValue(k, cty.SetVal(vals))
			}
		}
	}
	for _, d := range s.Devices {
		db := b.AppendNewBlock("devices", nil).Body()
		db.SetAttributeValue("host_path", cty.StringVal(d.Source))
		if d.Target != "" && d.Target != d.Source {
			setStr(db, "container_path", d.Target)
		}
		setStr(db, "permissions", d.Permissions)
	}
	healthcheck(b, s.HealthCheck)

	for _, v := range s.Volumes {
		mb := b.AppendNewBlock("mounts", nil).Body()
		switch v.Type {
		case ctypes.VolumeTypeBind:
			mb.SetAttributeValue("type", cty.StringVal("bind"))
			mb.SetAttributeRaw("source", c.modulePath(v.Source, true))
		case ctypes.VolumeTypeTmpfs:
			mb.SetAttributeValue("type", cty.StringVal("tmpfs"))
			if v.Tmpfs != nil {
				setInt(mb, "tmpfs_size", int64(v.Tmpfs.Size))
			}
		case ctypes.VolumeTypeVolume, "":
			mb.SetAttributeValue("type", cty.StringVal("volume"))
			if v.Source != "" {
				vol := p.Volumes[v.Source]
				if vol.External {
					mb.SetAttributeValue("source", cty.StringVal(vol.Name))
				} else {
					mb.SetAttributeRaw("source", raw("docker_volume."+ident(v.Source)+".name"))
				}
			}
		default:
			mb.SetAttributeValue("type", cty.StringVal("volume"))
			todo(mb, "mount type %s is not supported", v.Type)
		}
		mb.SetAttributeValue("target", cty.StringVal(v.Target))
		setBool(mb, "read_only", v.ReadOnly)
	}
	for _, t := range s.Tmpfs {
		target, _, _ := strings.Cut(t, ":")
		mb := b.AppendNewBlock("mounts", nil).Body()
		mb.SetAttributeValue("type", cty.StringVal("tmpfs"))
		mb.SetAttributeValue("target", cty.StringVal(target))
	}
	fileMount := func(kind, key string, obj ctypes.FileObjectConfig, target string) {
		if obj.File == "" {
			todo(b, "%s %s (from %s) cannot be mounted by docker_container: only file-backed %ss can (or use docker_compose / -mode swarm)", kind, key, blobSource(obj), kind)
			return
		}
		mb := b.AppendNewBlock("mounts", nil).Body()
		mb.SetAttributeValue("type", cty.StringVal("bind"))
		mb.SetAttributeRaw("source", c.modulePath(obj.File, true))
		mb.SetAttributeValue("target", cty.StringVal(target))
		mb.SetAttributeValue("read_only", cty.True)
	}
	for _, ref := range s.Secrets {
		target := ref.Target
		if target == "" {
			target = "/run/secrets/" + ref.Source
		}
		fileMount("secret", ref.Source, ctypes.FileObjectConfig(p.Secrets[ref.Source]), target)
	}
	for _, ref := range s.Configs {
		target := ref.Target
		if target == "" {
			target = "/" + ref.Source
		}
		fileMount("config", ref.Source, ctypes.FileObjectConfig(p.Configs[ref.Source]), target)
	}

	for _, key := range s.NetworksByPriority() {
		n := p.Networks[key]
		nb := b.AppendNewBlock("networks_advanced", nil).Body()
		if n.External {
			nb.SetAttributeValue("name", cty.StringVal(n.Name))
		} else {
			nb.SetAttributeRaw("name", raw("docker_network."+ident(key)+".name"))
		}
		aliases := []string{s.Name}
		if nc := s.Networks[key]; nc != nil {
			aliases = append(aliases, nc.Aliases...)
			setStr(nb, "ipv4_address", nc.Ipv4Address)
			setStr(nb, "ipv6_address", nc.Ipv6Address)
		}
		vals := make([]cty.Value, len(aliases))
		for i, a := range aliases {
			vals[i] = cty.StringVal(a)
		}
		nb.SetAttributeValue("aliases", cty.SetVal(vals))
	}
	c.ports(s, b, func(pc ctypes.ServicePortConfig, pub int64) {
		pb := b.AppendNewBlock("ports", nil).Body()
		pb.SetAttributeValue("internal", cty.NumberIntVal(int64(pc.Target)))
		setInt(pb, "external", pub)
		setStr(pb, "ip", pc.HostIP)
		if pc.Protocol != "" && pc.Protocol != "tcp" {
			setStr(pb, "protocol", pc.Protocol)
		}
	})
	ulimits(b, s.Ulimits)
}

func blobSource(o ctypes.FileObjectConfig) string {
	switch {
	case bool(o.External):
		return "external"
	case o.Environment != "":
		return "environment " + o.Environment
	case o.Content != "":
		return "content"
	}
	return "unknown source"
}

// labelsTokens renders a label map; the container-number label may be an
// interpolation of count.index.
func labelsTokens(labels map[string]string, number string) hclwrite.Tokens {
	keys := sortedKeys(labels)
	all := append(keys, "com.docker.compose.container-number")
	sort.Strings(all)
	var sb strings.Builder
	sb.WriteString("{\n")
	for _, k := range all {
		v := labels[k]
		var val string
		if k == "com.docker.compose.container-number" {
			val = `"` + number + `"`
		} else {
			val = string(hclwrite.TokensForValue(cty.StringVal(v)).Bytes())
		}
		fmt.Fprintf(&sb, "%s = %s\n", string(hclwrite.TokensForValue(cty.StringVal(k)).Bytes()), val)
	}
	sb.WriteString("}")
	return raw(sb.String())
}

// unsupported pairs a compose attribute with whether the service sets it.
type unsupported struct {
	attr string
	set  bool
}
