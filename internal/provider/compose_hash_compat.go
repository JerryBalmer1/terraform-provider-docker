package provider

// docker_compose stamps every object it creates with a config hash label and
// recreates the object when the hash changes. The hash is the JSON of the
// create options, so it depends on how the Engine API types serialize. The
// move from github.com/docker/docker v28 to github.com/moby/moby/api changed
// that serialization without changing what is sent to the daemon:
//   - network create options gained EnableIPv4/EnableIPv6 without omitempty,
//     and ConfigFrom became a string;
//   - volume create options reordered their fields;
//   - container.Config.OnBuild gained omitempty.
// Hashing the new types directly would recreate every compose network,
// volume and container (losing volume data) on the first apply after an
// upgrade. These shapes reproduce the v28 JSON exactly, so existing objects
// keep their hash. compose_hash_compat_test.go pins known v28 digests.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// composeNetworkHashShape is docker/docker v28 network.CreateOptions.
type composeNetworkHashShape struct {
	Driver     string
	Scope      string
	EnableIPv4 *bool `json:",omitempty"`
	EnableIPv6 *bool `json:",omitempty"`
	IPAM       *network.IPAM
	Internal   bool
	Attachable bool
	Ingress    bool
	ConfigOnly bool
	ConfigFrom *composeConfigReference
	Options    map[string]string
	Labels     map[string]string
}

type composeConfigReference struct {
	Network string
}

// composeNetworkHash is the config hash of a compose network.
func composeNetworkHash(o client.NetworkCreateOptions) string {
	s := composeNetworkHashShape{
		Driver: o.Driver, Scope: o.Scope, EnableIPv4: o.EnableIPv4, EnableIPv6: o.EnableIPv6, IPAM: o.IPAM,
		Internal: o.Internal, Attachable: o.Attachable, Ingress: o.Ingress, ConfigOnly: o.ConfigOnly,
		Options: o.Options, Labels: o.Labels,
	}
	if o.ConfigFrom != "" {
		s.ConfigFrom = &composeConfigReference{Network: o.ConfigFrom}
	}
	return composeHash(s)
}

// composeVolumeHashShape is docker/docker v28 volume.CreateOptions.
type composeVolumeHashShape struct {
	ClusterVolumeSpec *volume.ClusterVolumeSpec `json:"ClusterVolumeSpec,omitempty"`
	Driver            string                    `json:"Driver,omitempty"`
	DriverOpts        map[string]string         `json:"DriverOpts,omitempty"`
	Labels            map[string]string         `json:"Labels,omitempty"`
	Name              string                    `json:"Name,omitempty"`
}

// composeVolumeHash is the config hash of a compose volume.
func composeVolumeHash(o client.VolumeCreateOptions) string {
	return composeHash(composeVolumeHashShape{
		ClusterVolumeSpec: o.ClusterVolumeSpec, Driver: o.Driver, DriverOpts: o.DriverOpts, Labels: o.Labels, Name: o.Name,
	})
}

// composeHashConfig serializes a container.Config the v28 way: OnBuild is
// always present (null when unset), just before Labels.
type composeHashConfig struct{ *container.Config }

func (c composeHashConfig) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(c.Config)
	if err != nil || c.Config == nil || c.Config.OnBuild != nil {
		return b, err
	}
	return jsonInsertBefore(b, "Labels", "OnBuild", json.RawMessage("null"))
}

// jsonInsertBefore inserts key:val into the top-level JSON object obj just
// before the member named before (at the end when there is none). Member
// values are copied byte for byte.
func jsonInsertBefore(obj []byte, before, key string, val json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("jsonInsertBefore: not a JSON object")
	}
	var out bytes.Buffer
	out.WriteByte('{')
	n := 0
	member := func(k string, v []byte) {
		if n > 0 {
			out.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		out.Write(kb)
		out.WriteByte(':')
		out.Write(v)
		n++
	}
	inserted := false
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if k == before && !inserted {
			member(key, val)
			inserted = true
		}
		member(k, raw)
	}
	if !inserted {
		member(key, val)
	}
	if _, err := dec.Token(); err != nil && err != io.EOF {
		return nil, err
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}
