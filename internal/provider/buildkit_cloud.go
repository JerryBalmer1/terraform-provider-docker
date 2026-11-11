package provider

// Pass 6c Lane C: Docker Build Cloud, buildx's `cloud` driver
// (`docker buildx create --driver cloud <org>/<builder>`), through the
// existing BuildKit seam and without the buildx library.
//
// Read from docker/buildx v0.38.2 (3c4cbd35, reference/SOURCES.md),
// driver/cloud:
//   - factory.go:20-31: endpoint prefix "cloud://", auth host
//     https://auth.docker.io, BuildKit proxy tcp://build-cloud.docker.com:443,
//     Hub credentials under "https://index.docker.io/v1/", token service
//     "build.docker.com"; factory.go:186-194: the builder name is normalized
//     to <account>/<builder> and a token source logs in with it.
//   - create.go:56-76 (parseBuilderName / normalizeBuilderName): the
//     <account>/<builder> format and its error message.
//   - login.go:28-61 (login): GET <auth host>/token?service=build.docker.com
//     &scope=builder:<account>/<builder>:build with HTTP basic auth (Hub
//     username + password, or the identity token when one is stored,
//     login.go:117-136), answer {"access_token": ...}.
//   - login.go:138-229: retries on transport errors, 429 and 5xx (not 501),
//     5 retries, 100ms doubling to 10s, Retry-After honoured.
//   - login.go:231-258: the token is refreshed when older than a minute.
//   - driver.go:134-177 (Client): BuildKit at the proxy address over TLS
//     with the system roots, keepalive 10s, and gRPC unary and stream
//     interceptors that add the metadata "authorization: Bearer <token>"
//     plus buildx-version / user-context / user-platform
//     (factory.go:199-207, 213-233), and use the gzip compressor.
//   - driver.go:126-132: Stop and Rm do nothing (the builder is Docker's).
//
// What the provider does not do (see lane-C.md): buildx v0.38 resolves a
// builder group into per-platform instances through the Hub Cloud Builds
// API (create.go:79-210, called from builder/builder.go:353-367;
// hub.docker.com/v2/cloud-builds/...), each with its
// own data-plane proxy; the provider talks to the group endpoint at the
// default proxy, as factory.New does when no instance options are given.
// The "cloud pull" optimisation (driver.go:206-354) is not used: results
// are loaded through the session like any other non-docker builder.
//
// The token is held in memory by the connection's token source only. It is
// never written to state, to diagnostics or to the log.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	bkclient "github.com/moby/buildkit/client"
	"google.golang.org/grpc"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

const (
	bxbDriverCloud        = "cloud"
	bkDriverCloud         = "cloud"
	bkCloudEndpointPrefix = "cloud://"
	bkCloudService        = "build.docker.com"
	// bkCloudAuthEntry is where buildx reads the Hub credential
	// (factory.go:31 hubRegistryEntry).
	bkCloudAuthEntry       = dockerHubAuthKey
	bkCloudTokenRefresh    = time.Minute
	bkCloudRetryMax        = 5
	bkCloudRetryWaitMin    = 100 * time.Millisecond
	bkCloudRetryWaitMax    = 10 * time.Second
	bkCloudHeaderAuth      = "authorization"
	bkCloudHeaderVersion   = "buildx-version"
	bkCloudHeaderContext   = "user-context"
	bkCloudHeaderPlatform  = "user-platform"
	bkCloudFormatErrorText = "builder should be in the format: <account>/<builder>"
)

// Endpoints of Docker Build Cloud; vars so unit tests can point them at
// fakes.
var (
	bkCloudAuthHost     = "https://auth.docker.io"
	bkCloudProxyAddress = "tcp://build-cloud.docker.com:443"
	// bkCloudPlaintext dials the proxy without TLS (tests only).
	bkCloudPlaintext  = false
	bkCloudHTTPClient = &http.Client{Timeout: 30 * time.Second}
	bkCloudNow        = time.Now
)

// bkCloudParseBuilder splits "<account>/<builder>" (optionally prefixed with
// cloud://), as buildx's parseBuilderName.
func bkCloudParseBuilder(builder string) (account, name string, _ error) {
	builder = strings.TrimPrefix(builder, bkCloudEndpointPrefix)
	account, name, ok := strings.Cut(builder, "/")
	switch {
	case !ok, strings.Contains(name, "/"),
		account == "", account == ".", account == "..",
		name == "", name == ".", name == "..":
		return "", "", errors.New(bkCloudFormatErrorText)
	}
	return account, name, nil
}

