package provider

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

type p3eManifestRow struct {
	Digest       string `tfsdk:"digest"`
	MediaType    string `tfsdk:"media_type"`
	Size         int64  `tfsdk:"size"`
	OS           string `tfsdk:"os"`
	Architecture string `tfsdk:"architecture"`
	Variant      string `tfsdk:"variant"`
	OSVersion    string `tfsdk:"os_version"`
}

func TestP3RegistryImageManifestsIndex(t *testing.T) {
	reg := newP3eFakeRegistry(t, "bearer")
	dA, dB := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	var accept string
	reg.handle("/v2/org/app/manifests/1.0", func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", p3MediaOCIIndex)
		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("f", 64))
		fmt.Fprintf(w, `{"schemaVersion":2,"mediaType":%q,"manifests":[
		  {"mediaType":%q,"digest":%q,"size":500,"platform":{"architecture":"amd64","os":"linux"}},
		  {"mediaType":%q,"digest":%q,"size":501,"platform":{"architecture":"arm64","os":"linux","variant":"v8"}}]}`,
			p3MediaOCIIndex, p3MediaOCIManifest, dA, p3MediaOCIManifest, dB)
	})
	state, diags := p3eReadDS(t, NewP3RegistryImageManifestsDataSource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{
		"name": p3eStr(reg.host() + "/org/app:1.0"),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var rows []p3eManifestRow
	state.GetAttribute(context.Background(), path.Root("manifests"), &rows)
	want := []p3eManifestRow{
		{dA, p3MediaOCIManifest, 500, "linux", "amd64", "", ""},
		{dB, p3MediaOCIManifest, 501, "linux", "arm64", "v8", ""},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("manifests = %+v", rows)
	}
	var digest string
	state.GetAttribute(context.Background(), path.Root("digest"), &digest)
	if digest != "sha256:"+strings.Repeat("f", 64) || !strings.Contains(accept, p3MediaDockerList) {
		t.Errorf("digest = %s, accept = %s", digest, accept)
	}
	if len(reg.tokenQ) != 1 || !strings.Contains(reg.tokenQ[0], "scope=repository%3Aorg%2Fapp%3Apull") {
		t.Errorf("token requests = %v", reg.tokenQ)
	}
}

func TestP3RegistryImageManifestsSingle(t *testing.T) {
	reg := newP3eFakeRegistry(t, "basic")
	cfgDigest := "sha256:" + strings.Repeat("c", 64)
	body := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":%q,"size":10}}`,
		p3MediaDockerManif2, cfgDigest)
	reg.handle("/v2/library/solo/manifests/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", p3MediaDockerManif2)
		_, _ = w.Write([]byte(body))
	})
	reg.handle("/v2/library/solo/blobs/"+cfgDigest, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"architecture":"amd64","os":"windows","os.version":"10.0.20348.1"}`))
	})
	state, diags := p3eReadDS(t, NewP3RegistryImageManifestsDataSource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{
		"name": p3eStr(reg.host() + "/library/solo"),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var rows []p3eManifestRow
	state.GetAttribute(context.Background(), path.Root("manifests"), &rows)
	if len(rows) != 1 || rows[0].OS != "windows" || rows[0].OSVersion != "10.0.20348.1" || !strings.HasPrefix(rows[0].Digest, "sha256:") ||
		rows[0].Size != int64(len(body)) || rows[0].MediaType != p3MediaDockerManif2 {
		t.Errorf("manifests = %+v", rows)
	}
}

func TestP3RegistryImageManifestsNotFound(t *testing.T) {
	reg := newP3eFakeRegistry(t, "none")
	_, diags := p3eReadDS(t, NewP3RegistryImageManifestsDataSource(), &providerData{}, map[string]tftypes.Value{
		"name": p3eStr(reg.host() + "/nope:1"),
	})
	if !diags.HasError() || !strings.Contains(diags[0].Detail(), "404") {
		t.Fatalf("diags = %v", diags)
	}
}

func TestP3RegistryImageTagsPaginated(t *testing.T) {
	reg := newP3eFakeRegistry(t, "bearer")
	var queries []string
	reg.handle("/v2/org/app/tags/list", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		switch r.URL.Query().Get("last") {
		case "":
			w.Header().Set("Link", `</v2/org/app/tags/list?last=b&n=2>; rel="next"`)
			_, _ = w.Write([]byte(`{"name":"org/app","tags":["a","b"]}`))
		case "b":
			w.Header().Set("Link", `</v2/org/app/tags/list?last=d>; rel="next"`) // registry dropped n
			_, _ = w.Write([]byte(`{"name":"org/app","tags":["c","d"]}`))
		default:
			_, _ = w.Write([]byte(`{"name":"org/app","tags":["e"]}`))
		}
	})
	state, diags := p3eReadDS(t, NewP3RegistryImageTagsDataSource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{
		"name":      p3eStr(reg.host() + "/org/app:ignored"),
		"page_size": tftypes.NewValue(tftypes.Number, 2),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var tags []string
	var repo string
	state.GetAttribute(context.Background(), path.Root("tags"), &tags)
	state.GetAttribute(context.Background(), path.Root("repository"), &repo)
	if !reflect.DeepEqual(tags, []string{"a", "b", "c", "d", "e"}) || repo != reg.host()+"/org/app" {
		t.Errorf("tags = %v, repository = %s", tags, repo)
	}
	// Only authorized requests reach the handler: one per page.
	if len(queries) != 3 {
		t.Errorf("page queries = %v", queries)
	}
	for _, q := range queries {
		if !strings.Contains(q, "n=2") {
			t.Errorf("page query %q lost n=2 (all: %v)", q, queries)
		}
	}
}

func TestAccP3RegistryImageDataSources(t *testing.T) {
	testAccPreCheck(t)
	host := testAccRegistryHost(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "docker_registry_image_manifests" "alpine" {
  name = "%[1]s/library/alpine:3.20"
}

data "docker_registry_image_tags" "alpine" {
  name      = "%[1]s/library/alpine"
  page_size = 1000
}
`, host),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckTypeSetElemNestedAttrs("data.docker_registry_image_manifests.alpine", "manifests.*",
					map[string]string{"os": "linux", "architecture": "amd64"}),
				resource.TestCheckTypeSetElemAttr("data.docker_registry_image_tags.alpine", "tags.*", "3.20"),
			),
		}},
	})
}
