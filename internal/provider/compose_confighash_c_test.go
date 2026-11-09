package provider

// Golden values from docker/compose internal/confighash/hash_test.go
// (Apache License 2.0, Copyright 2020 Docker Compose CLI authors): the
// provider's com.docker.compose.config-hash must equal the CLI's.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	ctypes "github.com/compose-spec/compose-go/v2/types"
)

func composeRichServiceFixture() ctypes.ServiceConfig {
	replicas := 3
	a := "1"
	var svc ctypes.ServiceConfig
	svc.Name = "web"
	svc.Image = "nginx:latest"
	svc.Command = ctypes.ShellCommand{"nginx", "-g", "daemon off;"}
	svc.User = "nobody"
	svc.Tty = true
	svc.StdinOpen = true
	svc.Restart = ctypes.RestartPolicyAlways
	svc.Environment = ctypes.MappingWithEquals{"A": &a, "B": nil}
	svc.Labels = ctypes.Labels{"com.example": "v"}
	svc.Annotations = ctypes.Mapping{"note": "x"}
	svc.CapAdd = []string{"NET_ADMIN"}
	svc.ExtraHosts = ctypes.HostsList{"alpha": []string{"10.0.0.1"}}
	svc.Deploy = &ctypes.DeployConfig{Replicas: &replicas}
	svc.Ports = []ctypes.ServicePortConfig{{Target: 80, Published: "8080", Protocol: "tcp"}}
	svc.HealthCheck = &ctypes.HealthCheckConfig{Test: ctypes.HealthCheckTest{"CMD", "true"}}
	svc.Volumes = []ctypes.ServiceVolumeConfig{{Type: ctypes.VolumeTypeVolume, Source: "data", Target: "/data"}}
	svc.Networks = map[string]*ctypes.ServiceNetworkConfig{"default": nil}
	return svc
}

func TestComposeConfigHashGolden(t *testing.T) {
	svc, err := composeServiceConfigHash(composeRichServiceFixture())
	if err != nil {
		t.Fatal(err)
	}
	if svc != "75bc312132c71fac4971d123202631b6978ac9d796336621af744d46611b42a2" {
		t.Errorf("service hash %s", svc)
	}
	if nw := composeNetworkConfigHash(ctypes.NetworkConfig{Name: "proj_default", Driver: "bridge"}); nw != "eeef29d8955b1d4e9382986e213c80789065d989d7a6d035163e0180159aaac0" {
		t.Errorf("network hash %s", nw)
	}
	if vol := composeVolumeConfigHash(ctypes.VolumeConfig{Name: "proj_data"}); vol != "dd3953f0ff20e0f9044086b0483690f2cc2b4653e57aaaa5fa8ea2735da61000" {
		t.Errorf("volume hash %s", vol)
	}
	// Scale and replicas do not move the hash.
	one, two := 1, 2
	a := ctypes.ServiceConfig{Name: "foo", Scale: &one, Deploy: &ctypes.DeployConfig{Replicas: &one}}
	a.Image = "bar"
	b := a
	b.Scale, b.Deploy = &two, &ctypes.DeployConfig{Replicas: &two}
	ha, _ := composeServiceConfigHash(a)
	hb, _ := composeServiceConfigHash(b)
	if ha != hb {
		t.Error("replicas changed the hash")
	}
}

func TestComposePinRootKeyOrder(t *testing.T) {
	in := []byte(`{"profiles":["p"],"command":["c"],"image":"img","user":"u"}`)
	if out, _ := composePinRootKeyOrder(in, composeServiceHashKeyOrder); string(out) != string(in) {
		t.Errorf("identity: %s", out)
	}
	out, _ := composePinRootKeyOrder([]byte(`{"zz_new":"2","image":"img","aa_new":"1"}`), composeServiceHashKeyOrder)
	if string(out) != `{"image":"img","aa_new":"1","zz_new":"2"}` {
		t.Errorf("unknown keys: %s", out)
	}
}

// TestComposeServiceHashKeyOrderCoversStruct fails when a compose-go
// upgrade adds a ServiceConfig attribute the frozen order does not list.
func TestComposeServiceHashKeyOrderCoversStruct(t *testing.T) {
	known := map[string]bool{}
	for _, k := range composeServiceHashKeyOrder {
		known[k] = true
	}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				ft := f.Type
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					walk(ft)
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if !known[name] {
				t.Errorf("ServiceConfig attribute %q is not in composeServiceHashKeyOrder", name)
			}
		}
	}
	walk(reflect.TypeOf(ctypes.ServiceConfig{}))
}

// TestComposeSeam checks seamComposeImpl is this lane's implementation.
func TestComposeSeam(t *testing.T) {
	ctx := context.Background()
	p, err := seamComposeImpl.Load(ctx, seamComposeLoadRequest{Content: "services:\n  web: {image: nginx}\n", WorkingDir: t.TempDir(), ProjectName: "seam"})
	if err != nil {
		t.Fatal(err)
	}
	y, err := seamComposeImpl.Normalize(p)
	if err != nil || !strings.Contains(string(y), "image: nginx") || !strings.Contains(string(y), "name: seam") {
		t.Fatalf("normalize: %v\n%s", err, y)
	}
	h, err := seamComposeImpl.ConfigHash(p, "web")
	if err != nil || len(h) != 64 {
		t.Fatalf("hash %q %v", h, err)
	}
	if want, _ := composeServiceConfigHash(p.Services["web"]); h != want {
		t.Error("ConfigHash differs from composeServiceConfigHash")
	}
	if _, err := seamComposeImpl.ConfigHash(p, "nope"); err == nil {
		t.Error("unknown service: no error")
	}
	if _, err := seamComposeImpl.Load(ctx, seamComposeLoadRequest{}); err == nil {
		t.Error("empty request: no error")
	}
}
