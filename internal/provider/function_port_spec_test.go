package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestParsePortSpec(t *testing.T) {
	cases := []struct {
		in      string
		want    portSpecParts
		wantErr bool
	}{
		{"80", portSpecParts{"", "", "80", "tcp"}, false},
		{"8080:80", portSpecParts{"", "8080", "80", "tcp"}, false},
		{"127.0.0.1:53:53/udp", portSpecParts{"127.0.0.1", "53", "53", "udp"}, false},
		{"127.0.0.1::80", portSpecParts{"127.0.0.1", "", "80", "tcp"}, false},
		{"[::1]:8443:443", portSpecParts{"::1", "8443", "443", "tcp"}, false},
		{"[2001:db8::1]:80:80/SCTP", portSpecParts{"2001:db8::1", "80", "80", "sctp"}, false},
		{"8000-8010:80-90", portSpecParts{"", "8000-8010", "80-90", "tcp"}, false},
		{"8000-8010:80", portSpecParts{"", "8000-8010", "80", "tcp"}, false},
		{"7000-7001/udp", portSpecParts{"", "", "7000-7001", "udp"}, false},
		{"", portSpecParts{}, true},
		{" 80", portSpecParts{}, true},
		{"99999:80", portSpecParts{}, true},
		{"80:abc", portSpecParts{}, true},
		{"8000-8002:80-81", portSpecParts{}, true},
		{"80/icmp", portSpecParts{}, true},
		{"300.1.1.1:80:80", portSpecParts{}, true},
		{"80/tcp/udp", portSpecParts{}, true},
	}
	for _, tc := range cases {
		got, err := parsePortSpec(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("parsePortSpec(%q) = %+v, %v; want %+v (err=%v)", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestPortSpecFunctionRun(t *testing.T) {
	v, ferr := p3eRunFunc(t, NewPortSpecFunction(), types.StringValue("0.0.0.0:8080:80"))
	if ferr != nil {
		t.Fatal(ferr)
	}
	got := p3eObjStrings(v)
	if got["host_ip"] != "0.0.0.0" || got["host_port"] != "8080" || got["container_port"] != "80" || got["protocol"] != "tcp" {
		t.Errorf("got %v", got)
	}
	if _, ferr := p3eRunFunc(t, NewPortSpecFunction(), types.StringValue("nope")); ferr == nil || *ferr.FunctionArgument != 0 {
		t.Errorf("want argument error, got %v", ferr)
	}
}
