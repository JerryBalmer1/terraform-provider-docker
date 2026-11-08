package provider

// Conversions between the provider's string attributes and the netip types
// that github.com/moby/moby/api uses for addresses, subnets and IP ranges.

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/moby/moby/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// parsePrefixes parses CIDR strings; field names the attribute in errors.
func parsePrefixes(field string, in []string) ([]netip.Prefix, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a CIDR subnet: %w", field, s, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// prefixStrings formats prefixes as CIDR strings (nil for none).
func prefixStrings(in []netip.Prefix) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		out = append(out, p.String())
	}
	return out
}

// parseOptPrefix parses an optional CIDR: "" is the zero Prefix.
func parseOptPrefix(field, s string) (netip.Prefix, error) {
	if s == "" {
		return netip.Prefix{}, nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%s: %q is not a CIDR subnet: %w", field, s, err)
	}
	return p, nil
}

// parseOptAddr parses an optional IP address: "" is the zero Addr.
func parseOptAddr(field, s string) (netip.Addr, error) {
	if s == "" {
		return netip.Addr{}, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%s: %q is not an IP address: %w", field, s, err)
	}
	return a, nil
}

// prefixString formats an optional prefix ("" for the zero value).
func prefixString(p netip.Prefix) string {
	if !p.IsValid() {
		return ""
	}
	return p.String()
}

// addrString formats an optional address ("" for the zero value).
func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// parseAddrs parses a list of IP addresses.
func parseAddrs(field string, in []string) ([]netip.Addr, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]netip.Addr, 0, len(in))
	for _, s := range in {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not an IP address: %w", field, s, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// addrStrings formats addresses (nil for none).
func addrStrings(in []netip.Addr) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, a := range in {
		out = append(out, a.String())
	}
	return out
}

// platformFromString splits an "os[/arch[/variant]]" platform string into
// the OCI Platform the moby client takes. The client joins the parts back
// with "/" on the wire, so the daemon sees exactly the string the user wrote.
// "" is the zero Platform (no platform sent).
func platformFromString(s string) ocispec.Platform {
	if s == "" {
		return ocispec.Platform{}
	}
	parts := strings.SplitN(s, "/", 3)
	p := ocispec.Platform{OS: parts[0]}
	if len(parts) > 1 {
		p.Architecture = parts[1]
	}
	if len(parts) > 2 {
		p.Variant = parts[2]
	}
	return p
}

// platformsFromString is platformFromString for the list-shaped options
// (pull, build, save): nil for "".
func platformsFromString(s string) []ocispec.Platform {
	if s == "" {
		return nil
	}
	return []ocispec.Platform{platformFromString(s)}
}

// platformPtrFromString is platformFromString for pointer-shaped options
// (container create, push): nil for "".
func platformPtrFromString(s string) *ocispec.Platform {
	if s == "" {
		return nil
	}
	p := platformFromString(s)
	return &p
}

// containerParseDNS parses the dns list (docker_container) into addresses,
// reporting a bad entry as an error on the dns attribute.
func containerParseDNS(in []string, diags *diag.Diagnostics) []netip.Addr {
	out, err := parseAddrs("dns", in)
	if err != nil {
		diags.AddAttributeError(path.Root("dns"), "Invalid dns", err.Error())
	}
	return out
}

// ipamConfigFrom builds one IPAM pool from its string form (subnet,
// ip_range, gateway, aux_address); empty strings stay unset.
func ipamConfigFrom(subnet, ipRange, gateway string, aux map[string]string) (network.IPAMConfig, error) {
	var c network.IPAMConfig
	var err error
	if c.Subnet, err = parseOptPrefix("subnet", subnet); err != nil {
		return c, err
	}
	if c.IPRange, err = parseOptPrefix("ip_range", ipRange); err != nil {
		return c, err
	}
	if c.Gateway, err = parseOptAddr("gateway", gateway); err != nil {
		return c, err
	}
	if len(aux) > 0 {
		c.AuxAddress = make(map[string]netip.Addr, len(aux))
		for k, v := range aux {
			a, err := netip.ParseAddr(v)
			if err != nil {
				return c, fmt.Errorf("aux_address %s: %q is not an IP address: %w", k, v, err)
			}
			c.AuxAddress[k] = a
		}
	}
	return c, nil
}

// auxAddressStrings formats an IPAM pool's auxiliary addresses (nil for none).
func auxAddressStrings(in map[string]netip.Addr) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v.String()
	}
	return out
}

// exposedPorts parses an "expose"-style spec ("80", "80/udp", "8000-8010")
// into the individual ports. github.com/moby/moby/api has no range-valued
// Port (go-connections nat.Port allowed "8000-8010/tcp" as one key, which
// the daemon expanded), so ranges are expanded here.
func exposedPorts(spec string) ([]network.Port, error) {
	pr, err := network.ParsePortRange(spec)
	if err != nil {
		return nil, err
	}
	var out []network.Port
	for p := range pr.All() {
		out = append(out, p)
	}
	return out, nil
}

// platformsString formats the first platform of a list option back to
// "os/arch[/variant]" ("" for none), for logs and messages.
func platformsString(ps []ocispec.Platform) string {
	if len(ps) == 0 {
		return ""
	}
	p := ps[0]
	return strings.TrimRight(strings.Join([]string{p.OS, p.Architecture, p.Variant}, "/"), "/")
}
