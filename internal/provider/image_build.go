package provider

// Pure helpers for docker_image / docker_registry_image: build-context tar
// creation with .dockerignore, context hashing, Engine JSON message stream
// decoding and digest parsing. Nothing here talks to a daemon.

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// imgDefaultDockerfile is the Dockerfile name used when build.dockerfile is unset.
const imgDefaultDockerfile = "Dockerfile"

// imgContextFile is one entry of a build context, in walk order.
type imgContextFile struct {
	// Rel is the slash-separated path inside the tar.
	Rel string
	// Abs is the path on disk.
	Abs  string
	Info fs.FileInfo
	// Link is the symlink target, for symlinks.
	Link string
}

// imgBuildContext describes what will be sent to the daemon.
type imgBuildContext struct {
	Files []imgContextFile
	// Dockerfile is the name the daemon must use (relative to the tar root).
	Dockerfile string
	// extraDockerfile is set when the Dockerfile lives outside the context:
	// its content is added to the tar under Dockerfile.
	extraDockerfile []byte
}

// imgReadDockerignore returns the patterns in <dir>/.dockerignore, or nil.
func imgReadDockerignore(dir string) ([]string, error) {
	f, err := os.Open(filepath.Join(dir, ".dockerignore"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ignorefile.ReadAll(f)
}

// imgResolveDockerfile maps build.dockerfile (relative to the context, or
// absolute) to the in-tar name. When the file lives outside the context its
// content is returned so it can be injected under a generated name, as the
// docker CLI does.
func imgResolveDockerfile(contextDir, dockerfile string) (name string, outside []byte, err error) {
	if dockerfile == "" {
		dockerfile = imgDefaultDockerfile
	}
	abs := dockerfile
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(contextDir, dockerfile)
	}
	rel, err := filepath.Rel(contextDir, abs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel), nil, nil
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", nil, fmt.Errorf("reading dockerfile %s: %w", abs, err)
	}
	sum := sha256.Sum256(b)
	return ".dockerfile." + hex.EncodeToString(sum[:])[:12], b, nil
}

