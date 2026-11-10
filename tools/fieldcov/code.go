package main

// Code signal: a type-checked scan of internal/provider (non-test files).
//
//   - Engine: every field of a moby API or moby client struct the provider
//     writes (a composite-literal key, or the target of an assignment).
//   - Compose: every field of a compose-go types struct the compose code reads,
//     outside the *Unsupported warning functions and the files that only echo
//     the project back (hash, value, state upgrade).
//   - Bake: the target attribute names the bake parser switches on.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

const (
	mobyAPIPrefix    = "github.com/moby/moby/api/"
	mobyClientPkg    = "github.com/moby/moby/client"
	composeTypesPkg  = "github.com/compose-spec/compose-go/v2/types"
	providerPkgPath  = "github.com/JerryBalmer1/terraform-provider-docker/internal/provider"
	bakeParserSource = "buildx_bake_parse.go"
)

// composeEchoFiles read every compose field to echo or hash the project; a
// read there is not support.
var composeEchoFiles = map[string]bool{
	"compose_hash_compat.go":   true,
	"compose_project_value.go": true,
	"compose_state_upgrade.go": true,
	"compose_query.go":         true,
}

// goField is one struct field the provider touches.
type goField struct {
	Pkg, PkgName, Type, Field, JSON string
	Pos                             []string
}

func (g *goField) key() string { return g.Pkg + "." + g.Type + "." + g.Field }

type codeFacts struct {
	// Written moby fields (engine), keyed by goField.key().
	Written map[string]*goField
	// Read compose-go fields, keyed by goField.key().
	ComposeRead map[string]*goField
	// Bake target attribute names in the parser's switch statements.
	BakeCases map[string]string
}

func loadCodeFacts(repo string) (*codeFacts, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  repo,
	}
	pkgs, err := packages.Load(cfg, "./internal/provider")
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("loaded %d packages, want 1", len(pkgs))
	}
	p := pkgs[0]
	if len(p.Errors) > 0 {
		return nil, fmt.Errorf("internal/provider does not type-check: %v", p.Errors[0])
	}
	cf := &codeFacts{Written: map[string]*goField{}, ComposeRead: map[string]*goField{}, BakeCases: map[string]string{}}
	for _, f := range p.Syntax {
		fname := filepath.Base(p.Fset.Position(f.Pos()).Filename)
		if strings.HasSuffix(fname, "_test.go") {
			continue
		}
		cf.scanFile(p.Fset, p.TypesInfo, f, fname)
	}
	return cf, nil
}

func fieldOf(info *types.Info, sel *ast.SelectorExpr) (*types.Var, types.Type) {
	s, ok := info.Selections[sel]
	if !ok || s.Kind() != types.FieldVal {
		return nil, nil
	}
	v, _ := s.Obj().(*types.Var)
	if v == nil {
		return nil, nil
	}
	// The struct that declares the field: walk the selection's index path
	// (embedded fields included).
	t := s.Recv()
	idx := s.Index()
	for n, i := range idx {
		st := structOf(t)
		if st == nil {
			return v, nil
		}
		if n == len(idx)-1 {
			return v, t
		}
		t = st.Field(i).Type()
	}
	return v, t
}

func structOf(t types.Type) *types.Struct {
	for {
		switch x := t.(type) {
		case *types.Pointer:
			t = x.Elem()
		case *types.Named:
			st, _ := x.Underlying().(*types.Struct)
			return st
		case *types.Alias:
			t = types.Unalias(x)
		default:
			st, _ := t.Underlying().(*types.Struct)
			return st
		}
	}
}

func namedOf(t types.Type) *types.Named {
	for {
		switch x := t.(type) {
		case *types.Pointer:
			t = x.Elem()
		case *types.Alias:
			t = types.Unalias(x)
		case *types.Named:
			return x
		default:
			return nil
		}
	}
}

