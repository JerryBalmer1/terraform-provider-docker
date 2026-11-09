package provider

// docker_compose: loading a compose project with compose-go. Only the values
// the resource is given reach the loader: no OS environment, no .env lookup
// beyond the env_file list, no remote (git/oci) includes.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	ctypes "github.com/compose-spec/compose-go/v2/types"
)

// composeInputs are the resource attributes that select and shape a project.
type composeInputs struct {
	Content     string
	Files       []string
	ProjectName string
	Profiles    []string
	Env         map[string]string
	EnvFiles    []string
	WorkingDir  string
}

// composeContentFilename is the name content is loaded under (it appears in
// error messages and anchors relative paths).
const composeContentFilename = "docker-compose.yml"

// composeBaseDir is where relative paths resolve: working_dir when set,
// otherwise the first file's directory, otherwise the process working
// directory (Terraform runs providers in the root module directory).
func composeBaseDir(in composeInputs) (string, error) {
	switch {
	case in.WorkingDir != "":
		return filepath.Abs(in.WorkingDir)
	case len(in.Files) > 0:
		abs, err := filepath.Abs(in.Files[0])
		if err != nil {
			return "", err
		}
		return filepath.Dir(abs), nil
	default:
		return os.Getwd()
	}
}

func composeAbs(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

// composeEnvironment builds the interpolation environment: env_file entries
// in order (later files win), then env on top.
func composeEnvironment(base string, in composeInputs) (map[string]string, error) {
	env := map[string]string{}
	for _, f := range in.EnvFiles {
		p := composeAbs(base, f)
		vals, err := dotenv.ReadFile(p, func(k string) (string, bool) {
			if v, ok := in.Env[k]; ok {
				return v, true
			}
			v, ok := env[k]
			return v, ok
		})
		if err != nil {
			return nil, fmt.Errorf("reading env_file %s: %w", f, err)
		}
		for k, v := range vals {
			env[k] = v
		}
	}
	for k, v := range in.Env {
		env[k] = v
	}
	return env, nil
}

// composeLoad parses and normalises the project.
func composeLoad(ctx context.Context, in composeInputs) (*ctypes.Project, error) {
	if (in.Content == "") == (len(in.Files) == 0) {
		return nil, errors.New("exactly one of content or files must be set")
	}
	base, err := composeBaseDir(in)
	if err != nil {
		return nil, fmt.Errorf("resolving working directory: %w", err)
	}
	env, err := composeEnvironment(base, in)
	if err != nil {
		return nil, err
	}
	details := ctypes.ConfigDetails{WorkingDir: base, Environment: env}
	if in.Content != "" {
		details.ConfigFiles = []ctypes.ConfigFile{{Filename: filepath.Join(base, composeContentFilename), Content: []byte(in.Content)}}
	} else {
		for _, f := range in.Files {
			p := composeAbs(base, f)
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("reading compose file: %w", err)
			}
			details.ConfigFiles = append(details.ConfigFiles, ctypes.ConfigFile{Filename: p, Content: b})
		}
	}
	load := func(name string) (*ctypes.Project, error) {
		d := details
		d.Environment = ctypes.Mapping{}
		for k, v := range env {
			d.Environment[k] = v
		}
		// ConfigFiles are reused: copy so the loader's caching cannot leak.
		d.ConfigFiles = append([]ctypes.ConfigFile(nil), details.ConfigFiles...)
		return loader.LoadWithContext(ctx, d, func(o *loader.Options) {
			if name != "" {
				o.SetProjectName(name, true)
			}
			o.Profiles = append([]string(nil), in.Profiles...)
			o.ResolvePaths = true
			o.SkipValidation = false
		})
	}
	p, err := load(in.ProjectName)
	if err != nil && in.ProjectName == "" && strings.Contains(err.Error(), "project name must not be empty") {
		// No project_name and no top-level name: default to the base
		// directory's name like `docker compose` does.
		fallback := loader.NormalizeProjectName(filepath.Base(base))
		if fallback == "" {
			return nil, errors.New("set project_name: the compose file has no top-level name and the working directory name is not a valid project name")
		}
		p, err = load(fallback)
	}
	if err != nil {
		return nil, err
	}
	if len(p.ComposeFiles) == 0 {
		// The loader leaves ComposeFiles to the CLI; record the files (the
		// synthetic docker-compose.yml for content) like it does.
		for _, f := range details.ConfigFiles {
			p.ComposeFiles = append(p.ComposeFiles, f.Filename)
		}
	}
	// Like `docker compose up`: networks and volumes no enabled service uses
	// (e.g. the implicit default network of a profile-disabled service) are
	// not created.
	return p.WithoutUnnecessaryResources(), nil
}

// composeBlobData returns the content of a top-level secret or config.
// External objects return (nil, false, nil).
func composeBlobData(o ctypes.FileObjectConfig, env ctypes.Mapping) ([]byte, bool, error) {
	if o.External {
		return nil, false, nil
	}
	switch {
	case o.File != "":
		b, err := os.ReadFile(o.File)
		if err != nil {
			return nil, false, err
		}
		return b, true, nil
	case o.Content != "":
		return []byte(o.Content), true, nil
	case o.Environment != "":
		if v, ok := o.Extensions[ctypes.SecretConfigXValue].(string); ok {
			return []byte(v), true, nil
		}
		if v, ok := env[o.Environment]; ok {
			return []byte(v), true, nil
		}
		return nil, false, fmt.Errorf("environment variable %s is not set (pass it in env or env_file)", o.Environment)
	}
	return nil, false, errors.New("no file, content or environment source")
}

// composeHash is a stable hex digest of v's JSON encoding (map keys sort).
func composeHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprintf("%#v", v))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func composeSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// composeServiceOrder returns service names so every service comes after
// the services it depends on (depends_on, links, network_mode/ipc/pid
// "service:" refs and volumes_from are all folded into depends_on by the
// loader). Ties sort by name.
func composeServiceOrder(p *ctypes.Project) ([]string, error) {
	state := map[string]int{} // 0 new, 1 visiting, 2 done
	var out []string
	var visit func(string, []string) error
	visit = func(n string, stack []string) error {
		switch state[n] {
		case 1:
			return fmt.Errorf("dependency cycle: %s -> %s", strings.Join(stack, " -> "), n)
		case 2:
			return nil
		}
		state[n] = 1
		svc, ok := p.Services[n]
		if ok {
			for _, d := range composeSortedKeys(svc.DependsOn) {
				if _, enabled := p.Services[d]; !enabled {
					if svc.DependsOn[d].Required {
						return fmt.Errorf("service %s depends on %s, which is not enabled (check profiles)", n, d)
					}
					continue
				}
				if err := visit(d, append(stack, n)); err != nil {
					return err
				}
			}
		}
		state[n] = 2
		out = append(out, n)
		return nil
	}
	for _, n := range composeSortedKeys(p.Services) {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
