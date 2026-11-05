package provider

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// p3eRunFunc calls f.Run with args, as the framework does after it has
// checked the arguments against the definition.
func p3eRunFunc(t *testing.T, f function.Function, args ...attr.Value) (attr.Value, *function.FuncError) {
	t.Helper()
	ctx := context.Background()
	var dr function.DefinitionResponse
	f.Definition(ctx, function.DefinitionRequest{}, &dr)
	rd, ferr := dr.Definition.Return.NewResultData(ctx)
	if ferr != nil {
		t.Fatal(ferr)
	}
	res := function.RunResponse{Result: rd}
	f.Run(ctx, function.RunRequest{Arguments: function.NewArgumentsData(args)}, &res)
	return res.Result.Value(), res.Error
}

func p3eObjStrings(v attr.Value) map[string]string {
	out := map[string]string{}
	if o, ok := v.(types.Object); ok {
		for k, a := range o.Attributes() {
			out[k] = p3eAttrString(a)
		}
	}
	return out
}

func TestParseImageRef(t *testing.T) {
	dig := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		in      string
		want    imageRefParts
		wantErr bool
	}{
		{"nginx", imageRefParts{"docker.io", "library/nginx", "latest", ""}, false},
		{"nginx:1.27", imageRefParts{"docker.io", "library/nginx", "1.27", ""}, false},
		{"docker.io/library/nginx:1.27", imageRefParts{"docker.io", "library/nginx", "1.27", ""}, false},
		{"bitnami/redis", imageRefParts{"docker.io", "bitnami/redis", "latest", ""}, false},
		{"ghcr.io/org/app:main", imageRefParts{"ghcr.io", "org/app", "main", ""}, false},
		{"localhost:5000/app", imageRefParts{"localhost:5000", "app", "latest", ""}, false},
		{"app@" + dig, imageRefParts{"docker.io", "library/app", "", dig}, false},
		{"reg.example.com:443/a/b/c:v1@" + dig, imageRefParts{"reg.example.com:443", "a/b/c", "v1", dig}, false},
		{"Upper/Case", imageRefParts{}, true},
		{"", imageRefParts{}, true},
		{"app@sha256:short", imageRefParts{}, true},
	}
	for _, tc := range cases {
		got, err := parseImageRef(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("parseImageRef(%q) = %+v, %v; want %+v, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestParseImageRefFunctionRun(t *testing.T) {
	v, ferr := p3eRunFunc(t, NewParseImageRefFunction(), types.StringValue("ghcr.io/org/app:1.2"))
	if ferr != nil {
		t.Fatal(ferr)
	}
	got := p3eObjStrings(v)
	if got["registry"] != "ghcr.io" || got["repository"] != "org/app" || got["tag"] != "1.2" || got["digest"] != "" {
		t.Errorf("got %v", got)
	}
	_, ferr = p3eRunFunc(t, NewParseImageRefFunction(), types.StringValue("bad ref"))
	if ferr == nil || ferr.FunctionArgument == nil || *ferr.FunctionArgument != 0 {
		t.Errorf("want argument error, got %v", ferr)
	}
}

func TestBuildImageRef(t *testing.T) {
	dig := "sha256:" + strings.Repeat("b", 64)
	cases := []struct {
		reg, repo, tag, dig string
		want                string
		errArg              int // -1: no error
	}{
		{"", "nginx", "1.27", "", "docker.io/library/nginx:1.27", -1},
		{"docker.io", "library/nginx", "", "", "docker.io/library/nginx", -1},
		{"ghcr.io", "org/app", "main", dig, "ghcr.io/org/app:main@" + dig, -1},
		{"localhost:5000", "app", "", dig, "localhost:5000/app@" + dig, -1},
		{"", "", "x", "", "", 1},
		{"ghcr.io", "org/app:1", "", "", "", 1},
		{"ghcr.io", "org/app", "bad tag!", "", "", 2},
		{"ghcr.io", "org/app", "", "sha256:nope", "", 3},
		{"notahost", "app", "", "", "", 0},
	}
	for _, tc := range cases {
		got, idx, err := buildImageRef(tc.reg, tc.repo, tc.tag, tc.dig)
		if tc.errArg < 0 {
			if err != nil || got != tc.want {
				t.Errorf("buildImageRef(%q,%q,%q,%q) = %q, %v; want %q", tc.reg, tc.repo, tc.tag, tc.dig, got, err, tc.want)
			}
			continue
		}
		if err == nil || idx != tc.errArg {
			t.Errorf("buildImageRef(%q,%q,%q,%q) = %q, arg %d, %v; want error on arg %d", tc.reg, tc.repo, tc.tag, tc.dig, got, idx, err, tc.errArg)
		}
	}
	// Round trip through parse.
	for _, ref := range []string{"nginx:1", "ghcr.io/a/b@" + dig, "localhost:5000/x:y"} {
		p, _ := parseImageRef(ref)
		out, _, err := buildImageRef(p.Registry, p.Repository, p.Tag, p.Digest)
		if err != nil {
			t.Fatal(err)
		}
		if p2, _ := parseImageRef(out); p2 != p {
			t.Errorf("round trip %q -> %q -> %+v != %+v", ref, out, p2, p)
		}
	}
}

func TestImageRefFunctionRunNulls(t *testing.T) {
	v, ferr := p3eRunFunc(t, NewImageRefFunction(), types.StringNull(), types.StringValue("redis"), types.StringValue("7"), types.StringNull())
	if ferr != nil {
		t.Fatal(ferr)
	}
	if s := p3eAttrString(v); s != "docker.io/library/redis:7" {
		t.Errorf("got %q", s)
	}
	_, ferr = p3eRunFunc(t, NewImageRefFunction(), types.StringNull(), types.StringValue("redis"), types.StringValue("bad tag!"), types.StringNull())
	if ferr == nil || *ferr.FunctionArgument != 2 {
		t.Errorf("want error on argument 2, got %v", ferr)
	}
}

func TestFunctionDefinitions(t *testing.T) {
	ctx := context.Background()
	names := map[string]bool{}
	for _, nf := range laneFunctions() {
		f := nf()
		var mr function.MetadataResponse
		f.Metadata(ctx, function.MetadataRequest{}, &mr)
		names[mr.Name] = true
		var dr function.DefinitionResponse
		f.Definition(ctx, function.DefinitionRequest{}, &dr)
		var vr function.DefinitionValidateResponse
		dr.Definition.ValidateImplementation(ctx, function.DefinitionValidateRequest{FuncName: mr.Name}, &vr)
		if vr.Diagnostics.HasError() {
			t.Errorf("%s: %v", mr.Name, vr.Diagnostics)
		}
		if !strings.Contains(dr.Definition.MarkdownDescription, ". ") || dr.Definition.Summary == "" {
			t.Errorf("%s: needs a summary and a standalone first sentence", mr.Name)
		}
	}
	for _, n := range []string{"parse_image_ref", "image_ref", "compose_env", "port_spec"} {
		if !names[n] {
			t.Errorf("function %s not registered", n)
		}
	}
}

// TestFunctionsTerraform calls every function from HCL through Terraform
// (provider functions need Terraform 1.8+). No daemon is involved.
func TestFunctionsTerraform(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_8_0)},
		Steps: []resource.TestStep{
			{
				Config: `
locals {
  ref = provider::docker::parse_image_ref("ghcr.io/org/app:1.2")
  p   = provider::docker::port_spec("[::1]:8080-8081:80-81/udp")
}
output "repo"  { value = local.ref.repository }
output "full"  { value = provider::docker::image_ref(local.ref.registry, local.ref.repository, "2.0", null) }
output "env"   { value = join(",", provider::docker::compose_env({ B = "2", A = "1", C = null })) }
output "ip"    { value = local.p.host_ip }
output "hport" { value = local.p.host_port }
output "proto" { value = local.p.protocol }
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckOutput("repo", "org/app"),
					resource.TestCheckOutput("full", "ghcr.io/org/app:2.0"),
					resource.TestCheckOutput("env", "A=1,B=2,C"),
					resource.TestCheckOutput("ip", "::1"),
					resource.TestCheckOutput("hport", "8080-8081"),
					resource.TestCheckOutput("proto", "udp"),
				),
			},
			{
				Config:      `output "x" { value = provider::docker::port_spec("99999:80") }`,
				ExpectError: regexp.MustCompile(`invalid port spec`),
			},
		},
	})
}
