package provider

// Pass 6c lane S: dropped values fail acceptance tests, and config_sha256.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// contractDroppedSummary starts every "a value did not fit its contract
// type" diagnostic. ExampleTests and PairwiseTest grep for it: keep it.
const contractDroppedSummary = "Outputs contract value dropped"

// contractDropped records a value the contract could not represent. It is a
// warning for users, and an error when TF_ACC is set, so an acceptance test
// can never pass while a structured output silently loses a value.
func contractDropped(diags *diag.Diagnostics, it *contractItem, msg string) {
	detail := fmt.Sprintf("%s (%s): %s", it.Name, it.ID, msg)
	if os.Getenv("TF_ACC") != "" {
		diags.AddError(contractDroppedSummary, detail+" (an error because TF_ACC is set: the structured output does not mirror the Engine response)")
		return
	}
	diags.AddWarning(contractDroppedSummary, detail)
}

// contractConfigHashOutput is the hash output computed from the
// configuration part of the raw inspect instead of all of it.
const contractConfigHashOutput = "config_sha256"

// contractConfigSelect picks the configuration part of a decoded inspect,
// per Terraform type. Runtime state (State, health logs, timestamps, PIDs,
// daemon-assigned addresses, attached containers, usage) is never selected.
var contractConfigSelect = map[string]func(map[string]any) map[string]any{
	"docker_container": func(m map[string]any) map[string]any {
		out := contractPick(m, "Name", "Image", "Platform", "Config", "HostConfig")
		if ns, ok := m["NetworkSettings"].(map[string]any); ok {
			if nets, ok := ns["Networks"].(map[string]any); ok {
				sel := map[string]any{}
				for name, ep := range nets {
					epm, _ := ep.(map[string]any)
					sel[name] = contractPick(epm, "IPAMConfig", "Links", "Aliases", "DriverOpts", "GwPriority")
				}
				out["Networks"] = sel
			}
		}
		return out
	},
	"docker_service": func(m map[string]any) map[string]any {
		return contractPick(m, "Spec")
	},
	"docker_network": func(m map[string]any) map[string]any {
		return contractPick(m, "Name", "Driver", "Scope", "EnableIPv4", "EnableIPv6", "IPAM", "Internal",
			"Attachable", "Ingress", "ConfigFrom", "ConfigOnly", "Options", "Labels")
	},
	"docker_volume": func(m map[string]any) map[string]any {
		out := contractPick(m, "Name", "Driver", "Scope", "Labels", "Options")
		if cv, ok := m["ClusterVolume"].(map[string]any); ok {
			out["ClusterVolume"] = contractPick(cv, "Spec")
		}
		return out
	},
}

func contractPick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// contractConfigHash returns the hex sha256 of the canonical JSON of the
// configuration part of raw (nil = null).
func contractConfigHash(it *contractItem, raw []byte, warn func(string)) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	sel, ok := contractConfigSelect[it.Name]
	if !ok {
		warn(contractConfigHashOutput + ": no configuration selector for " + it.Name)
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		warn(contractConfigHashOutput + ": " + err.Error())
		return nil
	}
	b, err := rCanonicalJSON(sel(m))
	if err != nil {
		warn(contractConfigHashOutput + ": " + err.Error())
		return nil
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// contractUpgradeNullOutputs rewrites a prior state JSON so that every
// contract model output of itemID (raw, structured, hash, version, result)
// is null. Pass 6c changed the shape of structured outputs (JSON strings and
// dynamic values became nested attributes, list(string) became maps), and
// terraform-plugin-framework cannot decode prior state whose stored value has
// the old type ("invalid JSON, expected "{"..."). The outputs are Computed and
// rewritten by the next Read, so a resource's StateUpgrader (schema version
// bump) hands its raw state through this function and sets
// resp.DynamicValue = &tfprotov6.DynamicValue{JSON: out}.
func contractUpgradeNullOutputs(itemID string, raw []byte) ([]byte, error) {
	var st map[string]json.RawMessage
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decoding prior state: %w", err)
	}
	if st == nil {
		return nil, fmt.Errorf("prior state is null")
	}
	it := contractMustItem(itemID)
	for _, o := range contractModelOutputs(it) {
		st[o.Name] = json.RawMessage("null")
	}
	return json.Marshal(st)
}
