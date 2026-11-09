package provider

// Registry v2 writes for docker_registry_manifest_list: manifest PUT and
// cross-repository blob mounts, with a bearer scope that covers several
// repositories.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// regDo sends one request to the registry of r (scheme picked by a first
// GET /v2/ when needed), answering one 401 challenge with scope. body is
// replayed on the retry.
func regDo(ctx context.Context, r *p3Registry, method, p string, body []byte, contentType, scope string) (*http.Response, error) {
	if r.base == "" {
		resp, err := r.send(ctx, "/v2/", nil)
		if err != nil {
			return nil, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	do := func() (*http.Response, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, r.base+p, rd)
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if r.authz != "" {
			req.Header.Set("Authorization", r.authz)
		}
		resp, err := r.hc.Do(req)
		r.regNoteAPIVersion(resp)
		return resp, err
	}
	resp, err := do()
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	authz, _, err := p3Authorize(ctx, r.hc, challenge, scope, r.user, r.pass)
	if err != nil {
		return nil, err
	}
	r.authz = authz
	return do()
}

// regExpect closes resp and returns an error unless its status is one of ok.
func regExpect(resp *http.Response, what string, ok ...int) error {
	defer resp.Body.Close()
	for _, c := range ok {
		if resp.StatusCode == c {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			return nil
		}
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s: %s: %s", what, resp.Status, strings.TrimSpace(string(b)))
}

// regPutManifest PUTs body as /v2/<repo>/manifests/<ref>.
func regPutManifest(ctx context.Context, r *p3Registry, ref, mediaType string, body []byte, scope string) error {
	resp, err := regDo(ctx, r, http.MethodPut, "/v2/"+r.repo+"/manifests/"+ref, body, mediaType, scope)
	if err != nil {
		return err
	}
	return regExpect(resp, "PUT manifest "+r.repo+":"+ref, http.StatusCreated, http.StatusOK, http.StatusAccepted)
}

// regBlobExists HEADs /v2/<repo>/blobs/<digest>.
func regBlobExists(ctx context.Context, r *p3Registry, digest, scope string) (bool, error) {
	resp, err := regDo(ctx, r, http.MethodHead, "/v2/"+r.repo+"/blobs/"+digest, nil, "", scope)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("HEAD blob %s@%s: %s", r.repo, digest, resp.Status)
	}
}

// regMountBlob mounts digest from repository from into r's repository
// (POST /v2/<repo>/blobs/uploads/?mount=<digest>&from=<from>). A registry
// that answers 202 (an upload session instead of a mount) cannot mount;
// the provider does not stream blobs between repositories, so that is an
// error.
func regMountBlob(ctx context.Context, r *p3Registry, digest, from, scope string) error {
	if ok, err := regBlobExists(ctx, r, digest, scope); err == nil && ok {
		return nil
	}
	q := url.Values{"mount": {digest}, "from": {from}}
	resp, err := regDo(ctx, r, http.MethodPost, "/v2/"+r.repo+"/blobs/uploads/?"+q.Encode(), nil, "", scope)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusAccepted {
		resp.Body.Close()
		return fmt.Errorf("registry did not mount blob %s from %s into %s (it opened an upload instead); push the image to %s first", digest, from, r.repo, r.repo)
	}
	return regExpect(resp, "mount blob "+digest+" from "+from, http.StatusCreated)
}
