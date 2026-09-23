package stacktrace

import (
	"path/filepath"
	"strings"
)

// pathSegmentExclusions lists path components that mark a frame as vendor,
// stdlib, or runtime-internal code rather than application code, tested as
// whole "/"-separated path segments (so a project directory that merely
// contains the substring, e.g. "internal_tools", is not excluded).
var pathSegmentExclusions = []string{
	"node_modules",
	"site-packages",
	"dist-packages",
	"vendor",
	".bundle",
	"gems",
	"internal", // legacy pre-"node:" Node internals; Deno's ext:deno_node/internal/*
}

// pathSubstringExclusions lists pseudo-scheme prefixes and markers that
// never denote a real path under the git root, so a plain substring test is
// enough (they can't collide with a legitimate project path segment).
var pathSubstringExclusions = []string{
	"node:internal",
	"bun:",
	"deno:",
	"ext:",
	"<anonymous>",
}

// InApp reports whether frame f is application code: a real file under
// gitRoot that isn't vendored, a stdlib/runtime-internal path, or a
// pseudo-path the runtime prints for internal/synthetic frames.
//
// It never touches the filesystem; gitRoot is a plain string prefix/root
// used for the relative-path test, not a live repository handle.
func InApp(f Frame, gitRoot string) bool {
	path := f.AbsPath
	if path == "" {
		path = f.Filename
	}
	if path == "" {
		return false
	}
	for _, marker := range pathSubstringExclusions {
		if strings.Contains(path, marker) {
			return false
		}
	}
	if hasGOROOTSrc(path) {
		return false
	}
	if strings.Contains(path, "/usr/lib/ruby") {
		return false
	}
	if hasExcludedSegment(path) {
		return false
	}
	if gitRoot == "" {
		return false
	}
	return underRoot(path, gitRoot)
}

// hasExcludedSegment reports whether any "/"-delimited segment of path
// exactly matches one of pathSegmentExclusions.
func hasExcludedSegment(path string) bool {
	clean := strings.ReplaceAll(path, "\\", "/")
	for _, seg := range strings.Split(clean, "/") {
		for _, excl := range pathSegmentExclusions {
			if seg == excl {
				return true
			}
		}
	}
	return false
}

// hasGOROOTSrc reports whether path looks like it lives under a Go
// toolchain's GOROOT/src (e.g. ".../go/src/runtime/panic.go" or the
// "runtime/", "internal/" packages the standard library itself uses). Since
// this package never shells out to `go env GOROOT`, it recognizes the
// canonical "/src/" layout Go toolchains use instead of comparing against
// an actual GOROOT value.
func hasGOROOTSrc(path string) bool {
	clean := strings.ReplaceAll(path, "\\", "/")
	return strings.Contains(clean, "/go/src/") || strings.HasPrefix(clean, "src/")
}

// underRoot reports whether path is a real filesystem path located inside
// gitRoot. A relative path is treated as already-relative-to-root (the
// common case for Ruby's bare "workload.rb"-style filenames), so it is
// in_app by default unless it was already excluded above.
func underRoot(path, gitRoot string) bool {
	if !filepath.IsAbs(path) {
		return true
	}
	root := filepath.Clean(gitRoot)
	abs := filepath.Clean(path)
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
