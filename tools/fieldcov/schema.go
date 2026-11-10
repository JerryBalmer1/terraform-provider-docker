package main

// Provider schema signal: the input attribute paths (Required or Optional,
// write-only included, Computed-only outputs excluded) of every resource,
// action and ephemeral resource, loaded in-process like tools/genskill.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/JerryBalmer1/terraform-provider-docker/internal/provider"
)

// inputPaths maps a Terraform type name to its input attribute paths
// (segments; nested attributes and blocks are walked).
type inputPaths map[string][][]string

func loadInputPaths(ctx context.Context) (inputPaths, error) {
	srv, err := providerserver.NewProtocol6WithError(provider.New("fieldcov")())()
	if err != nil {
		return nil, err
	}
	resp, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		return nil, fmt.Errorf("GetProviderSchema: %w", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return nil, fmt.Errorf("GetProviderSchema: %s: %s", d.Summary, d.Detail)
		}
	}
	out := inputPaths{}
	add := func(name string, s *tfprotov6.Schema) {
		if s == nil || s.Block == nil {
			return
		}
		var paths [][]string
		walkBlock(s.Block, nil, &paths)
		out[name] = paths
	}
	for n, s := range resp.ResourceSchemas {
		add(n, s)
	}
	for n, s := range resp.EphemeralResourceSchemas {
		add(n, s)
	}
	for n, s := range resp.ActionSchemas {
		if s != nil {
			add(n, s.Schema)
		}
	}
	return out, nil
}

func walkBlock(b *tfprotov6.SchemaBlock, prefix []string, out *[][]string) {
	for _, a := range b.Attributes {
		p := append(append([]string{}, prefix...), a.Name)
		if a.NestedType != nil {
			for _, na := range a.NestedType.Attributes {
				walkAttr(na, p, out)
			}
			if a.Required || a.Optional {
				*out = append(*out, p)
			}
			continue
		}
		if a.Required || a.Optional || a.WriteOnly {
			*out = append(*out, p)
		}
	}
	for _, nb := range b.BlockTypes {
		p := append(append([]string{}, prefix...), nb.TypeName)
		*out = append(*out, p)
		walkBlock(nb.Block, p, out)
	}
}

func walkAttr(a *tfprotov6.SchemaAttribute, prefix []string, out *[][]string) {
	p := append(append([]string{}, prefix...), a.Name)
	if a.NestedType != nil {
		if a.Required || a.Optional {
			*out = append(*out, p)
		}
		for _, na := range a.NestedType.Attributes {
			walkAttr(na, p, out)
		}
		return
	}
	if a.Required || a.Optional || a.WriteOnly {
		*out = append(*out, p)
	}
}

// genericLeaves are names too common to map on the leaf alone.
var genericLeaves = map[string]bool{
	"name": true, "type": true, "driver": true, "options": true, "labels": true, "mode": true, "id": true,
	"value": true, "target": true, "source": true, "file": true, "path": true, "key": true, "size": true,
	"count": true, "kind": true, "data": true, "config": true, "spec": true, "version": true, "uid": true,
	"gid": true, "image": true, "ref": true, "condition": true, "delay": true, "window": true, "network": true,
	"networks": true, "aliases": true, "env": true, "command": true, "args": true, "user": true, "test": true,
	"interval": true, "timeout": true, "retries": true, "start_period": true, "start_interval": true,
	"subnet": true, "gateway": true, "ip_range": true, "aux_address": true, "ipv4_address": true,
	"ipv6_address": true, "mac_address": true, "read_only": true, "consistency": true, "propagation": true,
	"no_copy": true, "driver_config": true, "tmpfs_options": true, "limits": true, "reservations": true,
	"external": true, "platform": true, "hostname": true, "description": true, "context": true, "tags": true,
	"pull": true, "force": true, "filters": true, "all": true, "signal": true, "t": true, "v": true,
	"dangling": true, "until": true, "label": true, "keep_storage": true, "max_used_space": true,
	"reserved_space": true, "min_free_space": true, "privileges": true, "remote": true, "tag": true,
	"from_image": true, "from_src": true, "repo": true, "changes": true, "message": true,
}

// wrapperSegs are upstream objects the provider flattens into the parent
// schema (docker_container takes HostConfig fields at the top level,
// docker_service takes ContainerSpec fields at the top level).
var wrapperSegs = map[string]bool{"HostConfig": true, "TaskTemplate": true, "ContainerSpec": true, "Spec": true,
	"ContainerConfig": true, "Config": true}

// onlyWrappers reports whether every segment but the leaf is a wrapper: a
// leaf-only schema match is trusted only then.
func onlyWrappers(segs []string) bool {
	for _, s := range segs[:len(segs)-1] {
		if !wrapperSegs[s] {
			return false
		}
	}
	return true
}

// snake converts an upstream name to the provider's snake_case spelling.
func snake(s string) string {
	s = strings.ReplaceAll(s, "-", "_")
	r := []rune(s)
	var b strings.Builder
	isUp := func(c rune) bool { return c >= 'A' && c <= 'Z' }
	isLow := func(c rune) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }
	for i, c := range r {
		if isUp(c) {
			if i > 0 && r[i-1] != '_' && (isLow(r[i-1]) || (i+1 < len(r) && isLow(r[i+1]) && isUp(r[i-1]) &&
				!(r[i+1] == 's' && (i+2 == len(r) || !isLow(r[i+2]))))) {
				b.WriteByte('_')
			}
			b.WriteRune(c + ('a' - 'A'))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// segEq compares two snake_case segments, tolerating a plural s.
func segEq(a, b string) bool {
	return a == b || a+"s" == b || a == b+"s"
}

// schemaMatch finds the best input attribute of the given types for an
// upstream field path. It returns "" when nothing matches with confidence:
// the attribute path must end in the field's last k segments (k >= 2), or in
// its leaf alone when that leaf is not a generic name or the attribute sits
// at the top level of the schema.
func schemaMatch(ip inputPaths, types []string, segs []string) (tfType, attr string) {
	var fs []string
	for _, s := range segs {
		if s == "[]" || s == "{}" {
			continue
		}
		fs = append(fs, snake(s))
	}
	if len(fs) == 0 {
		return "", ""
	}
	bestK := 0
	for _, t := range types {
		paths := ip[t]
		cands := make([]string, 0, len(paths))
		for _, p := range paths {
			k := 0
			for k < len(p) && k < len(fs) && segEq(p[len(p)-1-k], fs[len(fs)-1-k]) {
				k++
			}
			if k == 0 {
				continue
			}
			ok := k >= 2 || (len(p) == 1 && len(fs) == 1) || (!genericLeaves[fs[len(fs)-1]] && onlyWrappers(segs))
			if !ok {
				continue
			}
			if k > bestK {
				bestK = k
				tfType, attr = t, strings.Join(p, ".")
				cands = cands[:0]
			}
			if k == bestK {
				cands = append(cands, strings.Join(p, "."))
			}
		}
		if len(cands) > 1 && tfType == t {
			sort.Slice(cands, func(i, j int) bool {
				if len(cands[i]) != len(cands[j]) {
					return len(cands[i]) < len(cands[j])
				}
				return cands[i] < cands[j]
			})
			attr = cands[0]
		}
	}
	return tfType, attr
}
