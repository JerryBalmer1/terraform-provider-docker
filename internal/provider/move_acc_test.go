package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// TestAccMoveFromKreuzwerker creates a volume and a network with
// kreuzwerker/docker v4.6.0 (downloaded from the registry by the test
// framework), then moves both into this provider with cross-provider moved
// blocks: the plan must move them without creating or replacing anything.
// Needs Terraform >= 1.8 and registry access.
func TestAccMoveFromKreuzwerker(t *testing.T) {
	testAccPreCheck(t)
	vol := acctest.RandomWithPrefix("tfacc-mvvol")
	net := acctest.RandomWithPrefix("tfacc-mvnet")
	kw := map[string]resource.ExternalProvider{"kreuzwerker": {Source: "kreuzwerker/docker", VersionConstraint: "4.6.0"}}
	before := fmt.Sprintf(`
terraform {
  required_providers {
    kreuzwerker = {
      source  = "kreuzwerker/docker"
      version = "4.6.0"
    }
  }
}

resource "docker_volume" "v" {
  provider = kreuzwerker
  name     = %[1]q
  labels {
    label = "team"
    value = "acc"
  }
}

resource "docker_network" "n" {
  provider = kreuzwerker
  name     = %[2]q
}
`, vol, net)
	after := fmt.Sprintf(`
terraform {
  required_providers {
    kreuzwerker = {
      source  = "kreuzwerker/docker"
      version = "4.6.0"
    }
  }
}

moved {
  from = docker_volume.v
  to   = docker_volume.v_moved
}

moved {
  from = docker_network.n
  to   = docker_network.n_moved
}

resource "docker_volume" "v_moved" {
  name   = %[1]q
  labels = { team = "acc" }
}

resource "docker_network" "n_moved" {
  name = %[2]q
}
`, vol, net)
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_8_0)},
		Steps: []resource.TestStep{
			{
				ExternalProviders: kw,
				Config:            before,
			},
			{
				ExternalProviders:        kw,
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Config:                   after,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					kwExpectMovedInPlace{"docker_volume.v_moved"},
					kwExpectMovedInPlace{"docker_network.n_moved"},
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("docker_volume.v_moved", tfjsonpath.New("name"), knownvalue.StringExact(vol)),
					statecheck.ExpectKnownValue("docker_volume.v_moved", tfjsonpath.New("labels"), knownvalue.MapExact(map[string]knownvalue.Check{"team": knownvalue.StringExact("acc")})),
					statecheck.ExpectKnownValue("docker_network.n_moved", tfjsonpath.New("name"), knownvalue.StringExact(net)),
				},
			},
		},
	})
}

// kwExpectMovedInPlace fails when the plan creates, deletes or replaces the
// moved resource (an in-place update or no-op is a successful move).
type kwExpectMovedInPlace struct{ addr string }

func (c kwExpectMovedInPlace) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		if rc.Address != c.addr {
			continue
		}
		if rc.Change.Actions.Create() || rc.Change.Actions.Delete() || rc.Change.Actions.Replace() {
			resp.Error = fmt.Errorf("%s: planned %v, want a move without create/delete/replace", c.addr, rc.Change.Actions)
		}
		if rc.PreviousAddress == "" {
			resp.Error = fmt.Errorf("%s: no previous address: the moved block was not applied", c.addr)
		}
		return
	}
	resp.Error = fmt.Errorf("%s: not in the plan", c.addr)
}
