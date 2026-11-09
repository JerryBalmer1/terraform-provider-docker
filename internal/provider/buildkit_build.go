package provider

// Pass 5 Lane E: the BuildKit build seam (seamBuildKitImpl). One build is
// one BuildKit Solve with the dockerfile.v0 frontend, the way `docker
// buildx build` drives it:
//   - the builder is the daemon's embedded BuildKit (reached by POST /grpc
//     and /session, buildx's `docker` driver) or a docker-container /
//     remote builder (buildkit_conn.go);
//   - the context and Dockerfile directories are served to BuildKit over
//     the session (filesync), so .dockerignore is applied by the frontend;
//   - secrets, SSH agents and registry_auth credentials are session
//     attachables;
//   - exports follow buildx: the docker driver loads through the `moby`
//     exporter; other drivers load by streaming the `docker` exporter's
//     tarball into POST /images/load; push uses the image exporter;
//     `--output` specs are passed through (local, tar, oci, docker,
//     image, registry, cacheonly);
//   - the result metadata is buildx's --metadata-file object.

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/tonistiigi/fsutil"
)

func init() { seamBuildKitImpl = laneEBuildKit{} }

// laneEBuildKit implements seamBuildKit.
type laneEBuildKit struct{}

// bkFrontend is the Dockerfile frontend buildx uses.
const bkFrontend = "dockerfile.v0"

// bkTailLines is how many build log lines an error carries.
const bkTailLines = 20

// bkDrainJSONStream reads an Engine JSON message stream (image load) to
// the end and returns the first error message in it.
func bkDrainJSONStream(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var m struct {
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decoding daemon output: %w", err)
		}
		if m.ErrorDetail != nil && m.ErrorDetail.Message != "" {
			return errors.New(m.ErrorDetail.Message)
		}
		if m.Error != "" {
			return errors.New(m.Error)
		}
	}
}

// Build runs one build (see the file comment).
func (laneEBuildKit) Build(ctx context.Context, pd *providerData, req seamBuildRequest) (*seamBuildResult, error) {
	conn, err := bkConnect(ctx, pd, req.Builder)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return bkBuild(ctx, pd, conn, req, &bkExtra{Contexts: req.Contexts, Annotations: req.Annotations, Call: req.Call, Entitlements: req.Entitlements})
}

// bkExtra carries the build options that sit beside the main SolveOpt fields
// (named contexts, annotations, call, entitlements). The seam fills it from
// seamBuildRequest; bake fills it from a target.
type bkExtra struct {
	// Contexts are named build contexts (--build-context name=value):
	// a local directory, docker-image://, a git or HTTP(S) URL.
	Contexts map[string]string
	// Annotations are --annotation values ([type:]key=value).
	Annotations []string
	// Call is the frontend method (--call): build (default), check, outline, targets.
	Call string
	// Entitlements are extra allowed entitlements (network.host, security.insecure).
	Entitlements []string
}

// bkPrepared is a build turned into SolveOpt parts.
type bkPrepared struct {
	opt     bkclient.SolveOpt
	cleanup []func()
	// loads are background image loads fed by docker exporters.
	loads []*bkLoad
	// dockerfileText / syntax are what the frontend will read.
	dockerfileText string
}

func (p *bkPrepared) close() {
	for i := len(p.cleanup) - 1; i >= 0; i-- {
		p.cleanup[i]()
	}
}

