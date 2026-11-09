package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

func TestSecretBase64Validation(t *testing.T) {
	if err := secretValidateBase64("aGVsbG8="); err != nil {
		t.Errorf("valid: %v", err)
	}
	if err := secretValidateBase64("not base64!"); err == nil {
		t.Error("invalid accepted")
	}
	if err := secretValidateBase64(""); err != nil {
		t.Errorf("empty: %v", err)
	}
}

func TestSecretFakeDaemonInvalidData(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: svcFakeConfig(host, `
resource "docker_secret" "bad" {
  name = "bad"
  data = "plain text"
}
`),
			ExpectError: regexp.MustCompile(`Invalid base64`),
		}},
	})
}

func TestAccSecretBasic(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "docker_secret" "test" {
  name   = "tfacc-secret"
  data   = base64encode("s3cret")
  labels = { purpose = "test" }
}
`,
				Check: resource.TestCheckResourceAttrSet("docker_secret.test", "id"),
			},
			{ResourceName: "docker_secret.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"data"}},
		},
		CheckDestroy: func(s *terraform.State) error {
			c := testAccDockerClient(t)
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "docker_secret" {
					continue
				}
				if _, err := c.SecretInspect(context.Background(), rs.Primary.ID, client.SecretInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("secret %s still exists (err=%v)", rs.Primary.ID, err)
				}
			}
			return nil
		},
	})
}

// p3eSecretLikeCreate runs Create on r (docker_secret or docker_config) with
// cfg as both config and plan, write-only data_wo nulled in the plan like the
// framework does, and returns the state and identity after Create.
func p3eSecretLikeCreate(t *testing.T, r fwresource.Resource, pd *providerData, cfg map[string]tftypes.Value) (tfsdk.State, *tfsdk.ResourceIdentity, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	r.(fwresource.ResourceWithConfigure).Configure(ctx, fwresource.ConfigureRequest{ProviderData: pd}, &fwresource.ConfigureResponse{})
	var sr fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx)
	planVals := map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.String, tftypes.UnknownValue)}
	for k, v := range cfg {
		if k != "data_wo" {
			planVals[k] = v
		}
	}
	idSchema := listIDIdentitySchema()
	resp := fwresource.CreateResponse{
		State:    tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)},
		Identity: &tfsdk.ResourceIdentity{Schema: idSchema, Raw: tftypes.NewValue(idSchema.Type().TerraformType(ctx), nil)},
	}
	r.Create(ctx, fwresource.CreateRequest{
		Config: tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(typ, cfg)},
		Plan:   tfsdk.Plan{Schema: sr.Schema, Raw: p3eObject(typ, planVals)},
	}, &resp)
	return resp.State, resp.Identity, resp.Diagnostics
}

func p3eValidateSecretLike(t *testing.T, r fwresource.ResourceWithConfigValidators, cfg map[string]tftypes.Value) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()
	var sr fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &sr)
	conf := tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(sr.Schema.Type().TerraformType(ctx), cfg)}
	var diags diag.Diagnostics
	for _, v := range r.ConfigValidators(ctx) {
		var vr fwresource.ValidateConfigResponse
		v.ValidateResource(ctx, fwresource.ValidateConfigRequest{Config: conf}, &vr)
		diags.Append(vr.Diagnostics...)
	}
	return diags
}

func TestSecretWriteOnlySchema(t *testing.T) {
	for _, r := range []fwresource.Resource{NewSecretResource(), NewConfigResource()} {
		var sr fwresource.SchemaResponse
		r.Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
		if d := sr.Schema.ValidateImplementation(context.Background()); d.HasError() {
			t.Fatal(d)
		}
		wo := sr.Schema.Attributes["data_wo"]
		if wo == nil || !wo.IsWriteOnly() || !wo.IsSensitive() || wo.IsComputed() {
			t.Errorf("data_wo must be write-only, sensitive, not computed: %#v", wo)
		}
		ver, ok := sr.Schema.Attributes["data_wo_version"].(schema.Int64Attribute)
		if !ok || len(ver.PlanModifiers) != 1 {
			t.Errorf("data_wo_version must force replacement: %#v", sr.Schema.Attributes["data_wo_version"])
		}
		if sr.Schema.Attributes["data"].IsRequired() {
			t.Error("data must be optional now")
		}
	}
}

func TestSecretDataConfigValidators(t *testing.T) {
	b64 := p3eStr("eA==")
	ver := tftypes.NewValue(tftypes.Number, 1)
	cases := []struct {
		name    string
		cfg     map[string]tftypes.Value
		wantErr bool
	}{
		{"data only", map[string]tftypes.Value{"name": p3eStr("s"), "data": b64}, false},
		{"data_wo with version", map[string]tftypes.Value{"name": p3eStr("s"), "data_wo": b64, "data_wo_version": ver}, false},
		{"both", map[string]tftypes.Value{"name": p3eStr("s"), "data": b64, "data_wo": b64, "data_wo_version": ver}, true},
		{"neither", map[string]tftypes.Value{"name": p3eStr("s")}, true},
		{"data_wo without version", map[string]tftypes.Value{"name": p3eStr("s"), "data_wo": b64}, true},
		{"version without data_wo", map[string]tftypes.Value{"name": p3eStr("s"), "data": b64, "data_wo_version": ver}, true},
	}
	for _, r := range []fwresource.ResourceWithConfigValidators{&secretResource{}, &dockerConfigResource{}} {
		for _, tc := range cases {
			if got := p3eValidateSecretLike(t, r, tc.cfg).HasError(); got != tc.wantErr {
				t.Errorf("%T %s: error = %v, want %v", r, tc.name, got, tc.wantErr)
			}
		}
	}
}

func TestSecretCreateFromDataWO(t *testing.T) {
	f, pd := newP3eFake(t)
	var got swarm.SecretSpec
	f.on("POST /secrets/create", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		p3eWriteJSON(w, http.StatusCreated, map[string]string{"ID": "sec1"})
	})
	f.on("GET /secrets/sec1", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, http.StatusOK, swarm.Secret{ID: "sec1", Spec: swarm.SecretSpec{Annotations: got.Annotations}})
	})
	state, identity, diags := p3eSecretLikeCreate(t, NewSecretResource(), pd, map[string]tftypes.Value{
		"name":            p3eStr("pw"),
		"data_wo":         p3eStr("czNjcmV0"),
		"data_wo_version": tftypes.NewValue(tftypes.Number, 3),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if string(got.Data) != "s3cret" {
		t.Errorf("daemon got data %q", got.Data)
	}
	var m secretResourceModel
	state.Get(context.Background(), &m)
	if !m.DataWO.IsNull() || !m.Data.IsNull() || m.DataWOVersion.ValueInt64() != 3 || m.ID.ValueString() != "sec1" {
		t.Errorf("state = %+v", m)
	}
	var id types.String
	identity.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "sec1" {
		t.Errorf("identity id = %s", id)
	}
}

func TestAccSecretWriteOnly(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	cfg := func(v int) string {
		return fmt.Sprintf(`
resource "docker_secret" "wo" {
  name            = "tfacc-secret-wo-%d"
  data_wo         = base64encode("s3cret-%d")
  data_wo_version = %d
}
`, v, v, v)
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{Config: cfg(1), Check: resource.TestCheckNoResourceAttr("docker_secret.wo", "data_wo")},
			{Config: cfg(2), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("docker_secret.wo", plancheck.ResourceActionReplace),
			}}},
		},
	})
}
