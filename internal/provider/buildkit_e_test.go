package provider

// Lane E unit tests: a fake BuildKit control API (gRPC) behind bkOpen, and
// the docker_buildx_* data sources, the prune seam and the build helpers.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	controlapi "github.com/moby/buildkit/api/services/control"
	apitypes "github.com/moby/buildkit/api/types"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/moby/api/types/container"
	"github.com/tonistiigi/fsutil"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// bkFake is a fake BuildKit control service.
type bkFake struct {
	controlapi.UnimplementedControlServer
	mu      sync.Mutex
	records []*controlapi.BuildHistoryRecord
	usage   []*controlapi.UsageRecord
	prune   *controlapi.PruneRequest
	duReq   *controlapi.DiskUsageRequest
}

func (f *bkFake) ListWorkers(context.Context, *controlapi.ListWorkersRequest) (*controlapi.ListWorkersResponse, error) {
	return &controlapi.ListWorkersResponse{Record: []*apitypes.WorkerRecord{{
		ID: "w0", Labels: map[string]string{"org.mobyproject.buildkit.worker.executor": "oci"},
		Platforms:       []*pb.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}},
		GCPolicy:        []*apitypes.GCPolicy{{All: true, KeepDuration: int64(48 * time.Hour), ReservedSpace: 1 << 30, Filters: []string{"type==source.local"}}},
		BuildkitVersion: &apitypes.BuildkitVersion{Package: "github.com/moby/buildkit", Version: "v0.34.2"},
	}}}, nil
}

func (f *bkFake) DiskUsage(_ context.Context, req *controlapi.DiskUsageRequest) (*controlapi.DiskUsageResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.duReq = req
	return &controlapi.DiskUsageResponse{Record: f.usage}, nil
}

func (f *bkFake) Prune(req *controlapi.PruneRequest, s grpc.ServerStreamingServer[controlapi.UsageRecord]) error {
	f.mu.Lock()
	f.prune = req
	usage := f.usage
	f.mu.Unlock()
	for _, u := range usage {
		if u.InUse {
			continue
		}
		if err := s.Send(u); err != nil {
			return err
		}
	}
	return nil
}

func (f *bkFake) ListenBuildHistory(req *controlapi.BuildHistoryRequest, s grpc.ServerStreamingServer[controlapi.BuildHistoryEvent]) error {
	for _, r := range f.records {
		if req.Ref != "" && req.Ref != r.Ref {
			continue
		}
		if err := s.Send(&controlapi.BuildHistoryEvent{Type: controlapi.BuildHistoryEventType_COMPLETE, Record: r}); err != nil {
			return err
		}
	}
	return nil
}

// newBKFake serves a fake BuildKit and points bkOpen at it; opened records
// the nodes dialled.
func newBKFake(t *testing.T) (*bkFake, *[]string) {
	t.Helper()
	f := &bkFake{}
	srv := grpc.NewServer()
	controlapi.RegisterControlServer(srv, f)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)
	var mu sync.Mutex
	var opened []string
	old := bkOpen
	bkOpen = func(ctx context.Context, _ *providerData, node bkNode) (*bkclient.Client, error) {
		mu.Lock()
		opened = append(opened, node.Builder+"/"+node.Name)
		mu.Unlock()
		return bkclient.New(ctx, "tcp://"+l.Addr().String())
	}
	t.Cleanup(func() { bkOpen = old })
	return f, &opened
}

// bkFakeEngineNodes registers a docker-container builder "ci" (running
// node ci0, stopped node ci1) and a foreign one ("legacy0", no label).
func bkFakeEngineNodes(f *p3eFake) {
	f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.json("GET /containers/json", []container.Summary{
		{ID: "c0", Names: []string{"/buildx_buildkit_ci0"}, State: container.StateRunning, Command: "buildkitd --debug",
			Labels: map[string]string{bxbLabelBuilder: "ci", bxbLabelPlatforms: "linux/riscv64"}, Created: 1700000000},
		{ID: "c1", Names: []string{"/buildx_buildkit_ci1"}, State: container.StateExited,
			Labels: map[string]string{bxbLabelBuilder: "ci"}, Created: 1700000100},
		{ID: "c2", Names: []string{"/buildx_buildkit_legacy0"}, State: container.StateRunning},
		{ID: "c3", Names: []string{"/not_a_builder"}, State: container.StateRunning},
	})
	inspect := func(id, name string, running bool, labels map[string]string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			st := &container.State{Running: running, Status: container.StateExited}
			if running {
				st.Status = container.StateRunning
			}
			p3eWriteJSON(w, http.StatusOK, container.InspectResponse{ID: id, Name: "/" + name, State: st,
				Config:     &container.Config{Image: "moby/buildkit:buildx-stable-1", Cmd: []string{"--debug"}, Env: []string{"PATH=/bin", "HTTPS_PROXY=http://p"}, Labels: labels},
				HostConfig: &container.HostConfig{NetworkMode: "host", Memory: 1 << 30, RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}}})
		}
	}
	f.on("GET /containers/c0/json", inspect("c0", "buildx_buildkit_ci0", true, map[string]string{bxbLabelBuilder: "ci", bxbLabelPlatforms: "linux/riscv64", bxbLabelDefaultLoad: "true"}))
	f.on("GET /containers/c1/json", inspect("c1", "buildx_buildkit_ci1", false, map[string]string{bxbLabelBuilder: "ci"}))
	f.on("GET /containers/c2/json", inspect("c2", "buildx_buildkit_legacy0", true, nil))
}