// bkBuild runs req on an open connection.
func bkBuild(ctx context.Context, pd *providerData, conn *bkConn, req seamBuildRequest, extra *bkExtra) (*seamBuildResult, error) {
	prep, err := bkPrepare(ctx, pd, conn, req, extra)
	if prep != nil {
		defer prep.close()
	}
	if err != nil {
		return nil, err
	}
	prog := newBKProgress(req.Progress)
	ch := make(chan *bkclient.SolveStatus)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for st := range ch {
			prog.add(st)
		}
	}()
	tflog.Info(ctx, "BuildKit build", map[string]any{"builder": conn.Node.Builder, "node": conn.Node.Name, "ref": prep.opt.Ref, "platforms": req.Platforms, "exports": len(prep.opt.Exports)})
	resp, err := conn.Client.Solve(ctx, nil, prep.opt, ch)
	<-done
	prog.flush()
	for _, l := range prep.loads {
		if lerr := l.wait(); lerr != nil && err == nil {
			err = fmt.Errorf("loading the built image into the daemon: %w", lerr)
		}
	}
	if err != nil {
		return &seamBuildResult{BuildLog: prog.log()}, prog.wrap(err)
	}
	res := &seamBuildResult{BuildLog: prog.log(), Platforms: append([]string(nil), req.Platforms...)}
	res.Metadata = bkMetadata(resp.ExporterResponse, conn.Node, prep.opt.Ref, prog.warnings)
	res.ImageID = resp.ExporterResponse["containerimage.config.digest"]
	res.Digest = resp.ExporterResponse["containerimage.digest"]
	if res.ImageID == "" {
		res.ImageID = res.Digest
	}
	if req.Outputs.Push && res.Digest != "" && len(req.Outputs.Names) > 0 {
		res.ImageDigestRef = bkDigestRef(req.Outputs.Names[0], res.Digest)
	}
	return res, nil
}

// bkDigestRef turns repo:tag + digest into repo@digest.
func bkDigestRef(name, dgst string) string {
	repo := name
	if i := strings.LastIndex(repo, "@"); i >= 0 {
		repo = repo[:i]
	}
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	return repo + "@" + dgst
}

// bkPrepare builds the SolveOpt for req.
func bkPrepare(ctx context.Context, pd *providerData, conn *bkConn, req seamBuildRequest, extra *bkExtra) (*bkPrepared, error) {
	p := &bkPrepared{}
	if extra == nil {
		extra = &bkExtra{}
	}
	attrs, err := bkFrontendAttrs(req)
	if err != nil {
		return p, err
	}
	p.opt = bkclient.SolveOpt{
		Frontend:      bkFrontend,
		FrontendAttrs: attrs,
		LocalMounts:   map[string]fsutil.FS{},
		Ref:           identity.NewID(),
	}
	// Context.
	contextDir := req.ContextDir
	remote := bkRemoteContext(contextDir)
	switch {
	case req.ContextTar != nil:
		dir, err := os.MkdirTemp("", "tpd-buildctx-")
		if err != nil {
			return p, err
		}
		p.cleanup = append(p.cleanup, func() { _ = os.RemoveAll(dir) })
		if err := bkExtractTar(req.ContextTar, dir); err != nil {
			return p, fmt.Errorf("unpacking the build context: %w", err)
		}
		contextDir = dir
	case remote:
		attrs["context"] = contextDir
	case contextDir == "":
		return p, errors.New("build context is empty: set a context directory")
	}
	if !remote {
		abs, err := filepath.Abs(expandHome(contextDir))
		if err != nil {
			return p, err
		}
		fs, err := fsutil.NewFS(abs)
		if err != nil {
			return p, fmt.Errorf("build context %s: %w", abs, err)
		}
		p.opt.LocalMounts["context"] = fs
		p.opt.SharedKey = imgBuildKitSharedKey(abs)
		contextDir = abs
	}
	// Dockerfile.
	switch {
	case req.DockerfileInline != "":
		dir, err := os.MkdirTemp("", "tpd-dockerfile-")
		if err != nil {
			return p, err
		}
		p.cleanup = append(p.cleanup, func() { _ = os.RemoveAll(dir) })
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(req.DockerfileInline), 0o600); err != nil {
			return p, err
		}
		fs, err := fsutil.NewFS(dir)
		if err != nil {
			return p, err
		}
		p.opt.LocalMounts["dockerfile"] = fs
		attrs["filename"] = "Dockerfile"
		p.dockerfileText = req.DockerfileInline
	case remote && req.Dockerfile == "":
		attrs["filename"] = "Dockerfile"
	case remote:
		attrs["filename"] = req.Dockerfile
	default:
		df := bkDockerfilePath(contextDir, req.Dockerfile)
		fs, err := fsutil.NewFS(filepath.Dir(df))
		if err != nil {
			return p, fmt.Errorf("dockerfile directory: %w", err)
		}
		p.opt.LocalMounts["dockerfile"] = fs
		attrs["filename"] = filepath.Base(df)
		if b, err := os.ReadFile(df); err == nil {
			p.dockerfileText = string(b)
		} else {
			return p, fmt.Errorf("reading Dockerfile: %w", err)
		}
	}
	// Session.
	var creds []registryCredential
	if pd != nil {
		creds = pd.RegistryAuth
	}
	att, err := bkAttachables(req, creds)
	if err != nil {
		return p, err
	}
	p.opt.Session = att
	// Caches.
	for _, s := range req.CacheFrom {
		e, err := bkParseCacheSpec(s)
		if err != nil {
			return p, fmt.Errorf("cache_from %q: %w", s, err)
		}
		p.opt.CacheImports = append(p.opt.CacheImports, e)
	}
	for _, s := range req.CacheTo {
		e, err := bkParseCacheSpec(s)
		if err != nil {
			return p, fmt.Errorf("cache_to %q: %w", s, err)
		}
		p.opt.CacheExports = append(p.opt.CacheExports, e)
	}
	if req.Network == "host" {
		p.opt.AllowedEntitlements = append(p.opt.AllowedEntitlements, "network.host")
	}
	// Exports.
	exps, loads, err := bkExports(ctx, pd, conn, req.Outputs)
	if err != nil {
		return p, err
	}
	p.opt.Exports = exps
	p.loads = loads
	if err := bkApplyExtra(p, extra); err != nil {
		return p, err
	}
	return p, nil
}

