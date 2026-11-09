package provider

// Pass 5 Lane 0: the action result object (DECISIONS Q35 option A / Q43).
//
// Terraform 1.14 actions have no computed attributes, so an action hands
// its outcome back in two places, both carrying the same object:
//   - a progress event at the end of a successful run;
//   - the JSON file named by the optional `result_path` argument, which a
//     later run reads with jsondecode(file(...)) or the local_file data
//     source.
//
// The object is the action's whole output set from the contract: every
// output of the item keyed by its output name (`result`, identity, version,
// raw outputs such as stdout/stderr, and the hash outputs, which
// actFinishResult computes from the raws). Version values are filled in
// automatically.
//
// Sensitivity: fields the contract marks sensitive (whole raw outputs, or
// single fields of `result`) are replaced by "(sensitive)" in the progress
// message, which ends up in Terraform's UI and logs. They are written in
// full to result_path: that file is the user's own local file, asked for
// explicitly, and the point of it is to hand the values to a later run.
//
// Lane usage:
//
//	type fooActionModel struct {
//		...
//		ResultPath types.String `tfsdk:"result_path"`
//	}
//	Schema:  "result_path": actResultPathAttribute(),
//	Invoke:  out := actFinishResult(ctx, resp, a.pd, "engine:image-prune", map[string]any{
//		"result":         report,           // the API response body (moby struct or map)
//		"images_deleted": deletedIDs,       // identity outputs
//	}, m.ResultPath)

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/action"
	aschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// actResultPathAttr is the name of the result_path argument.
const actResultPathAttr = "result_path"

// actSensitiveMark replaces sensitive values in progress messages.
const actSensitiveMark = "(sensitive)"

// actResultPathAttribute is the optional result_path argument every action
// declares.
func actResultPathAttribute() aschema.StringAttribute {
	return aschema.StringAttribute{
		Optional: true,
		MarkdownDescription: "Local file the action writes its result object to after a successful run. " +
			"The file holds one JSON object keyed by the action's output names (`result`, the identity values, the raw outputs " +
			"with their `*_sha256` hashes, and the version values), with keys sorted at every level, two-space indentation and a " +
			"trailing newline; it is written atomically (temporary file, then rename) and parent directories are created. " +
			"Read it in a later run with `jsondecode(file(...))` or the `local_file` data source. Sensitive values are written " +
			"in full, since this is your own file, but show as `(sensitive)` in the progress message. Nothing is written when the action fails.",
		Validators: []validator.String{actResultPathValidator{}},
	}
}

// actResultPathValidator rejects an empty path and a path that names a
// directory. Unknown and null values pass.
type actResultPathValidator struct{}

func (actResultPathValidator) Description(context.Context) string {
	return "must be a file path (not empty, not a directory)"
}

