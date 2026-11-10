package main

// reference/_gen/drift-accepted.json: upstream drift reviewed and knowingly
// not mapped yet.
//
//	{
//	  "about": "...",
//	  "accepted": [
//	    {"item": "operation:ContainerFoo", "reason": "...", "date": "2026-11-09", "version_seen": "api/v1.57.0"}
//	  ]
//	}
//
// item is a finding's item: operation:<operationId>, a fieldcov pointer
// (engine:..., compose:..., bake:...), or version:<source> for a
// documentation-only source. A version:<source> entry accepts exactly the
// version_seen release, so the next release fails again; the other entries
// accept the item whatever version brings it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type accepted struct {
	Item        string `json:"item"`
	Reason      string `json:"reason"`
	Date        string `json:"date"`
	VersionSeen string `json:"version_seen"`
}

type acceptedFile struct {
	About    string     `json:"about"`
	Accepted []accepted `json:"accepted"`
}

// minReason is the shortest reason accepted (the same bar as a fieldmap.d skip).
const minReason = 20

func loadAccepted(path string) ([]accepted, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseAccepted(b)
}

func parseAccepted(b []byte) ([]accepted, error) {
	var f acceptedFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("drift-accepted.json: %w", err)
	}
	var problems []string
	seen := map[string]bool{}
	for i, a := range f.Accepted {
		at := fmt.Sprintf("accepted[%d] %q", i, a.Item)
		switch {
		case strings.TrimSpace(a.Item) == "":
			problems = append(problems, at+": item is required")
		case seen[a.Item]:
			problems = append(problems, at+": duplicate item")
		}
		seen[a.Item] = true
		if len(strings.TrimSpace(a.Reason)) < minReason {
			problems = append(problems, fmt.Sprintf("%s: reason must be at least %d characters", at, minReason))
		}
		if _, err := time.Parse("2006-01-02", a.Date); err != nil {
			problems = append(problems, at+": date must be YYYY-MM-DD")
		}
		if strings.TrimSpace(a.VersionSeen) == "" {
			problems = append(problems, at+": version_seen is required")
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("drift-accepted.json: %s", strings.Join(problems, "; "))
	}
	return f.Accepted, nil
}

// applyAccepted marks unmapped findings listed in the accepted file, and
// records the entries that matched nothing (stale: the item was mapped,
// re-pinned or the upstream moved again).
func applyAccepted(r *report, acc []accepted) {
	used := map[string]bool{}
	byItem := map[string]accepted{}
	for _, a := range acc {
		byItem[a.Item] = a
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.Status != statusUnmapped {
			continue
		}
		a, ok := byItem[f.Item]
		if !ok {
			continue
		}
		if f.Kind == kindVersion && a.VersionSeen != f.Version {
			continue
		}
		used[a.Item] = true
		f.Status = statusAccepted
		f.Detail = strings.TrimSpace(f.Detail + "; accepted " + a.Date + ": " + a.Reason)
	}
	for _, a := range acc {
		if !used[a.Item] {
			r.Stale = append(r.Stale, a.Item+" (version_seen "+a.VersionSeen+")")
		}
	}
}
