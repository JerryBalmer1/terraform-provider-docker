package provider

// Pass 5.3 Lane B: the build-record content docker_buildx_history reads
// from the builder, as `docker buildx history inspect` and `history inspect
// attachment` do: attestation blobs (provenance, SBOM) live in the
// builder's content store, which BuildKit serves next to its control API
// (buildkit control/control.go Controller.Register registers the
// containerd content service over the history namespace; the client
// reaches it with bkclient.Client.ContentClient). Materials come from the
// provenance attestation (SLSA v0.2 `predicate.materials`, v1
// `predicate.buildDefinition.resolvedDependencies`).
//
// The content service's request type lives in
// github.com/containerd/containerd/api, an indirect dependency of this
// module; it is called by reflection on the ContentClient value so go.mod
// keeps it indirect (no new direct dependency).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// bxhMaxBlob bounds one attestation blob read (provenance with max mode
// and SBOMs of large images stay well below it).
const bxhMaxBlob = 16 << 20

// bxhReadContent reads blobs by digest from node's content store; a var so
// unit tests can fake BuildKit.
var bxhReadContent = func(ctx context.Context, pd *providerData, node bkNode, digests []string) (map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*bkDialTimeout)
	defer cancel()
	c, err := bkOpen(ctx, pd, node)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out := map[string][]byte{}
	for _, d := range digests {
		b, err := bxhReadBlob(ctx, c.ContentClient(), d)
		if err != nil {
			return out, fmt.Errorf("reading %s: %w", d, err)
		}
		out[d] = b
	}
	return out, nil
}

// bxhReadBlob calls cc.Read(ctx, &ReadContentRequest{Digest: dgst}) and
// concatenates the streamed ReadContentResponse.Data.
func bxhReadBlob(ctx context.Context, cc any, dgst string) ([]byte, error) {
	m := reflect.ValueOf(cc).MethodByName("Read")
	if !m.IsValid() || m.Type().NumIn() < 2 || m.Type().In(1).Kind() != reflect.Pointer {
		return nil, errors.New("the BuildKit client has no content Read method")
	}
	req := reflect.New(m.Type().In(1).Elem())
	f := req.Elem().FieldByName("Digest")
	if !f.IsValid() || f.Kind() != reflect.String {
		return nil, errors.New("unexpected content Read request type")
	}
	f.SetString(dgst)
	out := m.Call([]reflect.Value{reflect.ValueOf(ctx), req})
	if err, _ := out[1].Interface().(error); err != nil {
		return nil, err
	}
	recv := out[0].MethodByName("Recv")
	if !recv.IsValid() {
		return nil, errors.New("unexpected content Read stream type")
	}
	var buf []byte
	for {
		r := recv.Call(nil)
		if err, _ := r[1].Interface().(error); err != nil {
			if errors.Is(err, io.EOF) {
				return buf, nil
			}
			return buf, err
		}
		if r[0].IsNil() {
			continue
		}
		data := r[0].Elem().FieldByName("Data")
		if data.IsValid() {
			buf = append(buf, data.Bytes()...)
		}
		if len(buf) > bxhMaxBlob {
			return nil, fmt.Errorf("blob larger than %d bytes", bxhMaxBlob)
		}
	}
}

// bxhIsProvenance reports an in-toto provenance predicate type.
func bxhIsProvenance(typ string) bool {
	return strings.HasPrefix(typ, "https://slsa.dev/provenance/")
}

// bxhMaterials extracts the materials of an in-toto provenance statement
// as buildx prints them ({URI, Digests}).
func bxhMaterials(statement []byte) []map[string]any {
	var st struct {
		Predicate struct {
			Materials []struct {
				URI    string            `json:"uri"`
				Digest map[string]string `json:"digest"`
			} `json:"materials"`
			BuildDefinition struct {
				ResolvedDependencies []struct {
					URI    string            `json:"uri"`
					Digest map[string]string `json:"digest"`
				} `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
		} `json:"predicate"`
	}
	out := []map[string]any{}
	if json.Unmarshal(statement, &st) != nil {
		return out
	}
	add := func(uri string, dg map[string]string) {
		ds := make([]string, 0, len(dg))
		for alg, v := range dg {
			ds = append(ds, alg+":"+v)
		}
		sort.Strings(ds)
		out = append(out, map[string]any{"URI": uri, "Digests": ds})
	}
	for _, m := range st.Predicate.Materials {
		add(m.URI, m.Digest)
	}
	for _, m := range st.Predicate.BuildDefinition.ResolvedDependencies {
		add(m.URI, m.Digest)
	}
	return out
}

// bxhMaterialsStructured is materials in the contract's field names.
func bxhMaterialsStructured(ms []map[string]any) []any {
	out := make([]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, map[string]any{"uri": m["URI"], "digests": m["Digests"]})
	}
	return out
}

// bxhPickAttachment selects one attachment by digest (a unique prefix is
// enough) or by type (`provenance`, `sbom`, or a full predicate/media type),
// with an optional platform, like `history inspect attachment`.
func bxhPickAttachment(atts []map[string]any, sel, platform string) (map[string]any, error) {
	var match []map[string]any
	for _, a := range atts {
		dg, _ := a["digest"].(string)
		typ, _ := a["type"].(string)
		plat, _ := a["platform"].(string)
		if platform != "" && plat != "" && plat != platform {
			continue
		}
		switch {
		case strings.HasPrefix(sel, "sha256:") && strings.HasPrefix(dg, sel):
		case sel == "provenance" && bxhIsProvenance(typ):
		case sel == "sbom" && strings.Contains(typ, "spdx"):
		case sel == typ:
		default:
			continue
		}
		match = append(match, a)
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return nil, fmt.Errorf("no attachment %q in the build record", sel)
	}
	return nil, fmt.Errorf("attachment %q is ambiguous (%d match); set attachment_platform or use a digest", sel, len(match))
}
