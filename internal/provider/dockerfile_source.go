package provider

// Lane D: the text sources of data.docker_dockerfile (path, url).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dockerfileMaxBytes bounds a Dockerfile read from a file or URL.
const dockerfileMaxBytes = 4 << 20

// dockerfileHTTPClient fetches `url` sources. Tests replace it with an
// httptest TLS client.
var dockerfileHTTPClient = &http.Client{Timeout: 60 * time.Second}

// dockerfileReadPath reads a local Dockerfile; it returns the absolute path.
func dockerfileReadPath(p string) (string, string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, dockerfileMaxBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(b) > dockerfileMaxBytes {
		return "", "", fmt.Errorf("%s is larger than %d bytes", abs, dockerfileMaxBytes)
	}
	return string(b), abs, nil
}

// dockerfileFetchURL downloads a raw Dockerfile over HTTPS (pure Go
// net/http; no git).
func dockerfileFetchURL(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("url must be an https:// URL of a raw Dockerfile (got %q)", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain, */*")
	resp, err := dockerfileHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", u.Redacted(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: HTTP %s", u.Redacted(), resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, dockerfileMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", u.Redacted(), err)
	}
	if len(b) > dockerfileMaxBytes {
		return "", fmt.Errorf("%s is larger than %d bytes", u.Redacted(), dockerfileMaxBytes)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return "", fmt.Errorf("%s returned HTML, not a raw Dockerfile (use the raw file URL)", u.Redacted())
	}
	return string(b), nil
}