func (v actResultPathValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (actResultPathValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := actCheckResultPath(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid result_path", err.Error())
	}
}

// actCheckResultPath is the validator's check.
func actCheckResultPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("result_path must not be empty")
	}
	if strings.HasSuffix(p, "/") || strings.HasSuffix(p, `\`) {
		return fmt.Errorf("result_path %q ends with a path separator; name a file", p)
	}
	if fi, err := os.Stat(expandHome(p)); err == nil && fi.IsDir() {
		return fmt.Errorf("result_path %q is a directory; name a file", p)
	}
	return nil
}

// actFinishResult completes a successful action run. values holds the
// action's outputs keyed by output name: a map[string]any, or a struct
// whose JSON field names are the output names (raw outputs must then be
// strings, since encoding/json writes []byte as base64). Raw outputs take
// a string or []byte; `result` and other structured values take the API
// response (a moby struct, map or json.RawMessage) and are mapped to the
// contract's snake_case fields. Hash and version outputs are computed;
// a key that is not an output of itemID is dropped with a warning.
//
// It then sends one progress event, "<type> result: <JSON>" with
// sensitive values redacted, and when resultPath is set writes the full
// object to it (see actResultPathAttribute), adding an error diagnostic if
// the write fails. It returns the object (nil when itemID is unknown).
func actFinishResult(ctx context.Context, resp *action.InvokeResponse, pd *providerData, itemID string, values any, resultPath types.String) map[string]any {
	diags := &diag.Diagnostics{}
	if resp != nil {
		diags = &resp.Diagnostics
	}
	it, ok := contractLookup(itemID)
	if !ok {
		diags.AddError("Provider bug", fmt.Sprintf("actFinishResult: unknown inventory item %q", itemID))
		return nil
	}
	vals, err := actResultValues(values)
	if err != nil {
		diags.AddError("Provider bug", fmt.Sprintf("actFinishResult %s: %v", itemID, err))
		return nil
	}
	in := contractFillInput{Raw: map[string][]byte{}, Structured: map[string]any{}, Result: map[string]any{}, Identity: map[string]any{}}
	known := map[string]bool{}
	for _, o := range it.Outputs {
		known[o.Name] = true
		v, ok := vals[o.Name]
		if !ok {
			continue
		}
		switch o.Category {
		case contractRaw:
			in.Raw[o.Name] = actRawBytes(v)
		case contractStructured:
			in.Structured[o.Name] = v
		case contractResult:
			in.Result[o.Name] = v
		case contractIdentity:
			in.Identity[o.Name] = v
		case contractVersion:
			switch o.Name {
			case "dockerfile_syntax":
				in.DockerfileSyntax, _ = v.(string)
			case "buildkit_version":
				in.BuildKitVersion, _ = v.(string)
			}
		}
	}
	for _, k := range actSortedKeys(vals) {
		if !known[k] {
			diags.AddWarning("Action result value dropped",
				fmt.Sprintf("%s: %q is not an output of %s in the outputs contract. This is a provider bug.", it.Name, k, itemID))
		}
	}
	obj, d := contractCompute(ctx, pd, it, in, func(contractOutput) bool { return true })
	diags.Append(d...)

	redacted := actRedact(it, obj)
	msg, _ := actJSON(redacted, false)
	actProgress(ctx, resp, "%s result: %s", it.Name, strings.TrimSpace(string(msg)))

	if p, ok := knownString(resultPath); ok && p != "" {
		b, err := actJSON(obj, true)
		if err == nil {
			_, err = actWriteFile(p, bytes.NewReader(b), nil)
		}
		if err != nil {
			diags.AddAttributeError(path.Root(actResultPathAttr), "Cannot write result_path",
				fmt.Sprintf("The action succeeded, but its result could not be written to %s: %v", p, err))
		} else {
			tflog.Debug(ctx, "wrote action result", map[string]any{"path": filepath.Clean(p)})
		}
	}
	return obj
}

// actResultValues turns the caller's values into a map by output name.
func actResultValues(values any) (map[string]any, error) {
	switch t := values.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return t, nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("values must be a map or a struct: %w", err)
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out, nil
}

// actRawBytes accepts a raw output as string, []byte or a JSON string.
func actRawBytes(v any) []byte {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return []byte(t)
	case []byte:
		return t
	case json.RawMessage:
		var s string
		if json.Unmarshal(t, &s) == nil {
			return []byte(s)
		}
		return t
	}
	return []byte(fmt.Sprint(v))
}

// actJSON encodes v with sorted keys (every level is a map[string]any),
// without HTML escaping; indent selects two-space indentation. The result
// ends with a newline.
func actJSON(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// actRedact returns a copy of obj with every sensitive value replaced by
// actSensitiveMark (null values stay null).
func actRedact(it *contractItem, obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	for _, o := range it.Outputs {
		v, ok := out[o.Name]
		if !ok || v == nil || o.Category == contractHash || o.Category == contractVersion {
			continue
		}
		out[o.Name] = actRedactNode(contractOutputNode(it, o), v)
	}
	return out
}

func actRedactNode(n *contractNode, v any) any {
	if v == nil {
		return nil
	}
	if n.Sensitive {
		return actSensitiveMark
	}
	switch n.Kind {
	case cnList, cnSet:
		arr, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(arr))
		for i, x := range arr {
			out[i] = actRedactNode(n.Elem, x)
		}
		return out
	case cnMap:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(m))
		for k, x := range m {
			out[k] = actRedactNode(n.Elem, x)
		}
		return out
	case cnObject:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(m))
		for k, x := range m {
			out[k] = x
		}
		for _, a := range n.Attrs {
			out[a.Name] = actRedactNode(a.Node, out[a.Name])
		}
		return out
	}
	return v
}

func actSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
