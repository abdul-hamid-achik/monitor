package ecosystem

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BinaryInfo reports every PATH match for one tool binary, in PATH search
// order, so a shadowed duplicate (a dev build ahead of the versioned release
// a person actually wants) is visible instead of silently winning. This is
// the "one binary per consumer" hygiene check from the local-sentry roadmap
// (E0.3): a stale `~/go/bin/glyph` dev build shadowing brew's
// `/opt/homebrew/bin/glyph`, or duplicate `cairn`/`vecgrep` installs, silently
// makes local runs disagree with CI.
type BinaryInfo struct {
	Name string `json:"name"`
	// Available and Path mirror what exec.LookPath (and therefore an
	// unqualified invocation of Name) would actually resolve to and run.
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	// AllPaths lists every PATH entry that has an executable named Name, in
	// PATH search order; AllPaths[0] is always Path.
	AllPaths []string `json:"all_paths,omitempty"`
	Shadowed bool     `json:"shadowed,omitempty"`
	Warning  string   `json:"warning,omitempty"`
}

// ScanBinary reports Name's LookPath resolution plus every other PATH entry
// that also has an executable named Name, flagging a shadowed duplicate.
func ScanBinary(ctx context.Context, name string) BinaryInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	info := BinaryInfo{Name: name, AllPaths: scanPathEntries(name)}
	if len(info.AllPaths) == 0 {
		return info
	}
	info.Available = true
	// Path comes from exec.LookPath, the same resolution an unqualified
	// invocation of Name would actually use, so it agrees with the
	// top-level ToolStatus.path other consumers already read — rather than
	// just assuming AllPaths[0] (scanPathEntries walks $PATH directly and
	// can diverge from LookPath's own rules, e.g. ErrDot on a relative PATH
	// entry).
	resolved, err := exec.LookPath(name)
	if err != nil {
		resolved = info.AllPaths[0]
	}
	info.Path = resolved
	info.Version = shortBinaryVersion(ctx, info.Path)
	if others := distinctOtherFiles(info.Path, info.AllPaths); len(others) > 0 {
		info.Shadowed = true
		info.Warning = fmt.Sprintf("%s resolves to %s; also found on PATH at %s",
			name, info.Path, strings.Join(others, ", "))
	}
	return info
}

// distinctOtherFiles returns the entries of allPaths that refer to a
// genuinely different underlying file than path — resolving symlinks on
// both sides — deduplicated by that same file identity. Without this, the
// same physical binary reached through a symlinked PATH directory (e.g.
// ~/.local/bin -> ~/go/bin) or a symlinked binary is reported as a
// "shadowing" duplicate, which tells a user to go remove a duplicate that
// doesn't exist.
func distinctOtherFiles(path string, allPaths []string) []string {
	seen := map[string]bool{resolvedFileKey(path): true}
	var others []string
	for _, p := range allPaths {
		key := resolvedFileKey(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		others = append(others, p)
	}
	return others
}

// resolvedFileKey is a best-effort identity for a file path: the
// symlink-resolved absolute path when that succeeds, or the path itself
// (compared as text) when it doesn't — e.g. the file no longer exists
// between the scan and this check, which is harmless: it just falls back to
// the same textual comparison scanPathEntries already used.
func resolvedFileKey(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// scanPathEntries walks $PATH once (in order, de-duplicating repeated
// entries) and returns every directory that has an executable, regular file
// named name.
func scanPathEntries(name string) []string {
	var found []string
	seen := make(map[string]bool)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		// An empty PATH entry traditionally means "the current directory" on
		// POSIX shells; skip it rather than let cwd silently participate.
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		candidate := filepath.Join(dir, name)
		st, err := os.Stat(candidate)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			continue
		}
		found = append(found, candidate)
	}
	return found
}
