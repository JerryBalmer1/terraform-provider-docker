package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LedgerDir is where the gate ledgers live. It is excluded from every hash: writing a ledger
// must not invalidate it.
const LedgerDir = "test/harness/.ledger/"

const providerDir = "internal/provider"

// globalPrefixes: any change under these invalidates every unit of every gate.
var globalPrefixes = []string{"test/harness/", "build/", "tools/ledger/"}

// alwaysIn: files every unit depends on.
var alwaysIn = []string{
	"go.mod", "go.sum",
	"internal/provider/registry_gen.go", "internal/provider/contract_gen.go",
	"reference/SOURCES.md",
	"terraform-provider-docker.build.ps1",
}

// repo is the set of files a ledger may hash: git-tracked plus untracked-not-ignored (so .logs,
// .work, .bin, .env never count), minus the ledgers themselves. Without git it walks the tree.
type repo struct {
	root  string
	files []string // sorted, slash-separated, repo-relative
	set   map[string]bool
}

func loadRepo(root string) (*repo, error) {
	var files []string
	cmd := exec.Command("git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
	out, err := cmd.Output()
	if err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
	} else {
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".git") || d.Name() == ".logs" || d.Name() == ".work" {
					return filepath.SkipDir
				}
				return nil
			}
			files = append(files, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	r := &repo{root: root, set: map[string]bool{}}
	for _, f := range files {
		if strings.HasPrefix(f, LedgerDir) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			continue // deleted but still tracked
		}
		r.files = append(r.files, f)
		r.set[f] = true
	}
	sort.Strings(r.files)
	return r, nil
}

// under returns the files below a repo-relative directory (recursive) or the file itself.
func (r *repo) under(p string) []string {
	p = strings.TrimSuffix(path.Clean(p), "/")
	if r.set[p] {
		return []string{p}
	}
	if p == "." || p == "" {
		return nil // never the whole repository
	}
	var out []string
	pre := p + "/"
	i := sort.SearchStrings(r.files, pre)
	for ; i < len(r.files) && strings.HasPrefix(r.files[i], pre); i++ {
		out = append(out, r.files[i])
	}
	return out
}

// direct returns the files directly in a directory (not in subdirectories).
func (r *repo) direct(dir string) []string {
	var out []string
	for _, f := range r.under(dir) {
		if path.Dir(f) == dir {
			out = append(out, f)
		}
	}
	return out
}

func (r *repo) globalFiles() []string {
	var out []string
	for _, p := range globalPrefixes {
		out = append(out, r.under(p)...)
	}
	return append(out, alwaysIn...)
}

// ---------------------------------------------------------------------------- static map

// staticEntry is a unit's declared inputs (tools/ledger/static.json): extra globs, or the reason
// its inputs are unknown (always run). Keys are "<gate>:<unit key>".
type staticEntry struct {
	Inputs  []string `json:"inputs,omitempty"`
	ExecOK  string   `json:"exec_ok,omitempty"`
	Unknown string   `json:"unknown,omitempty"`
}

func loadStatic(root string) (map[string]staticEntry, error) {
	b, err := os.ReadFile(filepath.Join(root, "tools/ledger/static.json"))
	if os.IsNotExist(err) {
		return map[string]staticEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]staticEntry
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("tools/ledger/static.json: %w", err)
	}
	return m, nil
}

