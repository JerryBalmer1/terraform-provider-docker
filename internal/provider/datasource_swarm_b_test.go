package provider

// Pass 5 Lane B: unit and acceptance tests of the singular swarm data
// sources (docker_node, docker_secret, docker_config, docker_service,
// docker_task, docker_swarm, docker_swarm_tokens), the logs data sources
// (docker_service_logs, docker_task_logs) and docker_nodes / docker_tasks.

import (
	"context"
	"encoding/binary"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/swarm"
)

// swarmBReadDS configures d with pd, validates its schema and config
// validators, and reads it with cfg.
func swarmBReadDS(t *testing.T, d datasource.DataSource, pd *providerData, cfg map[string]tftypes.Value) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	if dc, ok := d.(datasource.DataSourceWithConfigure); ok {
		dc.Configure(ctx, datasource.ConfigureRequest{ProviderData: pd}, &datasource.ConfigureResponse{})
	}
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	if diags := sr.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema: %v", diags)
	}
	typ := sr.Schema.Type().TerraformType(ctx)
	conf := tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(typ, cfg)}
	var diags diag.Diagnostics
	if dv, ok := d.(datasource.DataSourceWithConfigValidators); ok {
		for _, v := range dv.ConfigValidators(ctx) {
			var vr datasource.ValidateConfigResponse
			v.ValidateDataSource(ctx, datasource.ValidateConfigRequest{Config: conf}, &vr)
			diags.Append(vr.Diagnostics...)
		}
		if diags.HasError() {
			return tfsdk.State{}, diags
		}
	}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	d.Read(ctx, datasource.ReadRequest{Config: conf}, &resp)
	diags.Append(resp.Diagnostics...)
	return resp.State, diags
}

func swarmBStr(t *testing.T, st tfsdk.State, p path.Path) string {
	t.Helper()
	var v types.String
	if d := st.GetAttribute(context.Background(), p, &v); d.HasError() {
		t.Fatalf("%s: %v", p, d)
	}
	return v.ValueString()
}

func swarmBFakeSwarmObjects(t *testing.T) (*p3eFake, *providerData) {
	f, pd := newP3eFake(t)
	three := uint64(3)
	svc := swarm.Service{ID: "svc1abc", Meta: swarm.Meta{Version: swarm.Version{Index: 12}}, Spec: swarm.ServiceSpec{
		Annotations:  swarm.Annotations{Name: "web", Labels: map[string]string{"app": "shop"}},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx:1.27@sha256:" + repeatA(64), Env: []string{"PW=secret"}}},
		Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &three}},
	}, Endpoint: swarm.Endpoint{Ports: []swarm.PortConfig{{Protocol: "tcp", TargetPort: 80, PublishedPort: 30001, PublishMode: "ingress"}}}}
	node := swarm.Node{ID: "node1abc", Meta: swarm.Meta{Version: swarm.Version{Index: 4}},
		Spec:          swarm.NodeSpec{Role: swarm.NodeRoleManager, Availability: swarm.NodeAvailabilityActive, Annotations: swarm.Annotations{Labels: map[string]string{"zone": "a"}}},
		Description:   swarm.NodeDescription{Hostname: "mgr1", Engine: swarm.EngineDescription{EngineVersion: "29.0.0"}},
		Status:        swarm.NodeStatus{State: swarm.NodeStateReady, Addr: "10.0.0.5"},
		ManagerStatus: &swarm.ManagerStatus{Leader: true}}
	worker := swarm.Node{ID: "node2abc", Spec: swarm.NodeSpec{Role: swarm.NodeRoleWorker}, Description: swarm.NodeDescription{Hostname: "wrk1"}}
	task := swarm.Task{ID: "task1abc", ServiceID: "svc1abc", NodeID: "node2abc", Slot: 2, Meta: swarm.Meta{Version: swarm.Version{Index: 30}},
		Spec:   swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx", Env: []string{"PW=secret"}}},
		Status: swarm.TaskStatus{State: swarm.TaskStateRunning, ContainerStatus: &swarm.ContainerStatus{ContainerID: "ctr1"}}}
	f.json("GET /services", []swarm.Service{svc})
	f.json("GET /services/svc1abc", svc)
	f.json("GET /nodes", []swarm.Node{worker, node})
	f.json("GET /nodes/node1abc", node)
	f.json("GET /tasks", []swarm.Task{task, {ID: "task0", ServiceID: "svc1abc", Slot: 1}})
	f.json("GET /tasks/task1abc", task)
	f.json("GET /secrets", []swarm.Secret{{ID: "sec1abc", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "pw"}}}})
	f.json("GET /secrets/sec1abc", swarm.Secret{ID: "sec1abc", Meta: swarm.Meta{Version: swarm.Version{Index: 7}}, Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "pw", Labels: map[string]string{"k": "v"}}}})
	f.json("GET /configs", []swarm.Config{{ID: "cfg1abc", Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: "conf"}}}})
	f.json("GET /configs/cfg1abc", swarm.Config{ID: "cfg1abc", Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: "conf"}, Data: []byte("k=v")}})
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "cluster1", Meta: swarm.Meta{Version: swarm.Version{Index: 9}}}, JoinTokens: swarm.JoinTokens{Worker: "SWMTKN-w", Manager: "SWMTKN-m"}}
	sw.Spec.Labels = map[string]string{"env": "prod"}
	f.json("GET /swarm", sw)
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	return f, pd
}

