package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ function.Function = (*composeEnvFunction)(nil)

// NewComposeEnvFunction returns provider::docker::compose_env.
func NewComposeEnvFunction() function.Function { return &composeEnvFunction{} }

type composeEnvFunction struct{}

func (f *composeEnvFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "compose_env"
}

func (f *composeEnvFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary: "Turn a map of environment variables into a sorted list of KEY=VALUE strings.",
		MarkdownDescription: "Turns a map of environment variables into a list of `KEY=VALUE` strings sorted by key, the form of env files, compose `environment` lists " +
			"and the Engine API `Env` field. A null value yields a bare `KEY` (inherited, as in compose). " +
			"Empty keys and keys containing `=` are an error.",
		Parameters: []function.Parameter{
			function.MapParameter{Name: "env", ElementType: types.StringType, MarkdownDescription: "Variables, e.g. `{ LOG_LEVEL = \"info\" }`."},
		},
		Return: function.ListReturn{ElementType: types.StringType},
	}
}

// composeEnv renders env sorted by key; a nil value pointer means a bare key.
func composeEnv(env map[string]*string) ([]string, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		if k == "" {
			return nil, fmt.Errorf("environment variable names must not be empty")
		}
		if strings.Contains(k, "=") {
			return nil, fmt.Errorf("environment variable name %q must not contain '='", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if env[k] == nil {
			out = append(out, k)
		} else {
			out = append(out, k+"="+*env[k])
		}
	}
	return out, nil
}

func (f *composeEnvFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var env map[string]*string
	resp.Error = req.Arguments.Get(ctx, &env)
	if resp.Error != nil {
		return
	}
	out, err := composeEnv(env)
	if err != nil {
		resp.Error = function.NewArgumentFuncError(0, err.Error())
		return
	}
	resp.Error = resp.Result.Set(ctx, out)
}
