package provider

// Pass 5.1 Lane R: the two forms a raw *_json output takes (reference/CONTRACT.md, "Raw JSON fidelity").
//
//   - Byte-exact: the Engine API response body as received (client.*InspectResult.Raw,
//     client.ImageInspectWithRawResponse, the container stats body). rEngineBody only drops the
//     whitespace around the JSON value: the Engine's encoder appends "\n", and the client's
//     decode-with-raw capture (a TeeReader behind json.Decoder) may or may not have read it, so
//     keeping it would make the hash depend on TCP read boundaries. Every byte of the value itself,
//     including its key order and inner whitespace, is kept.
//   - Re-encoded: where moby client v0.6.2 decodes the body without keeping it (list endpoints,
//     SwarmInspect, DistributionInspect, /auth, /info, /version, /system/df, container changes / top,
//     the path-stat header), rCanonicalJSON re-encodes the decoded value in one canonical form:
//     compact, object keys sorted at every level, numbers written with the text the encoder produced
//     (decoded with UseNumber, so no float64 round trip), HTML characters not escaped (as the Engine
//     writes them), no trailing newline. Two reads of an unchanged object give the same bytes and
//     the same content_sha256. Lists are put in a documented order first (rSortJSONArray, or the
//     data source's own name/ID sort).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// rEngineBody returns the Engine response body b without the JSON whitespace around its value
// (nil for an empty or all-whitespace body). The bytes of the value itself are b's.
func rEngineBody(b []byte) []byte {
	b = bytes.Trim(b, " \t\r\n")
	if len(b) == 0 {
		return nil
	}
	return b
}

// rCanonicalJSON returns v as canonical JSON (see the file comment). A json.RawMessage is
// canonicalized as is; any other value is json.Marshal'ed first, so struct fields keep their
// json tags and omitempty rules.
func rCanonicalJSON(v any) ([]byte, error) {
	var b []byte
	switch x := v.(type) {
	case json.RawMessage:
		b = x
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	val, err := rDecodeNumber(b)
	if err != nil {
		return nil, err
	}
	return rEncodeCanonical(val)
}

// rCanonicalList is rCanonicalJSON for a list output: a nil or null list is written as [].
func rCanonicalList(v any) ([]byte, error) {
	b, err := rCanonicalJSON(v)
	if err == nil && string(b) == "null" {
		return []byte("[]"), nil
	}
	return b, err
}

// rDecodeNumber decodes one JSON value with UseNumber and rejects trailing data.
func rDecodeNumber(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("canonical JSON: trailing data after the value")
	}
	return val, nil
}

// rEncodeCanonical encodes a decoded value (maps are written with sorted keys by encoding/json;
// json.Number keeps its text).
func rEncodeCanonical(val any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(val); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// rSortJSONArray returns items (any slice) as a decoded JSON array sorted by the first of keys each
// element has as a string (e.g. "ID", "Id", "Name", "Path"), then by the element's canonical text, so
// the order no longer depends on the Engine's (map iteration) order. A nil list becomes an empty one.
func rSortJSONArray(items any, keys ...string) ([]any, error) {
	b, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	val, err := rDecodeNumber(b)
	if err != nil {
		return nil, err
	}
	if val == nil {
		return []any{}, nil
	}
	arr, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("canonical JSON: %T is not a list", items)
	}
	type row struct {
		key, text string
		v         any
	}
	rows := make([]row, len(arr))
	for i, e := range arr {
		r := row{v: e}
		if m, ok := e.(map[string]any); ok {
			for _, k := range keys {
				if s, ok := m[k].(string); ok {
					r.key = s
					break
				}
			}
		}
		t, err := rEncodeCanonical(e)
		if err != nil {
			return nil, err
		}
		r.text = string(t)
		rows[i] = r
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].key != rows[j].key {
			return rows[i].key < rows[j].key
		}
		return rows[i].text < rows[j].text
	})
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r.v
	}
	return out, nil
}