// bkCloudBuilder normalizes an endpoint to "<account>/<builder>".
func bkCloudBuilder(endpoint string) (string, error) {
	a, n, err := bkCloudParseBuilder(endpoint)
	if err != nil {
		return "", err
	}
	return a + "/" + n, nil
}

// bkCloudCredentials returns the Docker Hub username and secret from the
// provider's credential chain (registry_auth for docker.io, then the
// docker config's credHelpers / credsStore / auths), as buildx's
// getUserCreds does with the CLI config. An identity token replaces the
// password.
func bkCloudCredentials(ctx context.Context, pd *providerData) (string, string, error) {
	c, ok := pd.registryCredentialFor(ctx, bkCloudAuthEntry)
	if !ok {
		return "", "", errors.New("no Docker Hub credentials: Docker Build Cloud needs a registry_auth block for docker.io " +
			"(username and password or an access token), or a `docker login` entry in the docker config")
	}
	user, secret := c.Username, c.Password
	if user == "" && c.Auth != "" {
		if b, err := base64.StdEncoding.DecodeString(c.Auth); err == nil {
			user, secret, _ = strings.Cut(string(b), ":")
		}
	}
	if c.IdentityToken != "" {
		secret = c.IdentityToken
	}
	if user == "" || secret == "" {
		return "", "", errors.New("the Docker Hub credential has no username or no secret; Docker Build Cloud needs both")
	}
	return user, secret, nil
}

// bkCloudDo sends req with buildx's retry policy (login.go:160-224).
func bkCloudDo(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := bkCloudHTTPClient.Do(req.Clone(req.Context()))
		retry := req.Context().Err() == nil &&
			(err != nil || resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented))
		if !retry {
			return resp, err
		}
		wait := min(bkCloudRetryWaitMin<<attempt, bkCloudRetryWaitMax)
		if resp != nil {
			if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && s >= 0 {
				wait = min(time.Duration(s)*time.Second, bkCloudRetryWaitMax)
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}
		if attempt == bkCloudRetryMax {
			if err != nil {
				return nil, fmt.Errorf("%s %s%s: giving up after %d attempts: %w", req.Method, req.URL.Host, req.URL.Path, attempt+1, err)
			}
			return nil, fmt.Errorf("%s %s%s: giving up after %d attempts (status %d)", req.Method, req.URL.Host, req.URL.Path, attempt+1, resp.StatusCode)
		}
		t := time.NewTimer(wait)
		select {
		case <-req.Context().Done():
			t.Stop()
			return nil, req.Context().Err()
		case <-t.C:
		}
	}
}

// bkCloudLogin gets a build-scoped token for builder ("<account>/<builder>")
// from the auth host (buildx login.go:28-61). The error never carries the
// credential or the token.
func bkCloudLogin(ctx context.Context, pd *providerData, builder string) (string, error) {
	user, secret, err := bkCloudCredentials(ctx, pd)
	if err != nil {
		return "", err
	}
	u := fmt.Sprintf("%s/token?service=%s&scope=builder:%s:build", strings.TrimSuffix(bkCloudAuthHost, "/"), bkCloudService, builder)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("creating the Docker Build Cloud token request: %w", err)
	}
	req.SetBasicAuth(user, secret)
	resp, err := bkCloudDo(req)
	if err != nil {
		return "", fmt.Errorf("getting a Docker Build Cloud token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("getting a Docker Build Cloud token for %s: %s (trace id %q)", builder, resp.Status, resp.Header.Get("x-trace-id"))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", errors.New("decoding the Docker Build Cloud token response failed")
	}
	if tok.AccessToken == "" {
		return "", errors.New("the Docker Build Cloud token response has no access_token")
	}
	return tok.AccessToken, nil
}