func jsonName(st *types.Struct, fieldName string) string {
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() != fieldName {
			continue
		}
		tag := reflect.StructTag(st.Tag(i)).Get("json")
		if n, _, _ := strings.Cut(tag, ","); n != "" && n != "-" {
			return n
		}
		return fieldName
	}
	return fieldName
}

func record(m map[string]*goField, owner types.Type, field string, pos string) {
	n := namedOf(owner)
	if n == nil || n.Obj().Pkg() == nil {
		return
	}
	st := structOf(n)
	if st == nil {
		return
	}
	g := &goField{Pkg: n.Obj().Pkg().Path(), PkgName: n.Obj().Pkg().Name(), Type: n.Obj().Name(), Field: field,
		JSON: jsonName(st, field)}
	if ex, ok := m[g.key()]; ok {
		ex.Pos = append(ex.Pos, pos)
		return
	}
	g.Pos = []string{pos}
	m[g.key()] = g
}

func isMoby(t types.Type) bool {
	n := namedOf(t)
	if n == nil || n.Obj().Pkg() == nil {
		return false
	}
	pp := n.Obj().Pkg().Path()
	return strings.HasPrefix(pp, mobyAPIPrefix) || pp == mobyClientPkg
}

func isCompose(t types.Type) bool {
	n := namedOf(t)
	return n != nil && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == composeTypesPkg
}

func (cf *codeFacts) scanFile(fset *token.FileSet, info *types.Info, f *ast.File, fname string) {
	posOf := func(n ast.Node) string {
		p := fset.Position(n.Pos())
		return fmt.Sprintf("%s:%d", fname, p.Line)
	}
	// Engine writes: composite literal keys.
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		t := info.TypeOf(cl)
		if t == nil || !isMoby(t) || structOf(t) == nil {
			return true
		}
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			id, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			record(cf.Written, t, id.Name, posOf(kv))
		}
		return true
	})
	// Engine writes: assignment targets (x.F = ..., x.F.G = ..., x.F[k] = ...).
	lhsField := func(e ast.Expr) {
		for {
			switch x := e.(type) {
			case *ast.IndexExpr:
				e = x.X
				continue
			case *ast.StarExpr:
				e = x.X
				continue
			case *ast.ParenExpr:
				e = x.X
				continue
			case *ast.SelectorExpr:
				if v, owner := fieldOf(info, x); v != nil && owner != nil && isMoby(owner) {
					record(cf.Written, owner, v.Name(), posOf(x))
				}
				// x.F.G = v also writes F (and its parents).
				e = x.X
				continue
			}
			return
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for _, l := range s.Lhs {
				lhsField(l)
			}
		case *ast.IncDecStmt:
			lhsField(s.X)
		case *ast.UnaryExpr:
			// &x.F handed to a decoder or helper writes F.
			if s.Op == token.AND {
				lhsField(s.X)
			}
		}
		return true
	})
	// Compose reads.
	if strings.HasPrefix(fname, "compose_") || strings.HasPrefix(fname, "c_compose") || strings.HasPrefix(fname, "action_compose") {
		if !composeEchoFiles[fname] {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if ok && strings.Contains(fd.Name.Name, "Unsupported") {
					continue
				}
				ast.Inspect(d, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if v, owner := fieldOf(info, sel); v != nil && owner != nil && isCompose(owner) {
						record(cf.ComposeRead, owner, v.Name(), posOf(sel))
					}
					return true
				})
				// Composite-literal keys on compose types count as use too.
				ast.Inspect(d, func(n ast.Node) bool {
					cl, ok := n.(*ast.CompositeLit)
					if !ok {
						return true
					}
					t := info.TypeOf(cl)
					if t == nil || !isCompose(t) {
						return true
					}
					for _, e := range cl.Elts {
						if kv, ok := e.(*ast.KeyValueExpr); ok {
							if id, ok := kv.Key.(*ast.Ident); ok {
								record(cf.ComposeRead, t, id.Name, posOf(kv))
							}
						}
					}
					return true
				})
			}
		}
	}
	// Bake: case "attr": in the parser.
	if fname == bakeParserSource {
		ast.Inspect(f, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						if _, seen := cf.BakeCases[s]; !seen {
							cf.BakeCases[s] = posOf(bl)
						}
					}
				}
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// Engine field -> written moby field