func TestBKListNodes(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	nodes, err := bkListNodes(context.Background(), pd)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range nodes {
		got = append(got, n.Builder+"/"+n.Name+"/"+n.Status+"/"+n.Driver)
	}
	want := "default/default/running/docker,ci/ci0/running/docker-container,ci/ci1/stopped/docker-container,legacy/legacy0/running/docker-container"
	if strings.Join(got, ",") != want {
		t.Errorf("nodes = %v", got)
	}
	if nodes[1].Endpoint != "docker-container://buildx_buildkit_ci0" || strings.Join(nodes[1].Flags, " ") != "--debug" {
		t.Errorf("node ci0 = %+v", nodes[1])
	}
	if _, err := bkBuilderNodes(context.Background(), pd, "nope"); err == nil || !strings.Contains(err.Error(), "no buildx builder") {
		t.Errorf("unknown builder: %v", err)
	}
	if n, err := bkNodeForRef(context.Background(), pd, seamBuilderRef{Endpoint: "tcp://10.0.0.1:1234"}); err != nil || n.Driver != bkDriverRemote {
		t.Errorf("tcp endpoint: %+v %v", n, err)
	}
	if n, err := bkNodeForRef(context.Background(), pd, seamBuilderRef{Endpoint: "docker-container://buildx_buildkit_x0"}); err != nil || n.Builder != "x0" || n.ContainerName != "buildx_buildkit_x0" {
		t.Errorf("docker-container endpoint: %+v %v", n, err)
	}
	if n, err := bkNodeForRef(context.Background(), pd, seamBuilderRef{Name: "ci"}); err != nil || n.Name != "ci0" {
		t.Errorf("by name: %+v %v", n, err)
	}
}