// bkApplyExtra adds named contexts, annotations, the call method and
// entitlements to a prepared build.
func bkApplyExtra(p *bkPrepared, extra *bkExtra) error {
	attrs := p.opt.FrontendAttrs
	names := make([]string, 0, len(extra.Contexts))
	for k := range extra.Contexts {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		v := extra.Contexts[name]
		switch {
		case strings.HasPrefix(v, "docker-image://") || bkRemoteContext(v):
			attrs["context:"+name] = v
		case strings.HasPrefix(v, "target:"):
			return fmt.Errorf("named context %s=%s: target: contexts are not supported; build that target and refer to its image with docker-image://", name, v)
		case strings.Contains(v, "://"):
			return fmt.Errorf("named context %s=%s: unsupported scheme (use a local directory, docker-image://, git or https)", name, v)
		default:
			abs, err := filepath.Abs(expandHome(v))
			if err != nil {
				return err
			}
			fs, err := fsutil.NewFS(abs)
			if err != nil {
				return fmt.Errorf("named context %s: %w", name, err)
			}
			p.opt.LocalMounts[name] = fs
			attrs["context:"+name] = "local:" + name
		}
	}
	for _, a := range extra.Annotations {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return fmt.Errorf("annotation %q: expected [type:]key=value", a)
		}
		key := "annotation." + k
		if typ, kk, ok := strings.Cut(k, ":"); ok && !strings.Contains(typ, ".") {
			key = "annotation-" + typ + "." + kk
		}
		for i := range p.opt.Exports {
			switch p.opt.Exports[i].Type {
			case bkclient.ExporterImage, bkclient.ExporterOCI, bkclient.ExporterDocker, "moby":
				if p.opt.Exports[i].Attrs == nil {
					p.opt.Exports[i].Attrs = map[string]string{}
				}
				p.opt.Exports[i].Attrs[key] = v
			}
		}
	}
	switch extra.Call {
	case "", "build":
	default:
		attrs["requestid"] = "frontend." + extra.Call
		p.opt.Exports = nil
	}
	p.opt.AllowedEntitlements = bakeDedupe(append(p.opt.AllowedEntitlements, extra.Entitlements...))
	return nil
}