func normName(s string) []string {
	l := strings.ToLower(s)
	out := []string{l}
	for _, suf := range []string{"request", "options", "opts", "body"} {
		if strings.HasSuffix(l, suf) && len(l) > len(suf) {
			out = append(out, strings.TrimSuffix(l, suf))
		}
	}
	return out
}

// contextAliases maps a swagger definition or inline property name to moby
// Go types (pkgname.Type) that hold the same fields under another name.
var contextAliases = map[string][]string{
	"Resources":                 {"container.Resources"},
	"TaskSpec.Resources":        {"swarm.ResourceRequirements"},
	"TaskSpec.RestartPolicy":    {"swarm.RestartPolicy"},
	"TaskSpec.LogDriver":        {"swarm.Driver"},
	"Limits":                    {"swarm.Limit"},
	"Reservations":              {"swarm.Resources"},
	"LogDriver":                 {"swarm.Driver"},
	"LogConfig":                 {"container.LogConfig"},
	"TaskTemplate":              {"swarm.TaskSpec"},
	"TaskSpec":                  {"swarm.TaskSpec"},
	"ContainerSpec":             {"swarm.ContainerSpec"},
	"Driver":                    {"swarm.Driver"},
	"Templating":                {"swarm.Driver"},
	"PluginSpec":                {"runtime.PluginSpec"},
	"NetworkAttachmentSpec":     {"swarm.NetworkAttachmentSpec"},
	"Networks":                  {"swarm.NetworkAttachmentConfig"},
	"EndpointSpec":              {"swarm.EndpointSpec"},
	"EndpointPortConfig":        {"swarm.PortConfig"},
	"Ports":                     {"swarm.PortConfig"},
	"UpdateConfig":              {"swarm.UpdateConfig"},
	"RollbackConfig":            {"swarm.UpdateConfig"},
	"Mode":                      {"swarm.ServiceMode"},
	"Replicated":                {"swarm.ReplicatedService"},
	"ReplicatedJob":             {"swarm.ReplicatedJob"},
	"Placement":                 {"swarm.Placement"},
	"Preferences":               {"swarm.PlacementPreference"},
	"Spread":                    {"swarm.SpreadOver"},
	"Platforms":                 {"swarm.Platform"},
	"DNSConfig":                 {"swarm.DNSConfig"},
	"Privileges":                {"swarm.Privileges"},
	"CredentialSpec":            {"swarm.CredentialSpec"},
	"SELinuxContext":            {"swarm.SELinuxContext"},
	"Seccomp":                   {"swarm.SeccompOpts"},
	"AppArmor":                  {"swarm.AppArmorOpts"},
	"Secrets":                   {"swarm.SecretReference"},
	"Configs":                   {"swarm.ConfigReference"},
	"File":                      {"swarm.SecretReferenceFileTarget", "swarm.ConfigReferenceFileTarget"},
	"HealthConfig":              {"container.HealthConfig"},
	"Mount":                     {"mount.Mount"},
	"BindOptions":               {"mount.BindOptions"},
	"VolumeOptions":             {"mount.VolumeOptions"},
	"DriverConfig":              {"mount.Driver"},
	"TmpfsOptions":              {"mount.TmpfsOptions"},
	"ImageOptions":              {"mount.ImageOptions"},
	"ClusterOptions":            {"mount.ClusterOptions"},
	"EndpointSettings":          {"network.EndpointSettings"},
	"EndpointIPAMConfig":        {"network.EndpointIPAMConfig"},
	"IPAMConfig":                {"network.IPAMConfig", "network.EndpointIPAMConfig"},
	"IPAM":                      {"network.IPAM"},
	"ConfigReference":           {"network.ConfigReference"},
	"SwarmSpec":                 {"swarm.Spec"},
	"Orchestration":             {"swarm.OrchestrationConfig"},
	"Raft":                      {"swarm.RaftConfig"},
	"Dispatcher":                {"swarm.DispatcherConfig"},
	"CAConfig":                  {"swarm.CAConfig"},
	"ExternalCAs":               {"swarm.ExternalCA"},
	"EncryptionConfig":          {"swarm.EncryptionConfig"},
	"TaskDefaults":              {"swarm.TaskDefaults"},
	"NodeSpec":                  {"swarm.NodeSpec"},
	"SecretSpec":                {"swarm.SecretSpec"},
	"ConfigSpec":                {"swarm.ConfigSpec"},
	"ContainerConfig":           {"container.Config"},
	"HostConfig":                {"container.HostConfig"},
	"NetworkingConfig":          {"network.NetworkingConfig"},
	"RestartPolicy":             {"container.RestartPolicy", "swarm.RestartPolicy"},
	"Ulimits":                   {"container.Ulimit"},
	"Devices":                   {"container.DeviceMapping"},
	"DeviceMapping":             {"container.DeviceMapping"},
	"DeviceRequest":             {"container.DeviceRequest"},
	"ThrottleDevice":            {"blkiodev.ThrottleDevice"},
	"BlkioWeightDevice":         {"blkiodev.WeightDevice"},
	"GenericResources":          {"swarm.GenericResource"},
	"NamedResourceSpec":         {"swarm.NamedGenericResource"},
	"DiscreteResourceSpec":      {"swarm.DiscreteGenericResource"},
	"VolumeCreateRequest":       {"volume.CreateRequest", "client.VolumeCreateOptions"},
	"ClusterVolumeSpec":         {"volume.ClusterVolumeSpec"},
	"AccessMode":                {"volume.AccessMode"},
	"AccessibilityRequirements": {"volume.TopologyRequirement"},
	"CapacityRange":             {"volume.CapacityRange"},
	"NetworkCreateRequest":      {"network.CreateRequest", "client.NetworkCreateOptions"},
	"ExecConfig":                {"container.ExecCreateRequest", "client.ExecCreateOptions"},
	"ContainerExec":             {"container.ExecCreateRequest", "client.ExecCreateOptions"},
	"ExecStart":                 {"container.ExecStartRequest", "client.ExecStartOptions"},
	"ContainerUpdate":           {"container.UpdateConfig", "client.ContainerUpdateOptions"},
	"SwarmInit":                 {"swarm.InitRequest", "client.SwarmInitOptions"},
	"SwarmJoin":                 {"swarm.JoinRequest", "client.SwarmJoinOptions"},
	"SwarmUnlock":               {"swarm.UnlockRequest", "client.SwarmUnlockOptions"},
	"ContainerCreate":           {"container.CreateRequest", "client.ContainerCreateOptions"},
	"ImageCommit":               {"container.Config", "client.ContainerCommitOptions"},
	"NetworkConnect":            {"network.ConnectRequest", "client.NetworkConnectOptions"},
	"PluginSet":                 {"client.PluginSetOptions"},
	"PluginPull":                {"client.PluginInstallOptions"},
	"PluginUpgrade":             {"client.PluginUpgradeOptions", "client.PluginInstallOptions"},
	"PluginPrivilege":           {"plugin.Privilege"},
}