func TestSwarmSingularDataSources(t *testing.T) {
	_, pd := swarmBFakeSwarmObjects(t)
	ctx := context.Background()

	st, d := swarmBReadDS(t, NewNodeDataSource(), pd, map[string]tftypes.Value{"hostname": p3eStr("mgr1")})
	if d.HasError() {
		t.Fatal(d)
	}
	if swarmBStr(t, st, path.Root("id")) != "node1abc" || swarmBStr(t, st, path.Root("role")) != "manager" ||
		swarmBStr(t, st, path.Root("name")) != "mgr1" || swarmBStr(t, st, path.Root("inspect").AtName("description").AtName("hostname")) != "mgr1" ||
		swarmBStr(t, st, path.Root("docker_server_version")) != "29.0.0" {
		t.Errorf("node state = %v", st.Raw)
	}
	var leader types.Bool
	var vi types.Int64
	st.GetAttribute(ctx, path.Root("leader"), &leader)
	st.GetAttribute(ctx, path.Root("version_index"), &vi)
	if !leader.ValueBool() || vi.ValueInt64() != 4 {
		t.Errorf("leader=%v version_index=%v", leader, vi)
	}
	if _, d := swarmBReadDS(t, NewNodeDataSource(), pd, map[string]tftypes.Value{"hostname": p3eStr("mgr1"), "id": p3eStr("x")}); !d.HasError() {
		t.Error("id and hostname together should fail")
	}
	if _, d := swarmBReadDS(t, NewNodeDataSource(), pd, map[string]tftypes.Value{"hostname": p3eStr("nope")}); !d.HasError() {
		t.Error("unknown node should fail")
	}

	st, d = swarmBReadDS(t, NewSecretDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("pw")})
	if d.HasError() {
		t.Fatal(d)
	}
	if swarmBStr(t, st, path.Root("id")) != "sec1abc" || swarmBStr(t, st, path.Root("labels").AtMapKey("k")) != "v" || len(swarmBStr(t, st, path.Root("content_sha256"))) != 64 {
		t.Errorf("secret state = %v", st.Raw)
	}

	st, d = swarmBReadDS(t, NewConfigDataSource(), pd, map[string]tftypes.Value{"id": p3eStr("cfg1")})
	if d.HasError() {
		t.Fatal(d)
	}
	if swarmBStr(t, st, path.Root("name")) != "conf" || swarmBStr(t, st, path.Root("inspect").AtName("spec").AtName("data")) != "az12" {
		t.Errorf("config state = %v", st.Raw)
	}

	st, d = swarmBReadDS(t, NewServiceDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("web")})
	if d.HasError() {
		t.Fatal(d)
	}
	var replicas types.Int64
	st.GetAttribute(ctx, path.Root("replicas"), &replicas)
	var pub types.Number
	st.GetAttribute(ctx, path.Root("published_ports").AtListIndex(0).AtName("published_port"), &pub)
	if swarmBStr(t, st, path.Root("image_digest_ref")) != "nginx@sha256:"+repeatA(64) || replicas.ValueInt64() != 3 || pub.String() != "30001" ||
		swarmBStr(t, st, path.Root("inspect").AtName("spec").AtName("name")) != "web" {
		t.Errorf("service state = %v", st.Raw)
	}

	st, d = swarmBReadDS(t, NewTaskDataSource(), pd, map[string]tftypes.Value{"id": p3eStr("task1abc")})
	if d.HasError() {
		t.Fatal(d)
	}
	var slot types.Int64
	st.GetAttribute(ctx, path.Root("slot"), &slot)
	if swarmBStr(t, st, path.Root("name")) != "web.2" || swarmBStr(t, st, path.Root("container_id")) != "ctr1" || slot.ValueInt64() != 2 ||
		swarmBStr(t, st, path.Root("node_id")) != "node2abc" {
		t.Errorf("task state = %v", st.Raw)
	}

	st, d = swarmBReadDS(t, NewSwarmDataSource(), pd, nil)
	if d.HasError() {
		t.Fatal(d)
	}
	if swarmBStr(t, st, path.Root("id")) != "cluster1" || swarmBStr(t, st, path.Root("join_tokens").AtName("worker")) != "SWMTKN-w" ||
		swarmBStr(t, st, path.Root("manager_join_token")) != "SWMTKN-m" || swarmBStr(t, st, path.Root("labels").AtMapKey("env")) != "prod" ||
		!strings.Contains(swarmBStr(t, st, path.Root("inspect_json")), `"ID":"cluster1"`) {
		t.Errorf("swarm state = %v", st.Raw)
	}

	st, d = swarmBReadDS(t, NewSwarmTokensDataSource(), pd, nil)
	if d.HasError() {
		t.Fatal(d)
	}
	if swarmBStr(t, st, path.Root("join_tokens").AtName("manager")) != "SWMTKN-m" || swarmBStr(t, st, path.Root("engine_api_version")) == "" {
		t.Errorf("tokens state = %v", st.Raw)
	}
}