func (r *repo) globs(patterns []string) []string {
	var out []string
	for _, g := range patterns {
		if !strings.ContainsAny(g, "*?[") {
			out = append(out, r.under(g)...)
			continue
		}
		for _, f := range r.files {
			if ok, _ := path.Match(g, f); ok {
				out = append(out, f)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------- Go packages

type goPkg struct {
	ImportPath, Dir                              string
	GoFiles, CgoFiles, TestGoFiles, XTestGoFiles []string
	EmbedFiles, TestEmbedFiles, XTestEmbedFiles  []string
	Imports, TestImports, XTestImports           []string
}

func listPackages(root string) ([]goPkg, string, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, "", fmt.Errorf("go list: %w", err)
	}
	var pkgs []goPkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p goPkg
		if err := dec.Decode(&p); err != nil {
			return nil, "", err
		}
		pkgs = append(pkgs, p)
	}
	modOut, err := exec.Command("go", "list", "-m").Output()
	mod := strings.TrimSpace(string(modOut))
	if err != nil || mod == "" {
		b, _ := os.ReadFile(filepath.Join(root, "go.mod"))
		if m := regexp.MustCompile(`(?m)^module\s+(\S+)`).FindSubmatch(b); m != nil {
			mod = string(m[1])
		}
	}
	return pkgs, mod, nil
}

func relDir(root, dir string) string {
	r, err := filepath.Rel(root, dir)
	if err != nil {
		return dir
	}
	return filepath.ToSlash(r)
}

var pathLit = regexp.MustCompile(`^[A-Za-z0-9_.*/-]+$`)

// literalPaths returns the repo files that string literals in the given Go files name, resolved
// against the package directory and the repository root (a file, a directory's files, or a glob).
func (r *repo) literalPaths(pkgDir string, goFiles []string) []string {
	var out []string
	fset := token.NewFileSet()
	for _, f := range goFiles {
		af, err := parser.ParseFile(fset, filepath.Join(r.root, filepath.FromSlash(f)), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		ast.Inspect(af, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil || len(s) < 2 || !pathLit.MatchString(s) || !strings.ContainsAny(s, "/.") {
				return true
			}
			for _, base := range []string{pkgDir, "."} {
				p := path.Clean(path.Join(base, strings.TrimPrefix(s, "./")))
				if strings.HasPrefix(p, "..") || p == "." || p == pkgDir {
					continue
				}
				if strings.ContainsAny(p, "*?[") {
					out = append(out, r.globs([]string{p})...)
				} else {
					out = append(out, r.under(p)...)
				}
			}
			return true
		})
	}
	return out
}

func importsExec(root string, files []string) bool {
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(f)), nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, im := range af.Imports {
			if im.Path.Value == `"os/exec"` {
				return true
			}
		}
	}
	return false
}