// queryAliases maps an operationId to the moby client option types its
// query parameters are set through.
var queryAliases = map[string][]string{
	"ImageCreate":         {"ImagePullOptions", "ImageImportOptions"},
	"ContainerExec":       {"ExecCreateOptions"},
	"ExecStart":           {"ExecStartOptions"},
	"ImageCommit":         {"ContainerCommitOptions"},
	"PutContainerArchive": {"CopyToContainerOptions"},
	"PluginPull":          {"PluginInstallOptions"},
	"PluginUpgrade":       {"PluginInstallOptions"},
	"ContainerKill":       {"ContainerKillOptions"},
	"ImageTag":            {"ImageTagOptions"},
	"ImageLoad":           {"ImageLoadOptions"},
	"BuildPrune":          {"BuildCachePruneOptions"},
}

func typeMatches(ctx string, g *goField) bool {
	is := func(als []string) bool {
		for _, a := range als {
			if a == g.PkgName+"."+g.Type {
				return true
			}
		}
		return false
	}
	// A qualified inline context (Parent.Prop) with an alias is exact.
	if als, ok := contextAliases[ctx]; ok && strings.Contains(ctx, ".") {
		return is(als)
	}
	if i := strings.LastIndex(ctx, "."); i >= 0 {
		ctx = ctx[i+1:]
	}
	// An alias is exclusive: the name-based match below is only for
	// contexts no alias covers.
	if als, ok := contextAliases[ctx]; ok {
		return is(als)
	}
	cs := normName(ctx)
	gs := append(normName(g.Type), normName(g.PkgName+g.Type)...)
	for _, c := range cs {
		for _, x := range gs {
			if c == x {
				return true
			}
		}
	}
	return false
}

