// Package project resolves a single, stable project/service identity from
// whatever local signals a caller has on hand -- an explicit flag,
// MONITOR_PROJECT, the nearest git root, the nearest package manifest, or a
// live process's name -- so every occurrence writer agrees on it.
//
// Before this package existed, `monitor watch --stash` and
// `monitor investigate` each derived project/service ad hoc and disagreed on
// the same event, splitting one problem into two separate issues (local
// Sentry roadmap, bug 15). Every occurrence writer should call Resolve
// instead of deriving project/service on its own.
//
// A monorepo commonly nests a service's manifest well below the
// repository's git root, e.g. <repo>/services/web-api/package.json with
// .git at <repo>. Resolve treats those as two independent walks so the
// repo becomes the project and the nested manifest's directory becomes the
// service: Resolve(Hints{Dir: "<repo>/services/web-api", PID: 1}) yields
// Identity{Slug: "repo", Service: "web-api"}.
package project

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvProject overrides project resolution process-wide, equivalent to an
// explicit --project flag but without adding one to every command that
// writes occurrences.
const EnvProject = "MONITOR_PROJECT"

// markerFiles are the package manifests Resolve looks for, checked
// nearest-first and independently of the .git walk (see findMarkerRoot).
var markerFiles = []string{"package.json", "go.mod", "pyproject.toml", "Gemfile", "Cargo.toml"}

// Hints carries every signal a caller has on hand. All fields are optional;
// Resolve degrades to "local" (or "host" for a PID-less event, e.g. a
// system-wide alert with no process attached) when nothing else resolves.
type Hints struct {
	// ExplicitProject is normally a --project flag. Highest precedence.
	ExplicitProject string
	// ExplicitService is normally a --name flag or an already-resolved
	// contextids.IDs.Service (itself sourced from MONITOR_SERVICE,
	// CHALUPA_SERVICE, or an explicit override). Highest service precedence.
	ExplicitService string
	// Dir seeds the git-root/marker walk: a process's cwd (preferred) or
	// its already-resolved codebase root. Empty performs no git/marker walk
	// at all unless UseWorkingDir is set (see below).
	Dir string
	// ProcessName is the leaf process's name, the last-resort fallback for
	// both project and service.
	ProcessName string
	// PID is the subject process. PID <= 0 marks a host-wide event with no
	// process attached (e.g. a disk/swap system alert), which resolves
	// straight to project "host" instead of walking Dir.
	PID int32
	// UseWorkingDir opts in to falling back to monitor's own os.Getwd()
	// when Dir is empty. Leave this false (the default) for any caller
	// describing ANOTHER process -- watch --stash and investigate both
	// describe an alerted or investigated process, never monitor itself.
	// An exited PID, a process whose cwd could not be read, or a
	// system-wide alert with no process attached must never be attributed
	// to whatever directory monitor happens to be running from (verified
	// misattribution: investigating a PID with no readable cwd recorded
	// project = monitor's own repo checkout). Only a caller that is
	// genuinely describing monitor's own working directory as the subject
	// should set this.
	UseWorkingDir bool
}

// Identity is the single resolved project/service identity every occurrence
// writer (watch --stash, investigate, and the MCP investigate path) uses.
type Identity struct {
	// Slug is the resolved project ("graphite", "host", "local", ...).
	Slug string
	// Service is the resolved service within Slug ("web-api", ...). Empty
	// when nothing resolved it.
	Service string
	// Root is the in-app root: paths under it are in_app frames; everything
	// else (node_modules, vendor, GOROOT, ...) is not. Currently always
	// equal to GitRoot.
	Root string
	// GitRoot is the directory containing .git, walked up from Dir. Empty
	// when Dir is not inside a git working tree (or a linked worktree /
	// submodule, which record .git as a file rather than a directory).
	GitRoot string
	// Source names which precedence rule produced Slug: "flag", "env",
	// "host", "git_root", "marker", "service", "process", or "local".
	Source string
}

