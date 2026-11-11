package provider

// Pass 6c Lane F (open finding 2): the configuration Terraform generates for
// an imported global docker_service is valid (replicas is null in state for
// every mode but replicated; service_replicas_p6c_f.go).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

func TestServiceReplicasNullUnlessReplicatedP6cF(t *testing.T) {
	one := uint64(3)
	for name, tc := range map[string]struct {
		mode swarm.ServiceMode
		want types.Int64
	}{
		"replicated":     {swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &one}}, types.Int64Value(3)},
		"global":         {swarm.ServiceMode{Global: &swarm.GlobalService{}}, types.Int64Null()},
		"global_job":     {swarm.ServiceMode{GlobalJob: &swarm.GlobalJob{}}, types.Int64Null()},
		"replicated_job": {swarm.ServiceMode{ReplicatedJob: &swarm.ReplicatedJob{}}, types.Int64Null()},
	} {
		spec := swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "s"}, Mode: tc.mode,
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "busybox"}}}
		got := svcFlatten(spec, svcSpecModel{Replicas: types.Int64Value(1)}, nil, svcFlattenOpts{NullReplicasUnlessReplicated: true})
		if !got.Replicas.Equal(tc.want) {
			t.Errorf("%s: replicas = %v, want %v", name, got.Replicas, tc.want)
		}
		// docker_stack keeps the prior value (its schema default).
		if st := svcFlatten(spec, svcSpecModel{Replicas: types.Int64Value(1)}, nil, svcFlattenOpts{}); st.Replicas.IsNull() {
			t.Errorf("%s: stack flatten nulled replicas", name)
		}
	}
	// The docker_service schema has no static default any more (the plan
	// modifier decides by mode); the stack block keeps it.
	var sr resource.SchemaResponse
	NewServiceResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	a := sr.Schema.Attributes["replicas"]
	if ia, ok := a.(rschema.Int64Attribute); !ok || ia.Default != nil || len(ia.PlanModifiers) == 0 {
		t.Error("docker_service replicas still has a static default")
	}
}

// replicasNullCheck fails unless the planned replicas of addr is null.
type replicasNullCheck struct{ addr string }

func (c replicasNullCheck) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		if rc.Address != c.addr || rc.Change == nil {
			continue
		}
		after, _ := rc.Change.After.(map[string]any)
		if v, ok := after["replicas"]; ok && v != nil {
			resp.Error = fmt.Errorf("%s: planned replicas = %v, want null for a global service", c.addr, v)
		}
		return
	}
	resp.Error = fmt.Errorf("%s not in the plan", c.addr)
}

// TestAccServiceP6cF_globalGenerateConfig reproduces open finding 2 the way
// the finding states it: a global service created outside Terraform (docker
// service create --mode global), an import block, terraform plan
// -generate-config-out. The plan validates the generated configuration, so a
// generated `replicas = 1` fails the step.
func TestAccServiceP6cF_globalGenerateConfig(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	ctx := context.Background()
	name := acctest.RandomWithPrefix("tfacc-p6cf-global")
	out, err := c.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: name},
		Mode:        swarm.ServiceMode{Global: &swarm.GlobalService{}},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
			Image: "busybox:1.36", Command: []string{"sleep", "3600"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svcRemoveAndWait(context.Background(), c, out.ID, 2*time.Minute) })
	hAccTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{{
			Config:             "locals {\n  p6cf = 1\n}\n",
			ResourceName:       "docker_service.g",
			ImportState:        true,
			ImportStateKind:    tfresource.ImportBlockWithID,
			ImportStateId:      name,
			GenerateConfig:     true,
			ExpectNonEmptyPlan: true,
			ImportPlanChecks: tfresource.ImportPlanChecks{PreApply: []plancheck.PlanCheck{
				replicasNullCheck{addr: "docker_service.g"},
			}},
		}},
	})
}
