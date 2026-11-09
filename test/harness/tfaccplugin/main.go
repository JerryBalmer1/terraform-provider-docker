// Command tfaccplugin is a TEST HARNESS fixture, not part of the provider: the
// entrypoint of the two managed Docker plugins that test/harness/scripts/
// harness-up builds for the acceptance tests. It needs no kernel module, no
// FUSE and no host mount, so it runs on the dind nodes, and it is built from
// source with `go build`, so the harness never needs Docker Hub for it.
//
//	tfaccplugin volume   a no-op volume driver (docker.volumedriver/1.0) on
//	                     /run/docker/plugins/tfacc.sock: create, remove, get,
//	                     list and path work in memory; mount fails on purpose.
//	tfaccplugin csi      a controller-only CSI driver (docker.csicontroller/1.0
//	                     + docker.csinode/1.0) on /run/docker/plugins/csi.sock:
//	                     enough for a Swarm cluster volume to be created,
//	                     drained and deleted. It cannot publish a volume to a
//	                     task.
//
// The CSI side speaks gRPC with hand-encoded protobuf (protowire) for the few
// csi.v1 messages swarmkit reads, so the module needs no CSI spec dependency.
// Every other csi.v1 method answers with an empty message.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
)

const sockDir = "/run/docker/plugins"

func main() {
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		log.Fatal(err)
	}
	var err error
	switch mode {
	case "volume":
		err = serveVolume(filepath.Join(sockDir, "tfacc.sock"))
	case "csi":
		err = serveCSI(filepath.Join(sockDir, "csi.sock"))
	default:
		err = fmt.Errorf("usage: tfaccplugin volume|csi")
	}
	if err != nil {
		log.Fatal(err)
	}
}

func listen(sock string) (net.Listener, error) {
	_ = os.Remove(sock)
	return net.Listen("unix", sock)
}

func debugf(format string, args ...any) {
	if os.Getenv("DEBUG") == "1" {
		log.Printf(format, args...)
	}
}

// ---------------------------------------------------------------------------
// Volume driver (Docker plugin protocol, JSON over HTTP on a unix socket).

type volumeDriver struct {
	mu   sync.Mutex
	vols map[string]map[string]string
}

func serveVolume(sock string) error {
	d := &volumeDriver{vols: map[string]map[string]string{}}
	l, err := listen(sock)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/vnd.docker.plugins.v1+json")
		_ = json.NewEncoder(w).Encode(v)
	}
	type req struct {
		Name string
		Opts map[string]string
	}
	handle := func(path string, fn func(r req) any) {
		mux.HandleFunc(path, func(w http.ResponseWriter, hr *http.Request) {
			var r req
			_ = json.NewDecoder(hr.Body).Decode(&r)
			debugf("%s %q", path, r.Name)
			reply(w, fn(r))
		})
	}
	handle("/Plugin.Activate", func(req) any { return map[string]any{"Implements": []string{"VolumeDriver"}} })
	handle("/VolumeDriver.Capabilities", func(req) any { return map[string]any{"Capabilities": map[string]string{"Scope": "local"}} })
	handle("/VolumeDriver.Create", func(r req) any {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.vols[r.Name] = r.Opts
		return map[string]string{"Err": ""}
	})
	handle("/VolumeDriver.Remove", func(r req) any {
		d.mu.Lock()
		defer d.mu.Unlock()
		delete(d.vols, r.Name)
		return map[string]string{"Err": ""}
	})
	handle("/VolumeDriver.Get", func(r req) any {
		d.mu.Lock()
		defer d.mu.Unlock()
		if _, ok := d.vols[r.Name]; !ok {
			return map[string]string{"Err": "no such volume"}
		}
		return map[string]any{"Volume": map[string]string{"Name": r.Name}, "Err": ""}
	})
	handle("/VolumeDriver.List", func(req) any {
		d.mu.Lock()
		defer d.mu.Unlock()
		names := make([]string, 0, len(d.vols))
		for n := range d.vols {
			names = append(names, n)
		}
		sort.Strings(names)
		out := make([]map[string]string, 0, len(names))
		for _, n := range names {
			out = append(out, map[string]string{"Name": n})
		}
		return map[string]any{"Volumes": out, "Err": ""}
	})
	handle("/VolumeDriver.Path", func(req) any { return map[string]string{"Mountpoint": "", "Err": ""} })
	handle("/VolumeDriver.Mount", func(req) any {
		return map[string]string{"Err": "tfacc no-op volume plugin: volumes cannot be mounted"}
	})
	handle("/VolumeDriver.Unmount", func(req) any { return map[string]string{"Err": ""} })
	log.Printf("tfaccplugin volume driver on %s", sock)
	return http.Serve(l, mux)
}