// bkRemoteContext reports a git or HTTP(S) context.
func bkRemoteContext(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "git://") ||
		strings.HasPrefix(s, "git@") || strings.HasPrefix(s, "github.com/")
}

// bkDockerfilePath resolves the Dockerfile against the context.
func bkDockerfilePath(contextDir, df string) string {
	if df == "" {
		df = "Dockerfile"
	}
	df = expandHome(df)
	if filepath.IsAbs(df) {
		return df
	}
	return filepath.Join(contextDir, df)
}

// bkFrontendAttrs maps the request onto dockerfile.v0 frontend options
// (the keys buildx sends).
func bkFrontendAttrs(req seamBuildRequest) (map[string]string, error) {
	a := map[string]string{}
	if req.Target != "" {
		a["target"] = req.Target
	}
	if len(req.Platforms) > 0 {
		a["platform"] = strings.Join(req.Platforms, ",")
	}
	for k, v := range req.BuildArgs {
		a["build-arg:"+k] = v
	}
	for k, v := range req.Labels {
		a["label:"+k] = v
	}
	if req.NoCache {
		a["no-cache"] = ""
	} else if len(req.NoCacheFilter) > 0 {
		a["no-cache"] = strings.Join(req.NoCacheFilter, ",")
	}
	if req.Pull {
		a["image-resolve-mode"] = "pull"
	}
	if len(req.ExtraHosts) > 0 {
		hs := make([]string, 0, len(req.ExtraHosts))
		for _, h := range req.ExtraHosts {
			// buildx accepts host:ip and host=ip; the frontend wants host=ip.
			if !strings.Contains(h, "=") {
				if i := strings.Index(h, ":"); i > 0 {
					h = h[:i] + "=" + h[i+1:]
				}
			}
			hs = append(hs, h)
		}
		a["add-hosts"] = strings.Join(hs, ",")
	}
	if req.ShmSize > 0 {
		a["shm-size"] = strconv.FormatInt(req.ShmSize, 10)
	}
	if len(req.Ulimits) > 0 {
		a["ulimit"] = strings.Join(req.Ulimits, ",")
	}
	switch req.Network {
	case "", "default":
	case "none", "host":
		a["force-network-mode"] = req.Network
	default:
		return nil, fmt.Errorf("network %q: BuildKit supports default, none and host", req.Network)
	}
	switch req.Attestations.Provenance {
	case "", "false":
	case "min", "max":
		a["attest:provenance"] = "mode=" + req.Attestations.Provenance
	case "true":
		a["attest:provenance"] = "mode=min"
	default:
		a["attest:provenance"] = req.Attestations.Provenance
	}
	if req.Attestations.SBOM {
		a["attest:sbom"] = ""
	}
	for _, s := range req.Attestations.Extra {
		kv, err := bkParseCSV(s)
		if err != nil {
			return nil, fmt.Errorf("attestation %q: %w", s, err)
		}
		typ := kv["type"]
		if typ == "" {
			return nil, fmt.Errorf("attestation %q: type is required", s)
		}
		delete(kv, "type")
		if v, ok := kv["disabled"]; ok && v == "true" {
			delete(a, "attest:"+typ)
			continue
		}
		a["attest:"+typ] = bkJoinCSV(kv)
	}
	return a, nil
}

// bkParseCSV parses buildx key=value,key=value specs (CSV quoting).
func bkParseCSV(s string) (map[string]string, error) {
	r := csv.NewReader(strings.NewReader(s))
	fields, err := r.Read()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("invalid value %q: expected key=value", f)
		}
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out, nil
}