// unitUnits: one unit per Go package (key: the repo-relative directory, "." for the root).
// Inputs: the package's own files, its testdata/, its embeds, the files string literals in it
// name, and the non-test Go files and embeds of every in-module package it imports (transitively).
// A package that runs other programs (imports os/exec) is unknown unless static.json vouches.
func unitUnits(r *repo, static map[string]staticEntry) ([]Unit, error) {
	pkgs, mod, err := listPackages(r.root)
	if err != nil {
		return nil, err
	}
	byPath := map[string]goPkg{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	join := func(dir string, names []string) []string {
		var o []string
		for _, n := range names {
			o = append(o, dir+"/"+n)
		}
		return o
	}
	var units []Unit
	for _, p := range pkgs {
		dir := relDir(r.root, p.Dir)
		u := Unit{Key: dir}
		own := join(dir, append(append(append(append([]string{}, p.GoFiles...), p.CgoFiles...), p.TestGoFiles...), p.XTestGoFiles...))
		u.Files = append(u.Files, own...)
		u.Files = append(u.Files, join(dir, append(append(append([]string{}, p.EmbedFiles...), p.TestEmbedFiles...), p.XTestEmbedFiles...))...)
		u.Files = append(u.Files, r.under(path.Join(dir, "testdata"))...)
		u.Files = append(u.Files, r.literalPaths(dir, own)...)
		seen := map[string]bool{}
		var walk func(ip string)
		walk = func(ip string) {
			if seen[ip] || !strings.HasPrefix(ip, mod) {
				return
			}
			seen[ip] = true
			d, ok := byPath[ip]
			if !ok {
				return
			}
			dd := relDir(r.root, d.Dir)
			u.Files = append(u.Files, join(dd, append(append([]string{}, d.GoFiles...), d.CgoFiles...))...)
			u.Files = append(u.Files, join(dd, d.EmbedFiles)...)
			for _, i := range d.Imports {
				walk(i)
			}
		}
		for _, i := range append(append(append([]string{}, p.Imports...), p.TestImports...), p.XTestImports...) {
			walk(i)
		}
		st, hasStatic := static["unit:"+dir]
		u.Files = append(u.Files, r.globs(st.Inputs)...)
		switch {
		case st.Unknown != "":
			u.Unknown = st.Unknown
		case importsExec(r.root, own) && !(hasStatic && st.ExecOK != ""):
			u.Unknown = "runs other programs (os/exec) and tools/ledger/static.json does not declare what they read"
		}
		units = append(units, u)
	}
	return units, nil
}

// ---------------------------------------------------------------------------- provider declaration graph

type decl struct {
	file  string
	test  bool
	refs  []string // identifiers used in the declaration
	lits  []string // string literals in the declaration
	recv  string   // receiver type name for methods
	isTyp bool
}

// provGraph is a name-level graph of the provider package's top-level declarations. A
// declaration reaches every top-level name it mentions; reaching a type reaches all of its
// methods (they can be called through interfaces). Declarations in registry_gen.go are a
// barrier: the provider's type lists would otherwise reach every type. The types a test or
// example exercises enter through their names in configuration strings instead.
type provGraph struct {
	byName  map[string][]*decl
	methods map[string][]*decl
	inits   []*decl
	ctor    map[string][]string // registered type name -> constructor names
	allCtor []string
	embeds  []string
}

var registryLine = regexp.MustCompile(`(?m)^\s*(New\w+),\s*//\s*(\w+)\s+(\S+)\s*$`)

func loadProvGraph(r *repo, embeds []string) (*provGraph, error) {
	g := &provGraph{byName: map[string][]*decl{}, methods: map[string][]*decl{}, ctor: map[string][]string{}, embeds: embeds}
	reg, err := os.ReadFile(filepath.Join(r.root, providerDir, "registry_gen.go"))
	if err != nil {
		return nil, err
	}
	for _, m := range registryLine.FindAllStringSubmatch(string(reg), -1) {
		g.ctor[m[3]] = append(g.ctor[m[3]], m[1])
		g.allCtor = append(g.allCtor, m[1])
	}
	fset := token.NewFileSet()
	for _, f := range r.direct(providerDir) {
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "/registry_gen.go") {
			continue
		}
		af, err := parser.ParseFile(fset, filepath.Join(r.root, filepath.FromSlash(f)), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		test := strings.HasSuffix(f, "_test.go")
		mk := func(n ast.Node) *decl {
			d := &decl{file: f, test: test}
			ast.Inspect(n, func(x ast.Node) bool {
				switch v := x.(type) {
				case *ast.Ident:
					d.refs = append(d.refs, v.Name)
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						if s, err := strconv.Unquote(v.Value); err == nil {
							d.lits = append(d.lits, s)
						}
					}
				}
				return true
			})
			return d
		}
		for _, dd := range af.Decls {
			switch v := dd.(type) {
			case *ast.FuncDecl:
				d := mk(v)
				if v.Recv != nil && len(v.Recv.List) > 0 {
					d.recv = recvName(v.Recv.List[0].Type)
					g.methods[d.recv] = append(g.methods[d.recv], d)
					continue
				}
				if v.Name.Name == "init" {
					g.inits = append(g.inits, d)
					continue
				}
				g.byName[v.Name.Name] = append(g.byName[v.Name.Name], d)
			case *ast.GenDecl:
				for _, s := range v.Specs {
					switch sp := s.(type) {
					case *ast.TypeSpec:
						d := mk(sp)
						d.isTyp = true
						g.byName[sp.Name.Name] = append(g.byName[sp.Name.Name], d)
					case *ast.ValueSpec:
						d := mk(sp)
						for _, n := range sp.Names {
							if n.Name != "_" {
								g.byName[n.Name] = append(g.byName[n.Name], d)
							}
						}
					}
				}
			}
		}
	}
	return g, nil
}

func recvName(e ast.Expr) string {
	for {
		switch v := e.(type) {
		case *ast.StarExpr:
			e = v.X
		case *ast.IndexExpr:
			e = v.X
		case *ast.IndexListExpr:
			e = v.X
		case *ast.Ident:
			return v.Name
		default:
			return ""
		}
	}
}

var typeNameRe = regexp.MustCompile(`\bdocker_[a-z0-9_]+`)
var funcNameRe = regexp.MustCompile(`provider::docker::([a-z0-9_]+)`)
var dynamicTypeRe = regexp.MustCompile(`docker_%|docker_$|provider::docker::%|provider::docker::$`)