// bkCloudTokenSource caches the token in memory and refreshes it when older
// than a minute (buildx login.go:238-258).
func bkCloudTokenSource(fetch func(context.Context) (string, error)) func(context.Context) (string, error) {
	var mu sync.Mutex
	var token string
	var last time.Time
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if last.IsZero() || bkCloudNow().Sub(last) > bkCloudTokenRefresh {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			next, err := fetch(ctx)
			if err != nil {
				return "", err
			}
			token, last = next, bkCloudNow()
		}
		return token, nil
	}
}

// bkCloudUserContext is buildx's getUserContext (factory.go:213-233).
func bkCloudUserContext(env []string) string {
	if !slices.Contains(env, "CI=true") {
		return "local"
	}
	switch {
	case slices.Contains(env, "GITHUB_ACTIONS=true"):
		return "github"
	case slices.Contains(env, "GITLAB_CI=true"):
		return "gitlab"
	case slices.Contains(env, "CIRCLECI=true"):
		return "circleci"
	case slices.Contains(env, "TRAVIS=true"):
		return "travis"
	case slices.Contains(env, "BUILDKITE=true"):
		return "buildkite"
	case slices.ContainsFunc(env, func(v string) bool { return strings.HasPrefix(v, "JENKINS_HOME=") }):
		return "jenkins"
	}
	return "ci"
}

// bkCloudHeaders are the client metadata buildx sends (factory.go:199-207).
// buildx-version carries the buildx release the provider follows.
func bkCloudHeaders() []string {
	return []string{
		bkCloudHeaderVersion, contractBuildxVersion,
		bkCloudHeaderContext, bkCloudUserContext(os.Environ()),
		bkCloudHeaderPlatform, runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// bkCloudNode is the node of a cloud builder endpoint.
func bkCloudNode(name, endpoint string) (bkNode, error) {
	b, err := bkCloudBuilder(endpoint)
	if err != nil {
		return bkNode{}, err
	}
	if name == "" {
		name = bxbDriverCloud + "-" + strings.ReplaceAll(strings.ToLower(b), "/", "-")
	}
	return bkNode{Builder: name, Name: name + "0", Driver: bkDriverCloud, Endpoint: bkCloudEndpointPrefix + b, Status: bkStatusRunning}, nil
}

// bkCloudOpen connects to the Build Cloud BuildKit proxy for node, with a
// token source private to this connection.
func bkCloudOpen(ctx context.Context, pd *providerData, node bkNode) (*bkclient.Client, error) {
	builder, err := bkCloudBuilder(node.Endpoint)
	if err != nil {
		return nil, err
	}
	tokens := bkCloudTokenSource(func(ctx context.Context) (string, error) { return bkCloudLogin(ctx, pd, builder) })
	// Log in once up front so a credential problem is reported as such
	// instead of as a gRPC failure.
	if _, err := tokens(ctx); err != nil {
		return nil, err
	}
	headers := bkCloudHeaders()
	outgoing := func(ctx context.Context) (context.Context, error) {
		tok, err := tokens(ctx)
		if err != nil {
			return ctx, fmt.Errorf("fetching the Docker Build Cloud token: %w", err)
		}
		return metadata.AppendToOutgoingContext(ctx, append(slices.Clone(headers), bkCloudHeaderAuth, "Bearer "+tok)...), nil
	}
	unary := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, err := outgoing(ctx)
		if err != nil {
			return err
		}
		return invoker(ctx, method, req, reply, cc, append([]grpc.CallOption{grpc.UseCompressor(grpcgzip.Name)}, opts...)...)
	}
	stream := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx, err := outgoing(ctx)
		if err != nil {
			return nil, err
		}
		return streamer(ctx, desc, cc, method, append([]grpc.CallOption{grpc.UseCompressor(grpcgzip.Name)}, opts...)...)
	}
	var opts []bkclient.ClientOpt
	if !bkCloudPlaintext {
		opts = append(opts, bkclient.WithServerConfigSystem(""))
	}
	opts = append(opts,
		bkclient.WithGRPCDialOption(grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second})),
		bkclient.WithGRPCDialOption(grpc.WithChainUnaryInterceptor(unary)),
		bkclient.WithGRPCDialOption(grpc.WithChainStreamInterceptor(stream)),
	)
	c, err := bkclient.New(ctx, bkCloudProxyAddress, opts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to Docker Build Cloud at %s: %w", bkCloudProxyAddress, err)
	}
	return c, nil
}