// bkJoinCSV prints a map as sorted key=value pairs.
func bkJoinCSV(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

// bkParseCacheSpec parses a --cache-from / --cache-to value. A bare
// reference means type=registry,ref=<value>.
func bkParseCacheSpec(s string) (bkclient.CacheOptionsEntry, error) {
	if !strings.Contains(s, "=") {
		return bkclient.CacheOptionsEntry{Type: "registry", Attrs: map[string]string{"ref": s}}, nil
	}
	kv, err := bkParseCSV(s)
	if err != nil {
		return bkclient.CacheOptionsEntry{}, err
	}
	typ := kv["type"]
	if typ == "" {
		return bkclient.CacheOptionsEntry{}, errors.New("type is required")
	}
	delete(kv, "type")
	for _, k := range []string{"src", "dest"} {
		if v, ok := kv[k]; ok && (typ == "local") {
			if abs, err := filepath.Abs(expandHome(v)); err == nil {
				kv[k] = abs
			}
		}
	}
	return bkclient.CacheOptionsEntry{Type: typ, Attrs: kv}, nil
}

// ---------------------------------------------------------------------------
// Session attachables

// bkSecretStore serves secrets from memory.
type bkSecretStore map[string][]byte

func (s bkSecretStore) GetSecret(_ context.Context, id string) ([]byte, error) {
	if v, ok := s[id]; ok {
		return v, nil
	}
	return nil, secrets.ErrNotFound
}

// bkAttachables returns secrets, SSH and auth providers for req.
func bkAttachables(req seamBuildRequest, creds []registryCredential) ([]session.Attachable, error) {
	var out []session.Attachable
	if len(req.Secrets) > 0 {
		store := bkSecretStore{}
		for _, s := range req.Secrets {
			if s.ID == "" {
				return nil, errors.New("build secret without id")
			}
			if _, dup := store[s.ID]; dup {
				return nil, fmt.Errorf("build secret %q is declared more than once", s.ID)
			}
			n := 0
			for _, v := range []string{s.Src, s.Env, s.Value} {
				if v != "" {
					n++
				}
			}
			if n > 1 {
				return nil, fmt.Errorf("build secret %q: set only one of src, env and value", s.ID)
			}
			switch {
			case s.Src != "":
				b, err := os.ReadFile(expandHome(s.Src))
				if err != nil {
					return nil, fmt.Errorf("build secret %q: %w", s.ID, err)
				}
				store[s.ID] = b
			case s.Env != "":
				v, ok := os.LookupEnv(s.Env)
				if !ok {
					return nil, fmt.Errorf("build secret %q: environment variable %s is not set in the provider's environment", s.ID, s.Env)
				}
				store[s.ID] = []byte(v)
			default:
				// Value may legitimately be empty; buildx's env= default is the id.
				if s.Value == "" {
					if v, ok := os.LookupEnv(s.ID); ok {
						store[s.ID] = []byte(v)
						continue
					}
				}
				store[s.ID] = []byte(s.Value)
			}
		}
		out = append(out, secretsprovider.NewSecretProvider(store))
	}
	if len(req.SSH) > 0 {
		confs := make([]sshprovider.AgentConfig, 0, len(req.SSH))
		for _, s := range req.SSH {
			paths := make([]string, 0, len(s.Paths))
			for _, p := range s.Paths {
				paths = append(paths, expandHome(p))
			}
			confs = append(confs, sshprovider.AgentConfig{ID: s.ID, Paths: paths})
		}
		sp, err := sshprovider.NewSSHAgentProvider(confs)
		if err != nil {
			return nil, fmt.Errorf("ssh: %w", err)
		}
		out = append(out, sp)
	}
	out = append(out, &imgBuildKitAuth{creds: creds})
	return out, nil
}

// ---------------------------------------------------------------------------
// Exports

// bkLoad is a docker-exporter tarball streamed into POST /images/load.
type bkLoad struct {
	wg  sync.WaitGroup
	err error
}

func (l *bkLoad) wait() error {
	l.wg.Wait()
	return l.err
}

// bkLoader returns an exporter Output that pipes into the daemon's image
// load, and the handle to wait for it.
func bkLoader(ctx context.Context, pd *providerData) (func(map[string]string) (io.WriteCloser, error), *bkLoad) {
	l := &bkLoad{}
	return func(map[string]string) (io.WriteCloser, error) {
		if pd == nil || pd.Client == nil {
			return nil, errors.New("provider not configured")
		}
		pr, pw := io.Pipe()
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			res, err := pd.Client.ImageLoad(ctx, pr)
			if err != nil {
				l.err = err
				_ = pr.CloseWithError(err)
				return
			}
			defer res.Close()
			l.err = bkDrainJSONStream(res)
			_, _ = io.Copy(io.Discard, pr)
		}()
		return pw, nil
	}, l
}

