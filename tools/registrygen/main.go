// Command registrygen writes internal/provider/registry_gen.go (every
// resource, data source, action, ephemeral resource, list resource and
// function constructor, under the type name reference/inventory.json
// decides) and its test registry_gen_test.go.
//
//	go run ./tools/registrygen          # write both files
//	go run ./tools/registrygen -check   # exit 1 when either differs from the inventory
//	go run ./tools/registrygen -stubs   # also write Pass 5 stubs for constructors the package lacks
//
// Constructor names are derived, never listed: see Constructor. Run from the
// repository root (Invoke-Build RegistryGen / RegistryCheck).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	invPath := flag.String("inventory", "reference/inventory.json", "merged inventory")
	dir := flag.String("dir", "internal/provider", "provider package directory")
	check := flag.Bool("check", false, "fail if the generated files are stale instead of writing them")
	stubs := flag.Bool("stubs", false, "write Pass 5 stub constructors for every type the package does not define")
	lanes := flag.String("lanes", "reference/_gen/pass5/lanes.json", "item id -> lane map, for -stubs file placement")
	flag.Parse()
	if err := run(*invPath, *dir, *check, *stubs, *lanes); err != nil {
		fmt.Fprintln(os.Stderr, "registrygen:", err)
		os.Exit(1)
	}
}

func run(invPath, dir string, check, stubs bool, lanesPath string) error {
	inv, err := LoadInventory(invPath)
	if err != nil {
		return err
	}
	types, err := Types(inv)
	if err != nil {
		return err
	}
	src, test, err := Generate(types)
	if err != nil {
		return err
	}
	files := map[string][]byte{"registry_gen.go": src, "registry_gen_test.go": test}
	if check {
		stale := false
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || !bytes.Equal(normalizeEOL(got), want) {
				fmt.Fprintf(os.Stderr, "registrygen: %s is stale (Invoke-Build RegistryGen)\n", filepath.Join(dir, name))
				stale = true
			}
		}
		if stale {
			return fmt.Errorf("registration differs from %s", invPath)
		}
		fmt.Printf("registrygen: %d types match %s\n", len(types), invPath)
		return nil
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("registrygen: wrote %d types\n", len(types))
	if stubs {
		n, err := WriteStubs(types, dir, lanesPath)
		if err != nil {
			return err
		}
		fmt.Printf("registrygen: wrote %d stub constructors\n", n)
	}
	return nil
}

// normalizeEOL drops CRs so a checkout with autocrlf compares equal.
func normalizeEOL(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