func TestBuildxBuilderDataSource(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	_, opened := newBKFake(t)
	st, diags := p3eReadDS(t, NewBuildxBuilderDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("ci")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m bxdsBuilderModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	if m.Driver.ValueString() != bxbDriverDockerContainer || m.Endpoint.ValueString() != "docker-container://buildx_buildkit_ci0" ||
		m.BuildkitVersion.ValueString() != "v0.34.2" || m.BuildxVersion.ValueString() != contractBuildxVersion {
		t.Errorf("model: %+v", m)
	}
	var plats []string
	m.Platforms.ElementsAs(context.Background(), &plats, false)
	if strings.Join(plats, ",") != "linux/riscv64,linux/amd64,linux/arm64" {
		t.Errorf("platforms = %v", plats)
	}
	var doc bxBuilderView
	if err := json.Unmarshal([]byte(m.InspectJSON.ValueString()), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Nodes) != 2 || doc.Nodes[1].Status != bkStatusStopped || doc.Nodes[0].DriverOpts["env.HTTPS_PROXY"] != "http://p" ||
		doc.Nodes[0].DriverOpts["default-load"] != "true" || doc.Nodes[0].DriverOpts["network"] != "host" || len(doc.Nodes[0].GCPolicy) != 1 ||
		doc.Nodes[0].GCPolicy[0].KeepDuration != "48h0m0s" || doc.LastActivity == "" {
		t.Errorf("inspect = %s", m.InspectJSON.ValueString())
	}
	if strings.Join(*opened, ",") != "ci/ci0" {
		t.Errorf("dialled %v (stopped nodes must not be dialled)", *opened)
	}
	if m.Builder.IsNull() || m.ContentSHA256.IsNull() {
		t.Error("contract outputs not filled")
	}

	// Default builder: no container, BuildKit over /grpc.
	st, diags = p3eReadDS(t, NewBuildxBuilderDataSource(), pd, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	st.Get(context.Background(), &m)
	if m.Name.ValueString() != "default" || m.Driver.ValueString() != bkDriverDocker || m.Endpoint.ValueString() != "default" {
		t.Errorf("default: %+v", m)
	}

	// Unknown builder.
	if _, diags = p3eReadDS(t, NewBuildxBuilderDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("nope")}); !diags.HasError() {
		t.Error("unknown builder accepted")
	}
}

func TestBuildxBuildersDataSource(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	newBKFake(t)
	st, diags := p3eReadDS(t, NewBuildxBuildersDataSource(), pd, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m bxdsBuildersModel
	st.Get(context.Background(), &m)
	var names []string
	m.Names.ElementsAs(context.Background(), &names, false)
	if strings.Join(names, ",") != "default,ci,legacy" {
		t.Errorf("names = %v", names)
	}
	eps := m.Endpoints.Elements()
	if p3eAttrString(eps["ci"]) != "docker-container://buildx_buildkit_ci0" || p3eAttrString(eps["default"]) != "default" {
		t.Errorf("endpoints = %v", eps)
	}
	if len(m.Builders.Elements()) != 3 || !strings.Contains(m.BuildersJSON.ValueString(), `"Name":"legacy"`) {
		t.Errorf("builders: %s", m.BuildersJSON.ValueString())
	}
}

func TestBuildxDiskUsageDataSource(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	bk, _ := newBKFake(t)
	now := timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	bk.usage = []*controlapi.UsageRecord{
		{ID: "small", Size: 10, InUse: true, CreatedAt: now, RecordType: "regular", Shared: true},
		{ID: "big", Size: 1000, CreatedAt: now, LastUsedAt: now, RecordType: "source.local", Mutable: true, UsageCount: 3, Parents: []string{"small"}},
	}
	st, diags := p3eReadDS(t, NewBuildxDiskUsageDataSource(), pd, map[string]tftypes.Value{
		"builder": p3eStr("ci"), "filters": p3eStrMap(map[string]string{"type": "regular", "description": "~pulled"}),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m bxduModel
	st.Get(context.Background(), &m)
	var ids []string
	m.IDs.ElementsAs(context.Background(), &ids, false)
	if strings.Join(ids, ",") != "big,small" {
		t.Errorf("ids = %v", ids)
	}
	if got := strings.Join(bk.duReq.Filter, ","); got != "description~=pulled,type==regular" {
		t.Errorf("filters = %s", got)
	}
	sum := m.Summary.Attributes()
	if sum["total"].String() != "1010" || sum["reclaimable"].String() != "1000" || sum["shared"].String() != "10" || sum["private"].String() != "1000" {
		t.Errorf("summary = %v", sum)
	}
	if len(m.Records.Elements()) != 2 || m.ContentSHA256.IsNull() {
		t.Errorf("records = %v", m.Records)
	}
	// Default builder, no records.
	bk.usage = nil
	if _, diags := p3eReadDS(t, NewBuildxDiskUsageDataSource(), pd, nil); diags.HasError() {
		t.Error(diags)
	}
}

func bkTestRecords() []*controlapi.BuildHistoryRecord {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	return []*controlapi.BuildHistoryRecord{
		{Ref: "aaa111", CreatedAt: timestamppb.New(t0), CompletedAt: timestamppb.New(t0.Add(3 * time.Second)),
			FrontendAttrs: map[string]string{"context": "/src/app", "filename": "Dockerfile", "target": "prod", "platform": "linux/amd64,linux/arm64",
				"vcs:source": "https://github.com/acme/app.git", "vcs:revision": "deadbeef", "build-arg:FOO": "bar", "image-resolve-mode": "pull",
				"build-arg:BUILDKIT_SYNTAX": "docker/dockerfile:1.7"},
			NumTotalSteps: 10, NumCompletedSteps: 10, NumCachedSteps: 4,
			Result: &controlapi.BuildResultInfo{Attestations: []*controlapi.Descriptor{{Digest: "sha256:att", MediaType: "application/vnd.in-toto+json",
				Annotations: map[string]string{"in-toto.io/predicate-type": "https://slsa.dev/provenance/v0.2"}}}}},
		{Ref: "bbb222", CreatedAt: timestamppb.New(t0.Add(time.Hour)), CompletedAt: timestamppb.New(t0.Add(time.Hour + time.Second)),
			FrontendAttrs: map[string]string{"context": "/src/tool", "filename": "build/Dockerfile.ci"}, Error: &spb.Status{Code: 2, Message: "boom"}},
		{Ref: "aab333", CreatedAt: timestamppb.New(t0.Add(-time.Hour)), FrontendAttrs: map[string]string{"build-arg:BUILDKIT_BUILD_NAME": "named"}},
	}
}

func TestBuildxHistoryDataSources(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	bk, _ := newBKFake(t)
	bk.records = bkTestRecords()

	st, diags := p3eReadDS(t, NewBuildxHistoryRecordsDataSource(), pd, map[string]tftypes.Value{"builder": p3eStr("ci")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var lm bxhListModel
	st.Get(context.Background(), &lm)
	var refs []string
	lm.Refs.ElementsAs(context.Background(), &refs, false)
	if strings.Join(refs, ",") != "bbb222,aaa111,aab333" {
		t.Errorf("refs = %v", refs)
	}
	var raw []map[string]any
	json.Unmarshal([]byte(lm.HistoryJSON.ValueString()), &raw)
	if raw[0]["status"] != "Error" || raw[0]["name"] != "tool/Dockerfile.ci" || raw[1]["name"] != "app prod" || raw[2]["status"] != "Running" || raw[2]["name"] != "named" {
		t.Errorf("history = %s", lm.HistoryJSON.ValueString())
	}
	st, diags = p3eReadDS(t, NewBuildxHistoryRecordsDataSource(), pd, map[string]tftypes.Value{"limit": tftypes.NewValue(tftypes.Number, 1)})
	if diags.HasError() {
		t.Fatal(diags)
	}
	st.Get(context.Background(), &lm)
	if len(lm.Refs.Elements()) != 1 || lm.Builder.ValueString() != "default" {
		t.Errorf("limit: %v %s", lm.Refs, lm.Builder)
	}

	for ref, want := range map[string]string{"": "bbb222", "^1": "aaa111", "aaa": "aaa111", "ci/ci0/aab333": "aab333"} {
		st, diags := p3eReadDS(t, NewBuildxHistoryDataSource(), pd, map[string]tftypes.Value{"builder": p3eStr("ci"), "ref": p3eStr(ref)})
		if diags.HasError() {
			t.Fatalf("%q: %v", ref, diags)
		}
		var im bxhInspectModel
		st.Get(context.Background(), &im)
		if im.Ref.ValueString() != want {
			t.Errorf("ref %q -> %s, want %s", ref, im.Ref.ValueString(), want)
		}
		if want == "aaa111" {
			rec := im.Record.Attributes()
			var plats []string
			im.Platforms.ElementsAs(context.Background(), &plats, false)
			if im.VCSRevision.ValueString() != "deadbeef" || strings.Join(plats, ",") != "linux/amd64,linux/arm64" ||
				im.DockerfileSyntax.ValueString() != "docker/dockerfile:1.7" || rec["status"].String() != `"Completed"` ||
				rec["num_cached_steps"].String() != "4" || !strings.Contains(im.InspectJSON.ValueString(), "slsa.dev/provenance") {
				t.Errorf("inspect: %s", im.InspectJSON.ValueString())
			}
		}
	}
	for _, bad := range []string{"aa", "zzz", "^9", "^x"} {
		if _, diags := p3eReadDS(t, NewBuildxHistoryDataSource(), pd, map[string]tftypes.Value{"builder": p3eStr("ci"), "ref": p3eStr(bad)}); !diags.HasError() {
			t.Errorf("ref %q accepted", bad)
		}
	}
}

func TestBuildxPruneSeam(t *testing.T) {
	f, pd := newP3eFake(t)
	bkFakeEngineNodes(f)
	bk, opened := newBKFake(t)
	bk.usage = []*controlapi.UsageRecord{{ID: "r1", Size: 100}, {ID: "r0", Size: 50}, {ID: "busy", Size: 7, InUse: true}}
	if _, ok := seamBuildxPruneImpl.(laneEBuildxPrune); !ok {
		t.Fatalf("seamBuildxPruneImpl = %T", seamBuildxPruneImpl)
	}
	res, err := seamBuildxPruneImpl.Prune(context.Background(), pd, seamBuildxPruneRequest{
		Builder: "ci", All: true, Filters: map[string][]string{"until": {"24h"}, "type": {"regular"}, "description": {"~git"}},
		KeepStorage: 1 << 20, MaxUsedSpace: 1 << 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.CachesDeleted, ",") != "r0,r1" || res.SpaceReclaimed != 150 {
		t.Errorf("result = %+v", res)
	}
	p := bk.prune
	if !p.All || p.KeepDuration != int64(24*time.Hour) || p.ReservedSpace != 1<<20 || p.MaxUsedSpace != 1<<30 ||
		strings.Join(p.Filter, ",") != "description~=git,type==regular" {
		t.Errorf("request = %+v", p)
	}
	if strings.Join(*opened, ",") != "ci/ci0" {
		t.Errorf("dialled %v", *opened)
	}
	if _, err := seamBuildxPruneImpl.Prune(context.Background(), pd, seamBuildxPruneRequest{Builder: "ci", Filters: map[string][]string{"until": {"soon"}}}); err == nil {
		t.Error("bad until accepted")
	}
	if _, err := seamBuildxPruneImpl.Prune(context.Background(), pd, seamBuildxPruneRequest{Builder: "nope"}); err == nil {
		t.Error("unknown builder accepted")
	}
}

func TestBKFrontendAttrs(t *testing.T) {
	a, err := bkFrontendAttrs(seamBuildRequest{
		Target: "prod", Platforms: []string{"linux/amd64", "linux/arm64"}, BuildArgs: map[string]string{"A": "1"}, Labels: map[string]string{"l": "v"},
		NoCacheFilter: []string{"deps"}, Pull: true, ExtraHosts: []string{"db:10.0.0.2", "cache=10.0.0.3"}, ShmSize: 1 << 20,
		Ulimits: []string{"nofile=1024:2048"}, Network: "host",
		Attestations: seamBuildAttestations{Provenance: "max", SBOM: true, Extra: []string{"type=sbom,generator=img:1", "type=provenance,disabled=true"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"target": "prod", "platform": "linux/amd64,linux/arm64", "build-arg:A": "1", "label:l": "v", "no-cache": "deps",
		"image-resolve-mode": "pull", "add-hosts": "db=10.0.0.2,cache=10.0.0.3", "shm-size": "1048576", "ulimit": "nofile=1024:2048",
		"force-network-mode": "host", "attest:sbom": "generator=img:1",
	}
	for k, v := range want {
		if a[k] != v {
			t.Errorf("%s = %q, want %q", k, a[k], v)
		}
	}
	if _, ok := a["attest:provenance"]; ok {
		t.Error("disabled provenance still set")
	}
	if a, _ := bkFrontendAttrs(seamBuildRequest{NoCache: true, NoCacheFilter: []string{"x"}}); a["no-cache"] != "" {
		t.Errorf("no-cache = %q", a["no-cache"])
	}
	if _, err := bkFrontendAttrs(seamBuildRequest{Network: "bridge"}); err == nil {
		t.Error("bridge network accepted")
	}
	if _, err := bkFrontendAttrs(seamBuildRequest{Attestations: seamBuildAttestations{Extra: []string{"mode=max"}}}); err == nil {
		t.Error("attestation without type accepted")
	}
}

func TestBKExports(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	docker := &bkConn{Docker: true, Node: bkDefaultNode()}
	ctr := &bkConn{Node: bkNode{Builder: "ci", Name: "ci0", Driver: bxbDriverDockerContainer}}
	kinds := func(es []bkclient.ExportEntry) string {
		var s []string
		for _, e := range es {
			s = append(s, e.Type+"("+bkJoinCSV(e.Attrs)+")")
		}
		return strings.Join(s, " ")
	}
	cases := []struct {
		conn *bkConn
		out  seamBuildOutputs
		want string
	}{
		{docker, seamBuildOutputs{Names: []string{"a:1"}}, "moby(name=a:1)"},
		{docker, seamBuildOutputs{Names: []string{"a:1", "a:2"}, Push: true}, "moby(name=a:1,a:2,push=true)"},
		{ctr, seamBuildOutputs{Names: []string{"a:1"}}, ""},
		{ctr, seamBuildOutputs{Names: []string{"a:1"}, Push: true, Load: true}, "image(name=a:1,push=true) docker(name=a:1)"},
		{ctr, seamBuildOutputs{Raw: []string{"type=local,dest=" + dir, "type=registry,ref=x"}}, "local() image(push=true,ref=x)"},
		{docker, seamBuildOutputs{Raw: []string{"type=image,name=b:1", "type=docker"}}, "moby(name=b:1) moby()"},
		{ctr, seamBuildOutputs{Raw: []string{"type=tar,dest=" + filepath.Join(dir, "o.tar"), "type=cacheonly"}}, "tar() cacheonly()"},
	}
	for i, c := range cases {
		es, _, err := bkExports(ctx, nil, c.conn, c.out)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if got := kinds(es); got != c.want {
			t.Errorf("%d: %s, want %s", i, got, c.want)
		}
	}
	ctr.Node.Labels = map[string]string{bxbLabelDefaultLoad: "true"}
	if es, loads, _ := bkExports(ctx, nil, ctr, seamBuildOutputs{Names: []string{"a"}}); kinds(es) != "docker(name=a)" || len(loads) != 1 {
		t.Errorf("default-load: %s", kinds(es))
	}
	for _, bad := range []string{"type=local", "type=tar,dest=-", "type=nope", "dest=x"} {
		if _, _, err := bkExports(ctx, nil, ctr, seamBuildOutputs{Raw: []string{bad}}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// A bare path is a local export.
	if es, _, err := bkExports(ctx, nil, ctr, seamBuildOutputs{Raw: []string{dir}}); err != nil || es[0].Type != "local" || es[0].OutputDir == "" {
		t.Errorf("bare path: %v %v", es, err)
	}
}

func TestBKCacheAndCSV(t *testing.T) {
	e, err := bkParseCacheSpec("registry.example.com/cache:main")
	if err != nil || e.Type != "registry" || e.Attrs["ref"] != "registry.example.com/cache:main" {
		t.Errorf("bare: %+v %v", e, err)
	}
	e, err = bkParseCacheSpec(`type=local,dest=cache,mode=max`)
	if err != nil || e.Type != "local" || !filepath.IsAbs(e.Attrs["dest"]) || e.Attrs["mode"] != "max" {
		t.Errorf("local: %+v %v", e, err)
	}
	if _, err := bkParseCacheSpec("mode=max"); err == nil {
		t.Error("cache without type accepted")
	}
	kv, err := bkParseCSV(`type=local,"dest=a,b"`)
	if err != nil || kv["dest"] != "a,b" {
		t.Errorf("csv: %v %v", kv, err)
	}
	if _, err := bkParseCSV("novalue"); err == nil {
		t.Error("csv without = accepted")
	}
}

func TestBKAttachables(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.txt")
	os.WriteFile(src, []byte("file-secret"), 0o600)
	t.Setenv("BK_TEST_SECRET", "env-secret")
	att, err := bkAttachables(seamBuildRequest{Secrets: []seamBuildSecret{
		{ID: "f", Src: src}, {ID: "e", Env: "BK_TEST_SECRET"}, {ID: "v", Value: "literal"},
	}}, nil)
	if err != nil || len(att) != 2 {
		t.Fatalf("%d %v", len(att), err)
	}
	store := bkSecretStore{"x": []byte("y")}
	if v, err := store.GetSecret(context.Background(), "x"); err != nil || string(v) != "y" {
		t.Error(v, err)
	}
	if _, err := store.GetSecret(context.Background(), "nope"); err == nil {
		t.Error("missing secret found")
	}
	for _, bad := range [][]seamBuildSecret{
		{{ID: ""}}, {{ID: "a", Src: src}, {ID: "a", Src: src}}, {{ID: "a", Src: src, Env: "X"}},
		{{ID: "a", Env: "BK_TEST_UNSET_VARIABLE"}}, {{ID: "a", Src: filepath.Join(dir, "missing")}},
	} {
		if _, err := bkAttachables(seamBuildRequest{Secrets: bad}, nil); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestBKMetadataAndProgress(t *testing.T) {
	desc := base64.StdEncoding.EncodeToString([]byte(`{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:idx","size":1}`))
	md := bkMetadata(map[string]string{"containerimage.digest": "sha256:idx", "containerimage.descriptor": desc, "image.name": "a:1"},
		bkNode{Builder: "ci", Name: "ci0"}, "ref1", []map[string]any{{"short": "w"}})
	if md["buildx.build.ref"] != "ci/ci0/ref1" || md["containerimage.digest"] != "sha256:idx" || md["image.name"] != "a:1" {
		t.Errorf("metadata = %v", md)
	}
	if d, ok := md["containerimage.descriptor"].(map[string]any); !ok || d["digest"] != "sha256:idx" {
		t.Errorf("descriptor = %v", md["containerimage.descriptor"])
	}
	in, id := buildxBuildContract(seamBuildRequest{DockerfileInline: "# syntax=docker/dockerfile:1.7\nFROM scratch\n"},
		&seamBuildResult{ImageID: "sha256:cfg", Digest: "sha256:idx", Metadata: md, Platforms: []string{"linux/amd64"}})
	if in.DockerfileSyntax != "docker/dockerfile:1.7" || id.BuildRef != "ci/ci0/ref1" || id.ID != "sha256:cfg" || len(in.Raw["metadata_json"]) == 0 {
		t.Errorf("contract: %+v %+v", in, id)
	}
	var m struct{ contractBuildxBuild }
	if d := m.contractFill(context.Background(), nil, in); d.HasError() || d.WarningsCount() > 0 {
		t.Errorf("contract fill: %v", d)
	}
	if m.DockerfileSyntax.ValueString() != "docker/dockerfile:1.7" || m.MetadataJSONSHA256.IsNull() || m.Dockerfile.IsNull() {
		t.Errorf("filled: %+v", m)
	}

	var lines []string
	p := newBKProgress(func(s string) { lines = append(lines, s) })
	t0 := time.Unix(100, 0)
	t1 := t0.Add(1500 * time.Millisecond)
	p.add(&bkclient.SolveStatus{Vertexes: []*bkclient.Vertex{{Digest: "sha256:a", Name: "[1/2] FROM x", Started: &t0}}})
	p.add(&bkclient.SolveStatus{Logs: []*bkclient.VertexLog{{Vertex: "sha256:a", Data: []byte("hello\nwor")}}})
	p.add(&bkclient.SolveStatus{Logs: []*bkclient.VertexLog{{Vertex: "sha256:a", Data: []byte("ld\npartial")}},
		Vertexes: []*bkclient.Vertex{{Digest: "sha256:a", Started: &t0, Completed: &t1}, {Digest: "sha256:b", Name: "RUN false", Started: &t0, Error: "exit 1"}},
		Warnings: []*bkclient.VertexWarning{{Short: []byte("deprecated")}}})
	p.flush()
	want := "#1 [1/2] FROM x|#1 hello|#1 DONE 1.5s|#2 RUN false|#2 ERROR: exit 1|#1 world|WARNING: deprecated|#1 partial"
	if strings.Join(lines, "|") != want {
		t.Errorf("lines = %q", strings.Join(lines, "|"))
	}
	err := p.wrap(errors.New("solve failed"))
	if !strings.Contains(err.Error(), "[RUN false] exit 1") || !strings.Contains(err.Error(), "#1 partial") || len(p.warnings) != 1 {
		t.Errorf("wrap = %v", err)
	}
}

func TestBKSmallHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"reg.io/a/b:1": "reg.io/a/b@sha256:x", "localhost:5000/a": "localhost:5000/a@sha256:x", "a@sha256:old": "a@sha256:x",
	} {
		if got := bkDigestRef(in, "sha256:x"); got != want {
			t.Errorf("%s -> %s", in, got)
		}
	}
	for in, want := range map[string]string{
		"# syntax=docker/dockerfile:1\nFROM x": "docker/dockerfile:1", "\n#escape=`\n# Syntax = a/b\nFROM x": "a/b",
		"FROM x\n# syntax=late": "", "# a comment\n# syntax=x": "",
	} {
		if got := bkDockerfileSyntax(in); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
	if !bkRemoteContext("https://github.com/a/b.git#main") || bkRemoteContext("./app") {
		t.Error("remote context detection")
	}
	if p := bkDockerfilePath("/ctx", "sub/Dockerfile"); filepath.ToSlash(p) != "/ctx/sub/Dockerfile" {
		t.Error(p)
	}
	if err := bkDrainJSONStream(strings.NewReader(`{"stream":"ok"}{"errorDetail":{"message":"bad"}}`)); err == nil || err.Error() != "bad" {
		t.Errorf("drain = %v", err)
	}
	if err := bkDrainJSONStream(strings.NewReader(`{"stream":"Loaded image"}`)); err != nil {
		t.Error(err)
	}
	if _, ok := seamBuildKitImpl.(laneEBuildKit); !ok {
		t.Errorf("seamBuildKitImpl = %T", seamBuildKitImpl)
	}
}

func TestBKExtractTar(t *testing.T) {
	mk := func(names ...string) *bytes.Buffer {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, n := range names {
			if strings.HasSuffix(n, "/") {
				tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeDir, Mode: 0o755})
				continue
			}
			tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: 2})
			tw.Write([]byte("hi"))
		}
		tw.Close()
		return &buf
	}
	dir := t.TempDir()
	if err := bkExtractTar(mk("sub/", "sub/a.txt", "Dockerfile"), dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sub", "a.txt")); err != nil || string(b) != "hi" {
		t.Errorf("extract: %q %v", b, err)
	}
	if err := bkExtractTar(mk("../evil"), t.TempDir()); err == nil {
		t.Error("escaping entry accepted")
	}
}

func TestBKApplyExtra(t *testing.T) {
	dir := t.TempDir()
	p := &bkPrepared{opt: bkclient.SolveOpt{FrontendAttrs: map[string]string{}, LocalMounts: map[string]fsutil.FS{},
		Exports: []bkclient.ExportEntry{{Type: "image", Attrs: map[string]string{}}, {Type: "local"}}}}
	err := bkApplyExtra(p, &bkExtra{Contexts: map[string]string{"base": "docker-image://alpine:3.20", "src": dir, "git": "https://github.com/a/b.git"},
		Annotations: []string{"org.opencontainers.image.title=x", "manifest:foo=bar"}, Entitlements: []string{"network.host", "network.host"}})
	if err != nil {
		t.Fatal(err)
	}
	a := p.opt.FrontendAttrs
	if a["context:base"] != "docker-image://alpine:3.20" || a["context:src"] != "local:src" || a["context:git"] != "https://github.com/a/b.git" || p.opt.LocalMounts["src"] == nil {
		t.Errorf("contexts: %v", a)
	}
	ex := p.opt.Exports[0].Attrs
	if ex["annotation.org.opencontainers.image.title"] != "x" || ex["annotation-manifest.foo"] != "bar" || p.opt.Exports[1].Attrs != nil {
		t.Errorf("annotations: %v", ex)
	}
	if strings.Join(p.opt.AllowedEntitlements, ",") != "network.host" {
		t.Errorf("entitlements: %v", p.opt.AllowedEntitlements)
	}
	if err := bkApplyExtra(p, &bkExtra{Call: "check"}); err != nil || a["requestid"] != "frontend.check" || p.opt.Exports != nil {
		t.Errorf("call: %v %v", a, err)
	}
	for _, bad := range []map[string]string{{"x": "target:other"}, {"x": "oci-layout://store"}} {
		if err := bkApplyExtra(p, &bkExtra{Contexts: bad}); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if err := bkApplyExtra(p, &bkExtra{Annotations: []string{"novalue"}}); err == nil {
		t.Error("bad annotation accepted")
	}
}

func TestBxViewHelpers(t *testing.T) {
	groups := bxGroupBuilders([]bkNode{bkDefaultNode(), {Builder: "b", Name: "b1"}, {Builder: "a", Name: "a0"}, {Builder: "b", Name: "b0"}})
	var got []string
	for _, g := range groups {
		var ns []string
		for _, n := range g {
			ns = append(ns, n.Name)
		}
		got = append(got, strings.Join(ns, "+"))
	}
	if strings.Join(got, ",") != "default,b0+b1,a0" {
		t.Errorf("groups = %v", got)
	}
	n := bkNodeFromContainer("/buildx_buildkit_foo10", "id", nil, "created", nil, time.Time{})
	if n.Builder != "foo1" || n.Status != bkStatusInactive {
		t.Errorf("node = %+v", n)
	}
	v := bxBuilderView{Nodes: []bxNodeView{{Platforms: []string{"a", "b"}}, {Platforms: []string{"b", "c"}}}}
	if p := v.platforms(); strings.Join(p, ",") != "a,b,c" {
		t.Errorf("platforms = %v", p)
	}
	s := v.structured()
	if nodes := s["nodes"].([]any); len(nodes) != 2 {
		t.Errorf("structured = %v", s)
	}
	opts := bkNodeDriverOpts(container.InspectResponse{Config: &container.Config{Image: "img", Env: []string{"PATH=/x", "A=b"}},
		HostConfig: &container.HostConfig{Resources: container.Resources{CgroupParent: "/cg", CPUQuota: 1, CPUPeriod: 2, CPUShares: 3, CpusetCpus: "0", CpusetMems: "1", MemorySwap: -1}}})
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "cgroup-parent,cpu-period,cpu-quota,cpu-shares,cpuset-cpus,cpuset-mems,env.A,image,memory-swap" {
		t.Errorf("driver opts = %v", keys)
	}
	_ = types.StringNull()
}