func TestSwarmNodesTasksDataSources(t *testing.T) {
	f, pd := swarmBFakeSwarmObjects(t)
	ctx := context.Background()
	st, d := swarmBReadDS(t, NewNodesDataSource(), pd, map[string]tftypes.Value{"role": p3eStr("manager"), "labels": p3eStrMap(map[string]string{"zone": "a"})})
	if d.HasError() {
		t.Fatal(d)
	}
	var names []string
	st.GetAttribute(ctx, path.Root("names"), &names)
	if len(names) != 2 || names[0] != "mgr1" || swarmBStr(t, st, path.Root("nodes").AtListIndex(0).AtName("status").AtName("addr")) != "10.0.0.5" {
		t.Errorf("nodes = %v %v", names, st.Raw)
	}
	if args := p3eFilterArgs(t, f, "GET /nodes"); !filterExactMatch(args, "role", "manager") || !filterExactMatch(args, "node.label", "zone=a") {
		t.Errorf("node filters = %v", args)
	}

	st, d = swarmBReadDS(t, NewTasksDataSource(), pd, map[string]tftypes.Value{"service_id": p3eStr("web"), "desired_state": p3eStr("running")})
	if d.HasError() {
		t.Fatal(d)
	}
	var ids []string
	st.GetAttribute(ctx, path.Root("ids"), &ids)
	st.GetAttribute(ctx, path.Root("names"), &names)
	if len(ids) != 2 || ids[0] != "task0" || names[1] != "web.2" ||
		swarmBStr(t, st, path.Root("tasks").AtListIndex(1).AtName("status").AtName("container_status").AtName("container_id")) != "ctr1" {
		t.Errorf("tasks = %v %v", ids, names)
	}
	if args := p3eFilterArgs(t, f, "GET /tasks"); !filterExactMatch(args, "service", "web") || !filterExactMatch(args, "desired-state", "running") {
		t.Errorf("task filters = %v", args)
	}
}

