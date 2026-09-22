package mockups

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Set is one mockup set's index.json manifest, the file the /human-mockups
// skill writes next to each option's static HTML. The desktop's MockupSet
// mirrors it for the Wails board; the daemon's /api surface serialises it for
// the web sandbox. One shape, two readers — this package is the authority.
type Set struct {
	Feature string `json:"feature"`
	Slug    string `json:"slug"`
	Created string `json:"created"`
	Project string `json:"project,omitempty"`
	// Ticket is the PM key a ticket-linked invocation recorded in the
	// manifest — recovery metadata; the authoritative ticket→set link is the
	// project's .human/mockups.json.
	Ticket string `json:"ticket,omitempty"`
	// Parent is the slug of the group this one was varied from; empty for a
	// root group. ParentFile is the option HTML file within Parent that was
	// varied. Instructions is the free-text change request that produced this
	// group. All three are written by the human-mockups skill in variation
	// mode.
	Parent       string    `json:"parent,omitempty"`
	ParentFile   string    `json:"parentFile,omitempty"`
	Instructions string    `json:"instructions,omitempty"`
	Options      []SetItem `json:"options"`
}

// SetItem is one option within a set: a static HTML file and its label.
type SetItem struct {
	N           int    `json:"n"`
	Name        string `json:"name"`
	File        string `json:"file"`
	Description string `json:"description,omitempty"`
}

// Project is a directory to scan for mockups/. Declared here rather than
// imported from internal/daemon: the daemon imports this package for its /api
// surface, so the dependency must not point back.
type Project struct {
	Name string
	Dir  string
}

// ValidSet reports whether setDir holds a manifest the viewer would accept
// — parses, and carries at least one option. The desktop's card-link check
// uses it so "View mocks" never points at a set the viewer will not list.
func ValidSet(setDir string) bool {
	data, err := os.ReadFile(filepath.Join(setDir, "index.json")) // #nosec G304 — caller-supplied project dirs
	if err != nil {
		return false
	}
	var set Set
	return json.Unmarshal(data, &set) == nil && len(set.Options) > 0
}

// ScanSets scans every project root for mockups/<slug>/index.json and returns
// the parsed manifests, newest first. Directories without a valid manifest are
// skipped — the skill always writes one, and guessing at loose HTML files
// would put unlabeled content in the viewer. The first project to claim a
// slug wins: the slug is the URL key, so a cross-project duplicate cannot be
// served unambiguously. The returned slugDirs map (slug → set directory) backs
// the static file server; it is rebuilt on every call.
func ScanSets(projects []Project) (sets []Set, slugDirs map[string]string, err error) {
	sets = []Set{}
	slugDirs = map[string]string{}
	seen := map[string]string{}
	for _, p := range projects {
		entries, readErr := os.ReadDir(filepath.Join(p.Dir, "mockups"))
		if readErr != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				// Dot-prefixed dirs hold pruned/archived subtrees
				// (mockups/.archive/…); they must not surface.
				continue
			}
			setDir := filepath.Join(p.Dir, "mockups", e.Name())
			// #nosec G304 — e.Name() is the os.ReadDir entry (no separators),
			// p.Dir the registered project root; the same construct ScanSet
			// reads under the same exemption.
			data, readErr := os.ReadFile(filepath.Join(setDir, "index.json"))
			if readErr != nil {
				continue
			}
			var set Set
			if json.Unmarshal(data, &set) != nil || len(set.Options) == 0 {
				continue
			}
			if set.Slug == "" {
				set.Slug = e.Name()
			}
			set.Project = p.Name
			if _, dup := seen[set.Slug]; dup {
				continue
			}
			seen[set.Slug] = setDir
			slugDirs[set.Slug] = setDir
			sets = append(sets, set)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Created > sets[j].Created })
	return sets, slugDirs, nil
}

// ErrSetNotFound is returned by ScanSet for a slug no scanned project claims.
var ErrSetNotFound = errors.New("mockup set not found")

// ScanSet returns the single set with the given slug, plus its directory on
// disk. It scans afresh — a freshly generated set appears without any cache
// invalidation story, matching how the desktop's view behaves.
func ScanSet(projects []Project, slug string) (Set, string, error) {
	if slug == "" || strings.Contains(slug, "/") || strings.Contains(slug, "..") {
		return Set{}, "", ErrSetNotFound
	}
	for _, p := range projects {
		setDir := filepath.Join(p.Dir, "mockups", slug)
		if !ValidSet(setDir) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(setDir, "index.json")) // #nosec G304 — slug vetted above
		if err != nil {
			continue
		}
		var set Set
		if json.Unmarshal(data, &set) != nil || len(set.Options) == 0 {
			continue
		}
		if set.Slug == "" {
			set.Slug = slug
		}
		set.Project = p.Name
		return set, setDir, nil
	}
	return Set{}, "", ErrSetNotFound
}