// bkFileOutput writes an exporter tarball to dest ("-" is not supported).
func bkFileOutput(dest string) (func(map[string]string) (io.WriteCloser, error), error) {
	if dest == "" || dest == "-" {
		return nil, errors.New("dest must name a local file (stdout is not available to a provider)")
	}
	dest = expandHome(dest)
	return func(map[string]string) (io.WriteCloser, error) {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, err
		}
		return os.Create(dest)
	}, nil
}

// bkExports turns the outputs selection into exporter entries.
func bkExports(ctx context.Context, pd *providerData, conn *bkConn, o seamBuildOutputs) ([]bkclient.ExportEntry, []*bkLoad, error) {
	var out []bkclient.ExportEntry
	var loads []*bkLoad
	names := strings.Join(o.Names, ",")
	nameAttrs := func(extra map[string]string) map[string]string {
		a := map[string]string{}
		if names != "" {
			a["name"] = names
		}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	load := o.Load
	// buildx: the docker driver loads by default when nothing is asked, and
	// so does a builder created with default-load=true.
	if !o.Load && !o.Push && len(o.Raw) == 0 && (conn.Docker || conn.Node.Labels[bxbLabelDefaultLoad] == "true") {
		load = true
	}
	switch {
	case conn.Docker && (load || o.Push):
		a := nameAttrs(nil)
		if o.Push {
			a["push"] = "true"
		}
		out = append(out, bkclient.ExportEntry{Type: "moby", Attrs: a})
	default:
		if o.Push {
			out = append(out, bkclient.ExportEntry{Type: bkclient.ExporterImage, Attrs: nameAttrs(map[string]string{"push": "true"})})
		}
		if load {
			fn, l := bkLoader(ctx, pd)
			loads = append(loads, l)
			out = append(out, bkclient.ExportEntry{Type: bkclient.ExporterDocker, Attrs: nameAttrs(nil), Output: fn})
		}
	}
	for _, raw := range o.Raw {
		e, l, err := bkParseOutput(ctx, pd, conn, raw, names)
		if err != nil {
			return nil, nil, fmt.Errorf("output %q: %w", raw, err)
		}
		if l != nil {
			loads = append(loads, l)
		}
		out = append(out, e)
	}
	return out, loads, nil
}

// bkParseOutput parses one buildx --output value.
func bkParseOutput(ctx context.Context, pd *providerData, conn *bkConn, raw, names string) (bkclient.ExportEntry, *bkLoad, error) {
	var kv map[string]string
	if !strings.Contains(raw, "=") {
		// buildx: a bare path is type=local,dest=<path>.
		kv = map[string]string{"type": bkclient.ExporterLocal, "dest": raw}
	} else {
		var err error
		if kv, err = bkParseCSV(raw); err != nil {
			return bkclient.ExportEntry{}, nil, err
		}
	}
	typ := kv["type"]
	delete(kv, "type")
	if names != "" && kv["name"] == "" && (typ == "image" || typ == "registry" || typ == "docker" || typ == "oci") {
		kv["name"] = names
	}
	dest := kv["dest"]
	delete(kv, "dest")
	switch typ {
	case bkclient.ExporterLocal:
		if dest == "" {
			return bkclient.ExportEntry{}, nil, errors.New("type=local needs dest")
		}
		abs, err := filepath.Abs(expandHome(dest))
		if err != nil {
			return bkclient.ExportEntry{}, nil, err
		}
		return bkclient.ExportEntry{Type: typ, Attrs: kv, OutputDir: abs}, nil, nil
	case bkclient.ExporterTar, bkclient.ExporterOCI:
		fn, err := bkFileOutput(dest)
		if err != nil {
			return bkclient.ExportEntry{}, nil, err
		}
		return bkclient.ExportEntry{Type: typ, Attrs: kv, Output: fn}, nil, nil
	case bkclient.ExporterDocker:
		if dest != "" {
			fn, err := bkFileOutput(dest)
			if err != nil {
				return bkclient.ExportEntry{}, nil, err
			}
			return bkclient.ExportEntry{Type: typ, Attrs: kv, Output: fn}, nil, nil
		}
		if conn.Docker {
			return bkclient.ExportEntry{Type: "moby", Attrs: kv}, nil, nil
		}
		fn, l := bkLoader(ctx, pd)
		return bkclient.ExportEntry{Type: typ, Attrs: kv, Output: fn}, l, nil
	case "registry":
		kv["push"] = "true"
		typ = bkclient.ExporterImage
		fallthrough
	case bkclient.ExporterImage:
		if conn.Docker {
			typ = "moby"
		}
		return bkclient.ExportEntry{Type: typ, Attrs: kv}, nil, nil
	case "cacheonly":
		return bkclient.ExportEntry{Type: typ, Attrs: kv}, nil, nil
	case "":
		return bkclient.ExportEntry{}, nil, errors.New("type is required")
	default:
		return bkclient.ExportEntry{}, nil, fmt.Errorf("unsupported output type %q (local, tar, oci, docker, image, registry, cacheonly)", typ)
	}
}

// ---------------------------------------------------------------------------
// Result metadata

// bkMetadata builds buildx's --metadata-file object from the exporter
// response: base64-encoded JSON values are decoded, the rest kept as
// strings, plus buildx.build.ref and buildx.build.warnings.
func bkMetadata(resp map[string]string, node bkNode, ref string, warnings []map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range resp {
		if dt, err := base64.StdEncoding.DecodeString(v); err == nil && len(dt) > 0 && (dt[0] == '{' || dt[0] == '[') {
			var j any
			if json.Unmarshal(dt, &j) == nil {
				out[k] = j
				continue
			}
		}
		out[k] = v
	}
	out["buildx.build.ref"] = bkBuildRef(node, ref)
	if len(warnings) > 0 {
		out["buildx.build.warnings"] = warnings
	}
	return out
}

// bkBuildRef is buildx's builder/node/ref.
func bkBuildRef(node bkNode, ref string) string {
	return node.Builder + "/" + node.Name + "/" + ref
}

// ---------------------------------------------------------------------------
// Progress

// bkProgress renders SolveStatus updates as buildx's plain progress.
type bkProgress struct {
	mu       sync.Mutex
	onLine   func(string)
	lines    []string
	idx      map[string]int
	names    map[string]string
	started  map[string]bool
	done     map[string]bool
	partial  map[string]string
	errs     []string
	warnings []map[string]any
}

func newBKProgress(onLine func(string)) *bkProgress {
	return &bkProgress{onLine: onLine, idx: map[string]int{}, names: map[string]string{}, started: map[string]bool{}, done: map[string]bool{}, partial: map[string]string{}}
}

func (p *bkProgress) emit(s string) {
	p.lines = append(p.lines, s)
	if p.onLine != nil {
		p.onLine(s)
	}
}

func (p *bkProgress) num(d string) int {
	n, ok := p.idx[d]
	if !ok {
		n = len(p.idx) + 1
		p.idx[d] = n
	}
	return n
}

func (p *bkProgress) add(st *bkclient.SolveStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range st.Vertexes {
		d := v.Digest.String()
		n := p.num(d)
		if v.Name != "" {
			p.names[d] = v.Name
		}
		if v.Started != nil && !p.started[d] {
			p.started[d] = true
			p.emit(fmt.Sprintf("#%d %s", n, p.names[d]))
		}
		if v.Error != "" && !p.done[d] {
			p.done[d] = true
			msg := fmt.Sprintf("#%d ERROR: %s", n, v.Error)
			p.emit(msg)
			p.errs = append(p.errs, fmt.Sprintf("[%s] %s", p.names[d], v.Error))
			continue
		}
		if v.Completed != nil && !p.done[d] {
			p.done[d] = true
			if v.Cached {
				p.emit(fmt.Sprintf("#%d CACHED", n))
			} else if v.Started != nil {
				p.emit(fmt.Sprintf("#%d DONE %.1fs", n, v.Completed.Sub(*v.Started).Seconds()))
			} else {
				p.emit(fmt.Sprintf("#%d DONE", n))
			}
		}
	}
	for _, l := range st.Logs {
		d := l.Vertex.String()
		n := p.num(d)
		data := p.partial[d] + string(l.Data)
		parts := strings.Split(data, "\n")
		p.partial[d] = parts[len(parts)-1]
		for _, line := range parts[:len(parts)-1] {
			p.emit(fmt.Sprintf("#%d %s", n, strings.TrimRight(line, "\r")))
		}
	}
	for _, w := range st.Warnings {
		p.emit("WARNING: " + string(w.Short))
		wm := map[string]any{"level": w.Level, "short": string(w.Short), "url": w.URL}
		if len(w.Detail) > 0 {
			det := make([]string, 0, len(w.Detail))
			for _, x := range w.Detail {
				det = append(det, string(x))
			}
			wm["detail"] = det
		}
		p.warnings = append(p.warnings, wm)
	}
}

func (p *bkProgress) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]string, 0, len(p.partial))
	for k := range p.partial {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, d := range keys {
		if s := strings.TrimRight(p.partial[d], "\r"); s != "" {
			p.emit(fmt.Sprintf("#%d %s", p.num(d), s))
		}
		delete(p.partial, d)
	}
}

