package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/client"
)

func bxbTestModel() bxbModel {
	m := bxbModel{
		ID: types.StringUnknown(), Name: types.StringValue("ci"), Node: types.StringUnknown(), Append: types.BoolValue(false),
		Driver: types.StringValue(bxbDriverDockerContainer), Platform: stringListValue([]string{"linux/amd64"}, true),
		BuildkitdFlags: types.StringNull(), BuildkitdConfig: types.StringNull(), BuildkitdConfigContent: types.StringValue("debug = true\n"),
		Bootstrap: types.BoolValue(true), KeepState: types.BoolValue(false), TCPPort: types.Int64Value(11234),
		Endpoint: types.StringUnknown(), TCPAddress: types.StringUnknown(), ContainerName: types.StringUnknown(),
		VolumeName: types.StringUnknown(), Platforms: types.ListUnknown(types.StringType), Labels: types.MapUnknown(types.StringType),
		DockerContainer: &bxbContainerModel{
			Image: types.StringNull(), Network: types.StringValue("host"), Env: stringMapValue(map[string]string{"HTTPS_PROXY": "http://p:3128"}, false),
			RestartPolicy: types.StringNull(), Memory: types.Int64Value(1 << 30), MemorySwap: types.Int64Value(2 << 30),
			CPUQuota: types.Int64Value(50000), CPUPeriod: types.Int64Value(100000), CPUShares: types.Int64Value(512),
			CpusetCpus: types.StringValue("0-1"), CpusetMems: types.StringValue("0"), CgroupParent: types.StringValue("/build"),
			DefaultLoad: types.BoolValue(true),
		},
	}
	m.contractSetUnknown()
	return m
}

func TestBxbBuildCreate(t *testing.T) {
	var diags diag.Diagnostics
	m := bxbTestModel()
	cfg, hc := bxbBuildCreate(context.Background(), m, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if cfg.Image != bxbDefaultImage || cfg.Env[0] != "HTTPS_PROXY=http://p:3128" || cfg.Labels[bxbLabelBuilder] != "ci" || cfg.Labels[labelManagedBy] == "" {
		t.Errorf("config: %+v", cfg)
	}
	if got := strings.Join(cfg.Cmd, " "); got != bxbDefaultFlags+" --addr "+bxbSocket+" --addr tcp://0.0.0.0:1234" {
		t.Errorf("cmd = %s", got)
	}
	if !hc.Privileged || hc.Init == nil || !*hc.Init || hc.RestartPolicy.Name != "unless-stopped" || hc.NetworkMode != "host" || hc.Memory != 1<<30 {
		t.Errorf("host config: %+v", hc)
	}
	if hc.MemorySwap != 2<<30 || hc.CPUQuota != 50000 || hc.CPUPeriod != 100000 || hc.CPUShares != 512 || hc.CpusetCpus != "0-1" ||
		hc.CpusetMems != "0" || hc.CgroupParent != "/build" {
		t.Errorf("resource limits: %+v", hc.Resources)
	}
	if cfg.Labels[bxbLabelPlatforms] != "linux/amd64" || cfg.Labels[bxbLabelDefaultLoad] != "true" {
		t.Errorf("labels: %v", cfg.Labels)
	}
	if len(hc.Mounts) != 1 || hc.Mounts[0].Source != "buildx_buildkit_ci0_state" || hc.Mounts[0].Target != bxbStateDir {
		t.Errorf("mounts: %+v", hc.Mounts)
	}
	if b := hc.PortBindings[network.MustParsePort("1234/tcp")]; len(b) != 1 || b[0].HostPort != "11234" {
		t.Errorf("ports: %+v", hc.PortBindings)
	}

	m = bxbTestModel()
	m.Node = types.StringValue("ci7")
	cfg, hc = bxbBuildCreate(context.Background(), m, &diags)
	if hc.Mounts[0].Source != "buildx_buildkit_ci7_state" {
		t.Errorf("node mount: %+v", hc.Mounts)
	}
	m.BuildkitdFlags, m.TCPPort, m.DockerContainer = types.StringValue(" --debug  --oci-worker-gc=false "), types.Int64Null(), nil
	cfg, hc = bxbBuildCreate(context.Background(), m, &diags)
	if strings.Join(cfg.Cmd, "|") != "--debug|--oci-worker-gc=false" || hc.PortBindings != nil || hc.NetworkMode != "" {
		t.Errorf("custom flags: cmd=%q ports=%v", cfg.Cmd, hc.PortBindings)
	}
}

func TestBxbConfigTar(t *testing.T) {
	b, err := bxbConfigTar([]byte("debug = true\n"), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(b))
	var names []string
	var body string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Typeflag == tar.TypeReg {
			d, _ := io.ReadAll(tr)
			body = string(d)
		}
	}
	if strings.Join(names, ",") != "etc/buildkit/,etc/buildkit/buildkitd.toml" || body != "debug = true\n" {
		t.Errorf("tar: %v %q", names, body)
	}
}

