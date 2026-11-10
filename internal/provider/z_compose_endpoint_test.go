package provider

import (
	"context"
	"slices"
	"testing"
)

// TestComposeLocalEndpointFieldsZ: per-network gw_priority, link_local_ips,
// driver_opts, interface_name and use_service_name_alias reach the endpoint
// local mode creates, as compose v5.1.4 sends them
// (pkg/compose/create.go:382-450 createEndpointSettings). Pass 5.3 Lane Z:
// fieldcov found these once it looked inside map values.
func TestComposeLocalEndpointFieldsZ(t *testing.T) {
	yaml := `
name: epz
services:
  web:
    image: busybox:1.36
    networks:
      front:
        gw_priority: 10
        link_local_ips: ["169.254.10.10"]
        driver_opts: {com.example.opt: "1"}
        interface_name: eth7
        aliases: [www]
      back:
        use_service_name_alias: false
networks:
  front: {}
  back: {}
`
	p, err := composeLoad(context.Background(), composeInputs{Content: yaml, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := composeLocalExpand(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Containers) != 1 {
		t.Fatalf("containers = %d", len(plan.Containers))
	}
	eps := map[string]composeEndpoint{}
	for _, ep := range plan.Containers[0].Networks {
		eps[ep.Key] = ep
	}
	front, back := eps["front"], eps["back"]
	es, err := composeEndpointSettings(front, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es.GwPriority != 10 {
		t.Errorf("GwPriority = %d", es.GwPriority)
	}
	if es.IPAMConfig == nil || len(es.IPAMConfig.LinkLocalIPs) != 1 || es.IPAMConfig.LinkLocalIPs[0].String() != "169.254.10.10" {
		t.Errorf("IPAMConfig = %+v", es.IPAMConfig)
	}
	if es.DriverOpts["com.example.opt"] != "1" || es.DriverOpts[composeEndpointIfname] != "eth7" {
		t.Errorf("DriverOpts = %v", es.DriverOpts)
	}
	if !slices.Contains(front.Aliases, "web") || !slices.Contains(front.Aliases, "www") {
		t.Errorf("front aliases = %v", front.Aliases)
	}
	if slices.Contains(back.Aliases, "web") {
		t.Errorf("use_service_name_alias: false must drop the service alias: %v", back.Aliases)
	}

	// An endpoint without the new keys keeps its old JSON (spec hash stable).
	plain := composeEndpoint{Key: "k", Name: "n", Aliases: []string{"web"}}
	es2, err := composeEndpointSettings(plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if es2.GwPriority != 0 || es2.DriverOpts != nil || es2.IPAMConfig != nil {
		t.Errorf("plain endpoint = %+v", es2)
	}
	if _, err := composeEndpointSettings(composeEndpoint{LinkLocalIPs: []string{"nope"}}, nil); err == nil {
		t.Error("an invalid link-local IP must fail")
	}
}