// ---------------------------------------------------------------------------
// CSI driver (gRPC, csi.v1), hand-encoded protobuf.

// rawCodec passes message bytes through unchanged: requests arrive as
// *[]byte, responses are []byte built with protowire.
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) {
	switch b := v.(type) {
	case []byte:
		return b, nil
	case *[]byte:
		return *b, nil
	}
	return nil, fmt.Errorf("rawCodec: cannot marshal %T", v)
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("rawCodec: cannot unmarshal into %T", v)
	}
	*p = append((*p)[:0], data...)
	return nil
}

func (rawCodec) Name() string { return "proto" }

// Enum values from csi.proto (csi.v1).
const (
	pluginCapControllerService = 1 // PluginCapability.Service.CONTROLLER_SERVICE
	ctrlCapCreateDeleteVolume  = 1 // ControllerServiceCapability.RPC.CREATE_DELETE_VOLUME
	driverName                 = "tfacc-csi"
	volumeCapacityBytes        = 1 << 20
)

func appendBytesField(b []byte, num protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func appendStringField(b []byte, num protowire.Number, v string) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, v)
}

func appendVarintField(b []byte, num protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

// stringField returns the first string (length-delimited) field num of msg.
func stringField(msg []byte, num protowire.Number) string {
	for len(msg) > 0 {
		n, typ, l := protowire.ConsumeTag(msg)
		if l < 0 {
			return ""
		}
		msg = msg[l:]
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(msg)
			if m < 0 {
				return ""
			}
			if n == num {
				return string(v)
			}
			msg = msg[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(n, typ, msg)
		if m < 0 {
			return ""
		}
		msg = msg[m:]
	}
	return ""
}

func csiResponse(method string, req []byte) []byte {
	switch method {
	case "/csi.v1.Identity/GetPluginInfo":
		// name = 1, vendor_version = 2
		return appendStringField(appendStringField(nil, 1, driverName), 2, "0.0.1")
	case "/csi.v1.Identity/GetPluginCapabilities":
		// capabilities = 1 { service = 1 { type = 1 } }
		svc := appendVarintField(nil, 1, pluginCapControllerService)
		cap1 := appendBytesField(nil, 1, svc)
		return appendBytesField(nil, 1, cap1)
	case "/csi.v1.Identity/Probe":
		// ready = 1 (google.protobuf.BoolValue{value = 1: true})
		return appendBytesField(nil, 1, appendVarintField(nil, 1, 1))
	case "/csi.v1.Controller/ControllerGetCapabilities":
		// capabilities = 1 { rpc = 1 { type = 1 } }
		rpc := appendVarintField(nil, 1, ctrlCapCreateDeleteVolume)
		cap1 := appendBytesField(nil, 1, rpc)
		return appendBytesField(nil, 1, cap1)
	case "/csi.v1.Controller/CreateVolume":
		// CreateVolumeRequest.name = 1 -> volume = 1 { capacity_bytes = 1, volume_id = 2 }
		name := stringField(req, 1)
		vol := appendVarintField(nil, 1, volumeCapacityBytes)
		vol = appendStringField(vol, 2, "tfacc-"+name)
		return appendBytesField(nil, 1, vol)
	case "/csi.v1.Node/NodeGetInfo":
		// node_id = 1
		host, _ := os.Hostname()
		return appendStringField(nil, 1, host)
	}
	// DeleteVolume, NodeGetCapabilities, ... : an empty message.
	return []byte{}
}

func serveCSI(sock string) error {
	l, err := listen(sock)
	if err != nil {
		return err
	}
	//nolint:staticcheck // ForceServerCodec is enough for a byte-passing codec.
	s := grpc.NewServer(grpc.ForceServerCodec(rawCodec{}), grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		var req []byte
		if err := stream.RecvMsg(&req); err != nil {
			return err
		}
		debugf("csi %s", method)
		if !strings.HasPrefix(method, "/csi.v1.") {
			return fmt.Errorf("tfaccplugin: unknown method %s", method)
		}
		return stream.SendMsg(csiResponse(method, req))
	}))
	log.Printf("tfaccplugin CSI driver on %s", sock)
	return s.Serve(l)
}