func TestBxbTCPHost(t *testing.T) {
	for in, want := range map[string]string{
		"tcp://10.0.0.5:2376": "10.0.0.5", "ssh://me@build.example.com": "build.example.com",
		"unix:///var/run/docker.sock": "127.0.0.1", "npipe:////./pipe/docker_engine": "127.0.0.1",
	} {
		if got := bxbTCPHost(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// TestBxbDemux checks the exec-attach stream handling used to reach
// buildkitd: stdout frames are the BuildKit byte stream, stderr is dropped,
// writes go to the raw connection.
func TestBxbDemux(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	go func() {
		out := newTestStdWriter(b, stdcopy.Stdout)
		errw := newTestStdWriter(b, stdcopy.Stderr)
		_, _ = out.Write([]byte("hello "))
		_, _ = errw.Write([]byte("noise"))
		_, _ = out.Write([]byte("world"))
		buf := make([]byte, 4)
		_, _ = io.ReadFull(b, buf)
		_, _ = out.Write(buf)
		b.Close()
	}()
	c := bxbDemux(a, a)
	got := make([]byte, 11)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hello world" {
		t.Fatalf("read %q %v", got, err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(c, echo); err != nil || string(echo) != "ping" {
		t.Errorf("echo %q %v", echo, err)
	}
}

// bxbFakeEngine serves the calls docker_buildx_builder makes.
type bxbFakeEngine struct {
	mu       sync.Mutex
	calls    []string
	created  container.CreateRequest
	name     string
	archive  []byte
	volumes  map[string]bool
	running  bool
	removedC bool
	others   []container.Summary
}

func (f *bxbFakeEngine) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	for _, pre := range []string{"/containers/", "/images/", "/volumes"} {
		if i := strings.Index(p, pre); i >= 0 {
			p = p[i:]
			break
		}
	}
	switch {
	case r.Method == http.MethodGet && p == "/containers/json":
		var list []container.Summary
		if f.name != "" && !f.removedC {
			st := container.StateCreated
			if f.running {
				st = container.StateRunning
			}
			list = append(list, container.Summary{ID: "bk0123456789", Names: []string{"/" + f.name}, Labels: f.created.Config.Labels, State: st})
		}
		list = append(list, f.others...)
		_ = json.NewEncoder(w).Encode(list)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/images/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:bk"})
	case r.Method == http.MethodPost && p == "/volumes/create":
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.calls = append(f.calls, "volume:"+body.Name)
		f.volumes[body.Name] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"Name": body.Name})
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/volumes/"):
		n := strings.TrimPrefix(p, "/volumes/")
		f.calls = append(f.calls, "rmvolume:"+n)
		delete(f.volumes, n)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && p == "/containers/create":
		f.name = r.URL.Query().Get("name")
		f.calls = append(f.calls, "create:"+f.name)
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "bk0123456789"})
	case r.Method == http.MethodPut && strings.HasSuffix(p, "/archive"):
		f.calls = append(f.calls, "upload:"+r.URL.Query().Get("path"))
		f.archive, _ = io.ReadAll(r.Body)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/start"):
		f.calls = append(f.calls, "start")
		f.running = true
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/json"):
		if f.removedC {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"No such container"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(container.InspectResponse{

			ID: "bk0123456789", Name: "/" + f.name, State: &container.State{Running: f.running}, HostConfig: f.created.HostConfig,
			Config: f.created.Config, NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{}},
		})
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/containers/"):
		f.calls = append(f.calls, "rmcontainer")
		f.removedC = true
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(w, `{"message":"fake: %s %s"}`, r.Method, p)
	}
}