// swarmBFrame is one multiplexed log frame.
func swarmBFrame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestSwarmLogsDataSources(t *testing.T) {
	f, pd := swarmBFakeSwarmObjects(t)
	ctx := context.Background()
	var query string
	f.on("GET /services/svc1abc/logs", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = w.Write(swarmBFrame(1, "2026-01-02T03:04:06.000000000Z second\n"))
		_, _ = w.Write(swarmBFrame(2, "2026-01-02T03:04:05.000000000Z first err\n"))
		_, _ = w.Write(swarmBFrame(1, "2026-01-02T03:04:07.000000000Z third\n"))
	})
	st, d := swarmBReadDS(t, NewServiceLogsDataSource(), pd, map[string]tftypes.Value{"service_id": p3eStr("web"), "tail": tftypes.NewValue(tftypes.Number, 2)})
	if d.HasError() {
		t.Fatal(d)
	}
	if !strings.Contains(query, "tail=2") || !strings.Contains(query, "timestamps=1") {
		t.Errorf("query = %s", query)
	}
	logs := swarmBStr(t, st, path.Root("logs"))
	if logs != "2026-01-02T03:04:06.000000000Z stdout web second\n2026-01-02T03:04:07.000000000Z stdout web third" {
		t.Errorf("logs = %q", logs)
	}
	if swarmBStr(t, st, path.Root("log_entries").AtListIndex(0).AtName("source")) != "web" || swarmBStr(t, st, path.Root("id")) != "svc1abc" {
		t.Errorf("entries = %v", st.Raw)
	}
	var tail types.Int64
	st.GetAttribute(ctx, path.Root("tail"), &tail)
	if tail.ValueInt64() != 2 {
		t.Errorf("tail input not echoed: %v", tail)
	}

	f.on("GET /tasks/task1abc/logs", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write(swarmBFrame(2, "2026-01-02T03:04:05Z oops\n"))
	})
	st, d = swarmBReadDS(t, NewTaskLogsDataSource(), pd, map[string]tftypes.Value{"task_id": p3eStr("task1abc"), "since": p3eStr("2026-01-01T00:00:00Z")})
	if d.HasError() {
		t.Fatal(d)
	}
	if swarmBStr(t, st, path.Root("log_entries").AtListIndex(0).AtName("stream")) != "stderr" ||
		swarmBStr(t, st, path.Root("log_entries").AtListIndex(0).AtName("source")) != "task1abc" || !strings.Contains(query, "since=1767225600") ||
		!strings.Contains(query, "tail=1000") {
		t.Errorf("task logs = %v query=%s", st.Raw, query)
	}
	if _, d := swarmBReadDS(t, NewTaskLogsDataSource(), pd, map[string]tftypes.Value{"task_id": p3eStr("missing")}); !d.HasError() {
		t.Error("missing task should fail")
	}
}

// TestAccSwarmDataSources reads every singular swarm data source and the
// logs/tasks ones against a swarm manager (needs the -SwarmInit harness pass).
func TestAccSwarmDataSources(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_secret" "s" {
  name = "tfacc-ds-secret"
  data = base64encode("x")
}

resource "docker_config" "c" {
  name = "tfacc-ds-config"
  data = base64encode("k=v")
}

resource "docker_service" "web" {
  name     = "tfacc-ds-web"
  image    = "busybox:1.36"
  args     = ["sh", "-c", "echo hello-from-tfacc; sleep 3600"]
  replicas = 1
  converge_config {
    timeout = "3m"
  }
}

data "docker_swarm" "this" {}

data "docker_nodes" "managers" {
  role = "manager"
}

data "docker_node" "first" {
  hostname = data.docker_nodes.managers.nodes[0].description.hostname
}

data "docker_secret" "s" {
  name = docker_secret.s.name
}

data "docker_config" "c" {
  id = docker_config.c.id
}

data "docker_service" "web" {
  name = docker_service.web.name
}

data "docker_tasks" "web" {
  service_id    = docker_service.web.id
  desired_state = "running"
}

data "docker_task" "first" {
  id = data.docker_tasks.web.ids[0]
}

data "docker_service_logs" "web" {
  service_id = docker_service.web.id
  tail       = 10
}

data "docker_task_logs" "first" {
  task_id = data.docker_task.first.id
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.docker_swarm.this", "id"),
				resource.TestCheckResourceAttrSet("data.docker_swarm.this", "inspect.spec.orchestration.task_history_retention_limit"),
				resource.TestCheckResourceAttr("data.docker_node.first", "role", "manager"),
				resource.TestCheckResourceAttrPair("data.docker_node.first", "id", "data.docker_nodes.managers", "ids.0"),
				resource.TestCheckResourceAttrPair("data.docker_secret.s", "id", "docker_secret.s", "id"),
				resource.TestCheckResourceAttr("data.docker_config.c", "inspect.spec.data", "az12"),
				resource.TestCheckResourceAttrPair("data.docker_service.web", "id", "docker_service.web", "id"),
				resource.TestCheckResourceAttrSet("data.docker_service.web", "image_digest_ref"),
				resource.TestCheckResourceAttr("data.docker_tasks.web", "tasks.#", "1"),
				resource.TestCheckResourceAttrPair("data.docker_task.first", "service_id", "docker_service.web", "id"),
				resource.TestCheckResourceAttr("data.docker_service_logs.web", "log_entries.0.source", "tfacc-ds-web"),
				resource.TestCheckResourceAttrSet("data.docker_task_logs.first", "content_sha256"),
			),
		}},
	})
}