// Resolve derives a single project/service identity.
//
// Project precedence: explicit flag > MONITOR_PROJECT > (PID<=0: "host") >
// basename(git root) > basename(nearest marker) > explicit service >
// process name > "local".
//
// Service precedence: explicit service > basename(nearest marker), only
// when it differs from the git root > process name.
//
// Project rule 5 ("explicit service") intentionally reads h.ExplicitService
// directly rather than the fully-resolved service: by the time project
// resolution reaches rule 5, rules 3-4 have already exhausted both the git
// root and every marker root, so service's own marker-based tier can never
// fire here either. Reusing the resolved value (which may itself have
// fallen back to the process name) would make project rule 6 unreachable
// whenever a process name is known.
func Resolve(h Hints) Identity {
	dir := strings.TrimSpace(h.Dir)
	if dir == "" && h.UseWorkingDir {
		if cwd, err := os.Getwd(); err == nil {
			dir = cwd
		}
	}
	gitRoot := findGitRoot(dir)
	markerRoot := findMarkerRoot(dir, gitRoot)

	service := resolveService(h, gitRoot, markerRoot)
	slug, source := resolveProject(h, gitRoot, markerRoot)

	return Identity{
		Slug:    slug,
		Service: service,
		Root:    gitRoot,
		GitRoot: gitRoot,
		Source:  source,
	}
}

func resolveProject(h Hints, gitRoot, markerRoot string) (string, string) {
	if v := strings.TrimSpace(h.ExplicitProject); v != "" {
		return v, "flag"
	}
	if v := strings.TrimSpace(os.Getenv(EnvProject)); v != "" {
		return v, "env"
	}
	if h.PID <= 0 {
		return "host", "host"
	}
	if gitRoot != "" {
		return baseName(gitRoot), "git_root"
	}
	if markerRoot != "" {
		return baseName(markerRoot), "marker"
	}
	if v := strings.TrimSpace(h.ExplicitService); v != "" {
		return v, "service"
	}
	if v := strings.TrimSpace(h.ProcessName); v != "" {
		return baseName(v), "process"
	}
	return "local", "local"
}

func resolveService(h Hints, gitRoot, markerRoot string) string {
	if v := strings.TrimSpace(h.ExplicitService); v != "" {
		return v
	}
	if markerRoot != "" && markerRoot != gitRoot {
		return baseName(markerRoot)
	}
	if v := strings.TrimSpace(h.ProcessName); v != "" {
		return baseName(v)
	}
	return ""
}

// findGitRoot walks up from start looking for a .git directory or file (a
// linked worktree or submodule records .git as a file, not a directory).
// Returns "" when start is not inside a git working tree.
func findGitRoot(start string) string {
	dir := absDir(start)
	for dir != "" {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// findMarkerRoot walks up from start looking for the nearest package
// manifest. Unlike findGitRoot, it never matches .git: in a monorepo the
// nearest manifest commonly sits well below the git root, and the two roots
// deliberately serve different purposes (project identity vs. service
// identity; see the package doc).
//
// stopAt bounds the walk to the repository: once dir reaches stopAt (the
// already-resolved git root) without finding a marker, the walk stops
// instead of continuing above it. Without this bound, a stray manifest
// above the repository -- a common ~/package.json, say -- would "differ
// from the git root" and become the service name for every process in
// every repo nested underneath it (verified: <tmp>/package.json plus
// <tmp>/projects/foo/.git with Dir=foo resolved Service to the outer
// directory's name instead of falling through to the process name).
// stopAt == "" (no git root was found) leaves the walk unbounded, as
// before: there is no repository to bound it to.
func findMarkerRoot(start, stopAt string) string {
	dir := absDir(start)
	for dir != "" {
		for _, name := range markerFiles {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return dir
			}
		}
		if stopAt != "" && dir == stopAt {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

func absDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

func baseName(path string) string {
	return filepath.Base(filepath.Clean(strings.TrimSpace(path)))
}
