package provider

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestComposeEnv(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name    string
		in      map[string]*string
		want    []string
		wantErr bool
	}{
		{"sorted", map[string]*string{"B": s("2"), "A": s("1"), "C": s("")}, []string{"A=1", "B=2", "C="}, false},
		{"bare key for null", map[string]*string{"HOME": nil, "X": s("a=b")}, []string{"HOME", "X=a=b"}, false},
		{"empty", map[string]*string{}, []string{}, false},
		{"empty key", map[string]*string{"": s("x")}, nil, true},
		{"key with =", map[string]*string{"A=B": s("x")}, nil, true},
	}
	for _, tc := range cases {
		got, err := composeEnv(tc.in)
		if (err != nil) != tc.wantErr || (!tc.wantErr && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("%s: composeEnv = %v, %v; want %v (err=%v)", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestComposeEnvFunctionRun(t *testing.T) {
	m := types.MapValueMust(types.StringType, map[string]attr.Value{"Z": types.StringValue("1"), "A": types.StringNull()})
	v, ferr := p3eRunFunc(t, NewComposeEnvFunction(), m)
	if ferr != nil {
		t.Fatal(ferr)
	}
	l := v.(types.List).Elements()
	if len(l) != 2 || p3eAttrString(l[0]) != "A" || p3eAttrString(l[1]) != "Z=1" {
		t.Errorf("got %v", l)
	}
	bad := types.MapValueMust(types.StringType, map[string]attr.Value{"A=B": types.StringValue("1")})
	if _, ferr := p3eRunFunc(t, NewComposeEnvFunction(), bad); ferr == nil || *ferr.FunctionArgument != 0 {
		t.Errorf("want argument error, got %v", ferr)
	}
}