// imgCollectContext walks contextDir and returns the files the build would
// send, honouring .dockerignore. The Dockerfile and .dockerignore are always
// included, like the docker CLI does.
func imgCollectContext(contextDir, dockerfile string) (*imgBuildContext, error) {
	contextDir, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(contextDir)
	if err != nil {
		return nil, fmt.Errorf("build context: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("build context %s is not a directory", contextDir)
	}
	dfName, dfOutside, err := imgResolveDockerfile(contextDir, dockerfile)
	if err != nil {
		return nil, err
	}
	if dfOutside == nil {
		if _, err := os.Stat(filepath.Join(contextDir, filepath.FromSlash(dfName))); err != nil {
			return nil, fmt.Errorf("dockerfile %q not found in build context %s: %w", dfName, contextDir, err)
		}
	}
	patterns, err := imgReadDockerignore(contextDir)
	if err != nil {
		return nil, fmt.Errorf("reading .dockerignore: %w", err)
	}
	if len(patterns) > 0 {
		// Same trick as the CLI's TrimBuildFilesFromExcludes: re-include the
		// files the daemon needs.
		patterns = append(patterns, "!.dockerignore")
		if dfOutside == nil {
			patterns = append(patterns, "!"+dfName)
		}
	}
	var pm *patternmatcher.PatternMatcher
	if len(patterns) > 0 {
		pm, err = patternmatcher.New(patterns)
		if err != nil {
			return nil, fmt.Errorf("invalid .dockerignore pattern: %w", err)
		}
	}

	out := &imgBuildContext{Dockerfile: dfName, extraDockerfile: dfOutside}
	err = filepath.WalkDir(contextDir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == contextDir {
			return nil
		}
		rel, err := filepath.Rel(contextDir, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if pm != nil {
			//nolint:staticcheck // MatchesOrParentMatches is fine for one-shot walks.
			excluded, err := pm.MatchesOrParentMatches(relSlash)
			if err != nil {
				return err
			}
			if excluded {
				if d.IsDir() && !pm.Exclusions() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		f := imgContextFile{Rel: relSlash, Abs: p, Info: info}
		if info.Mode()&os.ModeSymlink != 0 {
			if f.Link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			// Sockets, devices, pipes: the CLI skips them too.
			return nil
		}
		out.Files = append(out.Files, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// imgTarHeader builds a reproducible tar header for f.
func imgTarHeader(f imgContextFile) (*tar.Header, error) {
	h, err := tar.FileInfoHeader(f.Info, f.Link)
	if err != nil {
		return nil, err
	}
	h.Name = f.Rel
	if f.Info.IsDir() {
		h.Name += "/"
	}
	h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
	// Like moby's archive.FileInfoHeader: PAX format, but whole-second mtime and
	// no atime/ctime, so ordinary entries need no PAX records. BuildKit sniffs the
	// first tar block to tell a context archive from a bare Dockerfile; a leading
	// PAX record made it read the whole upload as the Dockerfile.
	h.Format = tar.FormatPAX
	h.ModTime = h.ModTime.Truncate(time.Second)
	h.AccessTime, h.ChangeTime = time.Time{}, time.Time{}
	if runtime.GOOS == "windows" {
		// Windows has no exec bit; mirror the CLI's chmodTarEntry.
		perm := h.Mode & 0o755
		h.Mode = (h.Mode &^ 0o777) | perm | 0o111
	}
	return h, nil
}

// imgWriteTar writes the build context as a tar stream to w.
func imgWriteTar(bc *imgBuildContext, w io.Writer) error {
	tw := tar.NewWriter(w)
	for _, f := range bc.Files {
		h, err := imgTarHeader(f)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if f.Info.Mode().IsRegular() {
			if err := imgCopyFile(tw, f.Abs); err != nil {
				return err
			}
		}
	}
	if bc.extraDockerfile != nil {
		h := &tar.Header{Name: bc.Dockerfile, Mode: 0o644, Size: int64(len(bc.extraDockerfile)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(bc.extraDockerfile); err != nil {
			return err
		}
	}
	return tw.Close()
}

func imgCopyFile(w io.Writer, p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// imgTarReader streams the build context through a pipe.
func imgTarReader(bc *imgBuildContext) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(imgWriteTar(bc, pw))
	}()
	return pr
}

// imgContextDigest hashes what the build would send: paths, file types,
// permission bits, symlink targets and file content (not mtimes), plus the
// Dockerfile name. Editing, adding or removing an included file changes it.
func imgContextDigest(bc *imgBuildContext) (string, error) {
	h := sha256.New()
	files := append([]imgContextFile(nil), bc.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	fmt.Fprintf(h, "dockerfile\x00%s\x00", bc.Dockerfile)
	for _, f := range files {
		mode := f.Info.Mode()
		fmt.Fprintf(h, "%s\x00%s\x00%o\x00%s\x00", f.Rel, imgFileKind(mode), mode.Perm(), f.Link)
		if mode.IsRegular() {
			if err := imgCopyFile(h, f.Abs); err != nil {
				return "", err
			}
			h.Write([]byte{0})
		}
	}
	if bc.extraDockerfile != nil {
		h.Write(bc.extraDockerfile)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func imgFileKind(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "d"
	case m&os.ModeSymlink != 0:
		return "l"
	default:
		return "f"
	}
}

// ---------------------------------------------------------------------------
// JSON message streams

// imgStreamResult collects what callers need from a build/pull/push stream.
type imgStreamResult struct {
	// Aux holds every aux payload in order.
	Aux []json.RawMessage
	// Tail keeps the last lines of human output, for error context.
	Tail []string
}

const imgStreamTailLines = 20

// imgDecodeStream reads an Engine JSON message stream to the end. The first
// errorDetail (or legacy error) message is returned as an error, with the
// last lines of build output attached. onMsg, when non-nil, sees every message.
func imgDecodeStream(r io.Reader, onMsg func(jsonmessage.JSONMessage)) (imgStreamResult, error) {
	var res imgStreamResult
	dec := json.NewDecoder(r)
	for {
		var m jsonmessage.JSONMessage
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return res, nil
			}
			return res, fmt.Errorf("decoding daemon output: %w", err)
		}
		if onMsg != nil {
			onMsg(m)
		}
		if m.Error != nil || m.ErrorMessage != "" {
			msg := m.ErrorMessage
			if m.Error != nil && m.Error.Message != "" {
				msg = m.Error.Message
			}
			return res, &imgStreamError{Message: msg, Tail: res.Tail}
		}
		if m.Aux != nil {
			res.Aux = append(res.Aux, *m.Aux)
		}
		line := strings.TrimRight(m.Stream, "\r\n")
		if line == "" && m.Status != "" {
			line = strings.TrimSpace(m.ID + " " + m.Status)
		}
		if line != "" {
			res.Tail = append(res.Tail, line)
			if len(res.Tail) > imgStreamTailLines {
				res.Tail = res.Tail[len(res.Tail)-imgStreamTailLines:]
			}
		}
	}
}

// imgStreamError is an error reported inside the message stream.
type imgStreamError struct {
	Message string
	Tail    []string
}

func (e *imgStreamError) Error() string {
	if len(e.Tail) == 0 {
		return e.Message
	}
	return e.Message + "\n\nLast output:\n" + strings.Join(e.Tail, "\n")
}

// imgBuiltImageID extracts the image ID from build aux messages ({"ID": ...}).
func imgBuiltImageID(aux []json.RawMessage) string {
	id := ""
	for _, a := range aux {
		var v struct {
			ID string `json:"ID"`
		}
		if json.Unmarshal(a, &v) == nil && v.ID != "" {
			id = v.ID
		}
	}
	return id
}

// imgPushedDigest extracts the manifest digest from push aux messages
// ({"Tag": ..., "Digest": ..., "Size": ...}).
func imgPushedDigest(aux []json.RawMessage) string {
	d := ""
	for _, a := range aux {
		var v struct {
			Digest string `json:"Digest"`
		}
		if json.Unmarshal(a, &v) == nil && v.Digest != "" {
			d = v.Digest
		}
	}
	return d
}

// ---------------------------------------------------------------------------
// References and digests

// imgDigestFromRepoDigest returns the "sha256:..." part of "repo@sha256:...".
func imgDigestFromRepoDigest(rd string) string {
	if _, d, ok := strings.Cut(rd, "@"); ok {
		return d
	}
	return ""
}

// imgPickRepoDigest returns the RepoDigests entry for name's repository, or
// the first entry, or "".
func imgPickRepoDigest(name string, repoDigests []string) string {
	if len(repoDigests) == 0 {
		return ""
	}
	if want, err := reference.ParseNormalizedNamed(name); err == nil {
		for _, rd := range repoDigests {
			repo, _, _ := strings.Cut(rd, "@")
			if got, err := reference.ParseNormalizedNamed(repo); err == nil && got.Name() == want.Name() {
				return rd
			}
		}
	}
	return repoDigests[0]
}

// imgRepoAndTag splits a reference into its registry host, repository path
// (without host) and tag-or-digest, normalising Docker Hub names
// ("nginx" -> "docker.io", "library/nginx", "latest").
func imgRepoAndTag(name string) (host, repo, tagOrDigest string, err error) {
	named, err := reference.ParseNormalizedNamed(name)
	if err != nil {
		return "", "", "", err
	}
	host = reference.Domain(named)
	repo = reference.Path(named)
	switch r := named.(type) {
	case reference.Canonical:
		tagOrDigest = r.Digest().String()
	case reference.Tagged:
		tagOrDigest = r.Tag()
	default:
		tagOrDigest = "latest"
	}
	return host, repo, tagOrDigest, nil
}

// imgIsIDRef reports whether ref is (a prefix of) the image ID id, with or
// without the "sha256:" algorithm prefix.
func imgIsIDRef(ref, id string) bool {
	if ref == "" {
		return false
	}
	return strings.HasPrefix(id, ref) || strings.HasPrefix(id, "sha256:"+ref)
}