func TestBuildxBuilderFakeEngine(t *testing.T) {
	ctx := context.Background()
	f := &bxbFakeEngine{volumes: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	host := "tcp://" + srv.Listener.Addr().String()
	c, err := newDockerClient(endpointConfig{Host: host, APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	pd := &providerData{Client: c, Endpoint: endpointConfig{Host: host}}
	var readyFor string
	old := bxbReady
	bxbReady = func(_ context.Context, _ *providerData, id string) (*bkclient.WorkerInfo, error) {
		readyFor = id
		return &bkclient.WorkerInfo{
			BuildkitVersion: bkclient.BuildkitVersion{Version: "v0.20.0"},
			Platforms:       []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm", Variant: "v7"}},
		}, nil
	}
	oldW := bkWorkers
	bkWorkers = func(context.Context, *providerData, bkNode) ([]*bkclient.WorkerInfo, error) {
		return []*bkclient.WorkerInfo{{BuildkitVersion: bkclient.BuildkitVersion{Version: "v0.20.0"}, Labels: map[string]string{"org.mobyproject.buildkit.worker.executor": "oci"},
			Platforms: []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm", Variant: "v7"}}}}, nil
	}
	t.Cleanup(func() { bxbReady = old; bkWorkers = oldW })

	r := &buildxBuilderResource{pd: pd}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	cresp := resource.CreateResponse{State: tfState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan(t, sr, bxbTestModel())}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	if got := strings.Join(f.calls, ","); got != "volume:buildx_buildkit_ci0_state,create:buildx_buildkit_ci0,upload:/,start" {
		t.Errorf("calls = %s", got)
	}
	if readyFor != "bk0123456789" || !f.created.HostConfig.Privileged || len(f.archive) == 0 {
		t.Errorf("ready=%s privileged=%v archive=%d", readyFor, f.created.HostConfig.Privileged, len(f.archive))
	}
	var st bxbModel
	cresp.State.Get(ctx, &st)
	wantTCP := fmt.Sprintf("tcp://%s:11234", bxbTCPHost(host))
	if st.Endpoint.ValueString() != "docker-container://buildx_buildkit_ci0" || st.TCPAddress.ValueString() != wantTCP ||
		st.BuildkitVersion.ValueString() != "v0.20.0" || !st.Platforms.Equal(stringListValue([]string{"linux/amd64", "linux/arm/v7"}, false)) ||
		st.VolumeName.ValueString() != "buildx_buildkit_ci0_state" || st.Node.ValueString() != "ci0" || st.BuildxVersion.ValueString() != contractBuildxVersion {
		t.Fatalf("state = %+v", st)
	}

	if !strings.Contains(st.InspectJSON.ValueString(), `"Name":"ci"`) || st.ContentSHA256.IsNull() || st.Builder.IsNull() {
		t.Errorf("contract: inspect=%s sha=%s", st.InspectJSON.ValueString(), st.ContentSHA256)
	}

	// Refresh keeps everything; import (name only known through the container) recovers the name.
	rresp := resource.ReadResponse{State: tfState(t, sr, &st)}
	r.Read(ctx, resource.ReadRequest{State: tfState(t, sr, &st)}, &rresp)
	var after bxbModel
	rresp.State.Get(ctx, &after)
	if rresp.Diagnostics.HasError() || after.Name.ValueString() != "ci" || after.BuildkitVersion.ValueString() != "v0.20.0" ||
		after.Labels.Elements()["org.mobyproject.buildkit.worker.executor"] == nil {
		t.Errorf("read: %v %+v", rresp.Diagnostics, after)
	}
	// A second builder of the same name needs append; append needs a builder.
	f.others = nil
	again := bxbTestModel()
	again.Node = types.StringValue("ci1")
	if err := r.bxbCheckAppend(ctx, again); err == nil || !strings.Contains(err.Error(), "append = true") {
		t.Errorf("duplicate builder: %v", err)
	}
	again.Append = types.BoolValue(true)
	if err := r.bxbCheckAppend(ctx, again); err != nil {
		t.Errorf("append: %v", err)
	}
	lone := bxbTestModel()
	lone.Name, lone.Append = types.StringValue("other"), types.BoolValue(true)
	if err := r.bxbCheckAppend(ctx, lone); err == nil {
		t.Error("append without builder accepted")
	}

	imp := bxbModel{ID: st.ID}
	bxbFromInspect(&imp, container.InspectResponse{ID: "bk0123456789", Name: "/buildx_buildkit_ci0",
		HostConfig: f.created.HostConfig, Config: f.created.Config}, host)
	if imp.Name.ValueString() != "ci" || imp.VolumeName.ValueString() != "buildx_buildkit_ci0_state" {
		t.Errorf("import: %+v", imp)
	}

	// Destroy removes the container and, without keep_state, the volume.
	f.calls = nil
	dresp := resource.DeleteResponse{State: tfState(t, sr, &st)}
	r.Delete(ctx, resource.DeleteRequest{State: tfState(t, sr, &st)}, &dresp)
	if dresp.Diagnostics.HasError() || strings.Join(f.calls, ",") != "rmcontainer,rmvolume:buildx_buildkit_ci0_state" {
		t.Errorf("delete: %v calls=%v", dresp.Diagnostics, f.calls)
	}
}

func TestBuildxBuilderExamplePlan(t *testing.T) {
	testExamplePlan(t, filepath.Join("resources", "docker_buildx_builder", "resource.tf"))
}

// TestAccBuildxBuilder_basic runs a docker-container builder on the test
// daemon (pulls moby/buildkit, needs privileged containers).
func TestAccBuildxBuilder_basic(t *testing.T) {
	testAccPreCheck(t)
	c := testAccDockerClient(t)
	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if _, err := c.ContainerInspect(context.Background(), "buildx_buildkit_tfacc0", client.ContainerInspectOptions{}); !isNotFound(err) {
				return fmt.Errorf("builder container still exists (%v)", err)
			}
			if _, err := c.VolumeInspect(context.Background(), "buildx_buildkit_tfacc0_state", client.VolumeInspectOptions{}); !isNotFound(err) {
				return fmt.Errorf("builder state volume still exists (%v)", err)
			}
			return nil
		},
		Steps: []tfresource.TestStep{
			{
				Config: `
resource "docker_buildx_builder" "b" {
  name                     = "tfacc"
  buildkitd_config_content = "debug = true\n"
}`,
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("docker_buildx_builder.b", "endpoint", "docker-container://buildx_buildkit_tfacc0"),
					tfresource.TestCheckResourceAttrSet("docker_buildx_builder.b", "buildkit_version"),
					tfresource.TestCheckResourceAttrSet("docker_buildx_builder.b", "platforms.0"),
				),
			},
			{
				ResourceName:            "docker_buildx_builder.b",
				ImportState:             true,
				ImportStateId:           "tfacc",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"buildkitd_config_content", "buildkit_version", "platforms", "labels", "builder", "inspect_json", "content_sha256"},
			},
		},
	})
}