// typeNames returns the registered type names a text mentions (docker_* names and provider functions).
func (g *provGraph) typeNames(text string) []string {
	var out []string
	for _, m := range typeNameRe.FindAllString(text, -1) {
		if _, ok := g.ctor[m]; ok {
			out = append(out, m)
		}
	}
	for _, m := range funcNameRe.FindAllStringSubmatch(text, -1) {
		if _, ok := g.ctor[m[1]]; ok {
			out = append(out, m[1])
		}
	}
	return out
}

// reach returns the files the roots reach (with or without test files), the type names their
// literals mention, whether a test declaration lists every type (references a registry
// function), and whether a literal builds a type name at run time.
func (g *provGraph) reach(roots []string, withTests bool, extraTypes []string) (files []string, types []string, dynamic bool) {
	seen := map[*decl]bool{}
	seenName := map[string]bool{}
	typeSet := map[string]bool{}
	var queue []*decl
	addName := func(n string) {
		if seenName[n] {
			return
		}
		seenName[n] = true
		for _, d := range g.byName[n] {
			if d.test && !withTests {
				continue
			}
			queue = append(queue, d)
			if d.isTyp {
				for _, m := range g.methods[n] {
					if !m.test || withTests {
						queue = append(queue, m)
					}
				}
			}
		}
	}
	addType := func(t string) {
		if typeSet[t] {
			return
		}
		typeSet[t] = true
		for _, c := range g.ctor[t] {
			addName(c)
		}
	}
	for _, d := range g.inits {
		if !d.test || withTests {
			queue = append(queue, d)
		}
	}
	for _, n := range roots {
		addName(n)
	}
	for _, t := range extraTypes {
		addType(t)
	}
	registryFns := map[string]bool{"genResources": true, "genDataSources": true, "genActions": true, "genEphemeralResources": true, "genListResources": true, "genFunctions": true}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if seen[d] {
			continue
		}
		seen[d] = true
		files = append(files, d.file)
		for _, ref := range d.refs {
			if d.test && registryFns[ref] {
				// a test that walks the registry exercises every type
				for t := range g.ctor {
					addType(t)
				}
			}
			addName(ref)
		}
		if d.test {
			for _, l := range d.lits {
				if dynamicTypeRe.MatchString(l) {
					dynamic = true
				}
				for _, t := range g.typeNames(l) {
					addType(t)
				}
			}
		}
	}
	for t := range typeSet {
		types = append(types, t)
	}
	sort.Strings(types)
	return files, types, dynamic
}

// testAccUnits: one unit per top-level TestAcc* function in internal/provider. Inputs: the
// declarations the function reaches (its file, its helpers, the code of every type its
// configurations name, the provider itself), the provider's embeds, and the repo files its
// literals name. Unknown when no type can be found or a type name is built at run time.
func testAccUnits(r *repo, g *provGraph, static map[string]staticEntry, swarmRe *regexp.Regexp) []Unit {
	var names []string
	for n, ds := range g.byName {
		if strings.HasPrefix(n, "TestAcc") {
			for _, d := range ds {
				if d.test && !d.isTyp {
					names = append(names, n)
					break
				}
			}
		}
	}
	sort.Strings(names)
	var units []Unit
	for _, n := range names {
		files, types, dynamic := g.reach([]string{n}, true, nil)
		u := Unit{Key: n, Extra: map[string]string{}}
		u.Files = append(u.Files, files...)
		u.Files = append(u.Files, g.embeds...)
		var testFiles []string
		for _, f := range uniqSorted(files) {
			if strings.HasSuffix(f, "_test.go") {
				testFiles = append(testFiles, f)
			}
		}
		u.Files = append(u.Files, r.literalPaths(providerDir, testFiles)...)
		u.Files = append(u.Files, r.under(providerDir+"/testdata")...)
		st := static["testacc:"+n]
		u.Files = append(u.Files, r.globs(st.Inputs)...)
		switch {
		case st.Unknown != "":
			u.Unknown = st.Unknown
		case dynamic:
			u.Unknown = "a configuration builds a type name at run time"
		case len(types) == 0:
			u.Unknown = "no registered type name found in its configurations"
		}
		u.Extra["types"] = strings.Join(types, ",")
		if swarmRe != nil && swarmRe.MatchString(n) {
			u.Extra["pass"] = "swarm"
		} else {
			u.Extra["pass"] = "plain"
		}
		units = append(units, u)
	}
	return units
}

