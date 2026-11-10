// Command credhelper is a fake docker credential helper for the provider's
// unit tests (provider_auth_r_test.go builds it as docker-credential-tfacc).
// It speaks the credential-helper protocol's `get`: the server URL on stdin,
// {"ServerURL","Username","Secret"} JSON on stdout, and the "credentials not
// found" message with exit status 1 for an unknown server. Every call is
// appended to the file named by TFACC_CREDHELPER_LOG.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "get" {
		fmt.Fprintln(os.Stderr, "credhelper: only get is supported")
		os.Exit(1)
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	server := strings.TrimSpace(string(b))
	if lf := os.Getenv("TFACC_CREDHELPER_LOG"); lf != "" {
		if f, err := os.OpenFile(lf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, server)
			_ = f.Close()
		}
	}
	var user, secret string
	switch server {
	case "helper.example":
		user, secret = "u-"+server, "s3cret"
	case "token.example":
		// "<token>" marks an identity token in the helper protocol.
		user, secret = "<token>", "refresh-token"
	default:
		fmt.Print("credentials not found in native keychain")
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"ServerURL": server, "Username": user, "Secret": secret})
}