func (p *bkProgress) log() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.lines, "\n")
}

// wrap attaches the failed steps and the last log lines to a build error.
func (p *bkProgress) wrap(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var b strings.Builder
	b.WriteString(err.Error())
	if len(p.errs) > 0 {
		b.WriteString("\n\nfailed steps:\n  " + strings.Join(p.errs, "\n  "))
	}
	tail := p.lines
	if len(tail) > bkTailLines {
		tail = tail[len(tail)-bkTailLines:]
	}
	if len(tail) > 0 {
		b.WriteString("\n\nbuild log (last lines):\n  " + strings.Join(tail, "\n  "))
	}
	return &bkBuildError{msg: b.String(), err: err}
}

// bkBuildError is a build failure with log context.
type bkBuildError struct {
	msg string
	err error
}

func (e *bkBuildError) Error() string { return e.msg }
func (e *bkBuildError) Unwrap() error { return e.err }

// ---------------------------------------------------------------------------
// Tar context

// bkExtractTar unpacks a context tar into dir, refusing entries that
// escape it.
func bkExtractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(strings.TrimPrefix(h.Name, "/"))
		target := filepath.Join(root, name)
		if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
			return fmt.Errorf("tar entry %q escapes the context", h.Name)
		}
		mode := os.FileMode(h.Mode & 0o777)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(f, tr)
			ferr := f.Close()
			if cerr != nil {
				return cerr
			}
			if ferr != nil {
				return ferr
			}
			if !h.ModTime.IsZero() {
				_ = os.Chtimes(target, time.Now(), h.ModTime)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		default:
			// Hard links, devices and FIFOs are not part of build contexts.
		}
	}
}

// bkDockerfileSyntax returns the `# syntax=` parser directive ("" = none).
func bkDockerfileSyntax(text string) string {
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "#") {
			return ""
		}
		body := strings.TrimSpace(strings.TrimPrefix(l, "#"))
		k, v, ok := strings.Cut(body, "=")
		if !ok {
			return ""
		}
		if strings.EqualFold(strings.TrimSpace(k), "syntax") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