// providerFor returns the provider files a configuration naming these types exercises.
func (g *provGraph) providerFor(types []string) []string {
	files, _, _ := g.reach([]string{"New"}, false, types)
	return append(files, g.embeds...)
}

// exampleUnits: one unit per examples/ folder that has a <name>.tests.ps1. Inputs: the folder's
// files, the TfDockerTest module, and the provider code of every type its files name.
func exampleUnits(r *repo, g *provGraph, static map[string]staticEntry) []Unit {
	dirs := map[string]bool{}
	for _, f := range r.under("examples") {
		if strings.HasSuffix(f, ".tests.ps1") {
			dirs[path.Dir(f)] = true
		}
	}
	var keys []string
	for d := range dirs {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	module := r.under("test/TfDockerTest")
	var units []Unit
	for _, d := range keys {
		key := strings.TrimPrefix(d, "examples/")
		u := Unit{Key: key, Extra: map[string]string{}}
		own := r.under(d)
		var text strings.Builder
		for _, f := range own {
			b, _ := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(f)))
			text.Write(b)
			text.WriteByte('\n')
		}
		types := uniqSorted(g.typeNames(text.String()))
		u.Files = append(append(append(u.Files, own...), module...), g.providerFor(types)...)
		st := static["examples:"+key]
		u.Files = append(u.Files, r.globs(st.Inputs)...)
		if st.Unknown != "" {
			u.Unknown = st.Unknown
		} else if dynamicTypeRe.MatchString(text.String()) {
			u.Unknown = "a file builds a type name at run time"
		}
		u.Extra["types"] = strings.Join(types, ",")
		units = append(units, u)
	}
	return units
}

// pairwiseUnits: one unit per generated config <configs>/<type>/<nn>. Inputs: the generated
// main.tf and expect.json (absolute paths outside the repo listing are hashed as given), the
// type's reference/_gen/pairwise/<type>.json, tools/pairwise, the TfDockerTest module, and the
// provider code of the type.
func pairwiseUnits(r *repo, g *provGraph, static map[string]staticEntry, configs string) ([]Unit, error) {
	types, err := os.ReadDir(configs)
	if err != nil {
		return nil, fmt.Errorf("pairwise configs %s: %w (run PairwiseGen first)", configs, err)
	}
	var units []Unit
	shared := append(r.under("tools/pairwise"), r.under("test/TfDockerTest")...)
	for _, t := range types {
		if !t.IsDir() {
			continue
		}
		cfgs, _ := os.ReadDir(filepath.Join(configs, t.Name()))
		prov := g.providerFor([]string{t.Name()})
		for _, c := range cfgs {
			if !c.IsDir() {
				continue
			}
			key := t.Name() + "/" + c.Name()
			u := Unit{Key: key, Extra: map[string]string{"type": t.Name()}}
			u.Files = append(u.Files, shared...)
			u.Files = append(u.Files, prov...)
			u.Files = append(u.Files, r.under("reference/_gen/pairwise/"+t.Name()+".json")...)
			for _, f := range []string{"main.tf", "expect.json"} {
				p := filepath.Join(configs, t.Name(), c.Name(), f)
				if rel, err := filepath.Rel(r.root, p); err == nil && !strings.HasPrefix(rel, "..") {
					p = rel // repo-relative, so the hash does not depend on where the checkout lives
				}
				u.Files = append(u.Files, filepath.ToSlash(p))
			}
			if b, err := os.ReadFile(filepath.Join(configs, t.Name(), c.Name(), "expect.json")); err == nil {
				var e struct {
					Config string `json:"config"`
				}
				if json.Unmarshal(b, &e) == nil {
					u.Extra["config"] = e.Config
				}
			}
			st := static["pairwise:"+key]
			u.Files = append(u.Files, r.globs(st.Inputs)...)
			u.Unknown = st.Unknown
			units = append(units, u)
		}
	}
	return units, nil
}