// engineCreditFile reports whether a write in this file counts for the
// Engine resources: the compose and stack conversions write the same moby
// structs for docker_compose / docker_stack, which does not make the field
// available on docker_container or docker_service.
func engineCreditFile(pos string) bool {
	f, _, _ := strings.Cut(pos, ":")
	for _, p := range []string{"compose_", "c_compose", "action_compose", "stack_", "datasource_compose", "datasource_stack",
		"buildx_builder_resource", "action_buildx"} {
		if strings.HasPrefix(f, p) {
			return false
		}
	}
	return true
}

// creditPos returns the positions of g that count for the given items.
func creditPos(g *goField, itemIDs []string) []string {
	var out []string
	for _, p := range g.Pos {
		if engineCreditFile(p) && creditsItem(p, itemIDs) {
			out = append(out, p)
		}
	}
	return out
}

// codeMatch returns the written moby field that sets an engine field, or nil.
func (cf *codeFacts) codeMatch(f field, itemIDs []string) *goField {
	leaf := ""
	for i := len(f.Segs) - 1; i >= 0; i-- {
		if f.Segs[i] != "[]" && f.Segs[i] != "{}" {
			leaf = f.Segs[i]
			break
		}
	}
	keys := make([]string, 0, len(cf.Written))
	for k := range cf.Written {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if f.Query {
		op := strings.TrimPrefix(f.Context, "")
		names := append([]string{op + "Options"}, queryAliases[op]...)
		for _, k := range keys {
			g := cf.Written[k]
			if g.Pkg != mobyClientPkg {
				continue
			}
			okType := false
			for _, n := range names {
				if strings.EqualFold(g.Type, n) {
					okType = true
				}
			}
			if !okType {
				continue
			}
			if len(creditPos(g, itemIDs)) == 0 {
				continue
			}
			fl, ll := strings.ToLower(g.Field), strings.ToLower(leaf)
			if fl == ll || fl == ll+"s" || fl+"s" == ll || strings.EqualFold(g.JSON, leaf) {
				return g
			}
		}
		return nil
	}
	for _, k := range keys {
		g := cf.Written[k]
		if !strings.EqualFold(g.JSON, leaf) && !strings.EqualFold(g.Field, leaf) {
			continue
		}
		if len(creditPos(g, itemIDs)) == 0 {
			continue
		}
		if typeMatches(f.Context, g) {
			return g
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Compose field -> compose-go Go field

// composeGoTypes are the compose-go root types of the counted definitions.
var composeGoRoots = map[string]string{
	"service": "ServiceConfig",
	"network": "NetworkConfig",
	"volume":  "VolumeConfig",
	"config":  "ConfigObjConfig",
	"secret":  "SecretConfig",
	"model":   "ModelConfig",
	"include": "IncludeConfig",
}

// composeLookup resolves a compose-spec property path to the compose-go
// struct fields it lands in (root first), using the yaml tags of the
// compose-go types package loaded by go/packages.
type composeLookup struct {
	scope *types.Scope
}

func loadComposeLookup(repo string) (*composeLookup, error) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes, Dir: repo}
	pkgs, err := packages.Load(cfg, composeTypesPkg)
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 || pkgs[0].Types == nil {
		return nil, fmt.Errorf("cannot load %s", composeTypesPkg)
	}
	return &composeLookup{scope: pkgs[0].Types.Scope()}, nil
}

func yamlName(st *types.Struct, i int) (string, bool) {
	tag := reflect.StructTag(st.Tag(i))
	y := tag.Get("yaml")
	n, opts, _ := strings.Cut(y, ",")
	if strings.Contains(opts, "inline") {
		return "", true
	}
	if n == "" {
		n, _, _ = strings.Cut(tag.Get("json"), ",")
	}
	return n, false
}

// elemType unwraps pointers, slices, arrays and maps to the element type.
func elemType(t types.Type) types.Type {
	for {
		switch x := t.(type) {
		case *types.Pointer:
			t = x.Elem()
		case *types.Slice:
			t = x.Elem()
		case *types.Array:
			t = x.Elem()
		case *types.Map:
			t = x.Elem()
		case *types.Alias:
			t = types.Unalias(x)
		case *types.Named:
			switch x.Underlying().(type) {
			case *types.Slice, *types.Map, *types.Pointer, *types.Array:
				t = x.Underlying()
				continue
			}
			return x
		default:
			return t
		}
	}
}

// findField looks a yaml name up in a struct, descending into inline fields.
func findField(st *types.Struct, name string) (*types.Var, types.Type, bool) {
	for i := 0; i < st.NumFields(); i++ {
		y, inline := yamlName(st, i)
		if inline {
			if sub := structOf(elemType(st.Field(i).Type())); sub != nil {
				if v, owner, ok := findField(sub, name); ok {
					return v, owner, true
				}
			}
			continue
		}
		if y == name {
			return st.Field(i), nil, true
		}
	}
	return nil, nil, false
}

// resolve returns the compose-go (owner type, field) pairs along segs.
func (cl *composeLookup) resolve(def string, segs []string) ([]*goField, error) {
	root := composeGoRoots[def]
	obj := cl.scope.Lookup(root)
	if obj == nil {
		return nil, fmt.Errorf("compose-go has no type %s", root)
	}
	cur := obj.Type()
	var out []*goField
	for _, s := range segs {
		if s == "[]" || s == "{}" {
			continue
		}
		n := namedOf(elemType(cur))
		if n == nil {
			return out, fmt.Errorf("%s: %s is not a struct", strings.Join(segs, "/"), s)
		}
		st := structOf(n)
		if st == nil {
			return out, fmt.Errorf("%s: %s is not a struct", strings.Join(segs, "/"), s)
		}
		v, owner, ok := findField(st, s)
		if !ok {
			return out, fmt.Errorf("compose-go %s has no field for %q", n.Obj().Name(), s)
		}
		ownerNamed := n
		if owner != nil {
			ownerNamed = namedOf(owner)
		}
		// findField through an inline: recover the real owner type.
		if ownerNamed == n {
			if real := inlineOwner(st, s); real != nil {
				ownerNamed = real
			}
		}
		out = append(out, &goField{Pkg: composeTypesPkg, PkgName: "types", Type: ownerNamed.Obj().Name(), Field: v.Name()})
		cur = v.Type()
	}
	return out, nil
}

func inlineOwner(st *types.Struct, name string) *types.Named {
	for i := 0; i < st.NumFields(); i++ {
		y, inline := yamlName(st, i)
		if !inline {
			if y == name {
				return nil
			}
			continue
		}
		n := namedOf(elemType(st.Field(i).Type()))
		if n == nil {
			continue
		}
		sub := structOf(n)
		if sub == nil {
			continue
		}
		if _, _, ok := findField(sub, name); ok {
			if deeper := inlineOwner(sub, name); deeper != nil {
				return deeper
			}
			return n
		}
	}
	return nil
}
