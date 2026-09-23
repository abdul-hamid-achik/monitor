package ecosystem

import (
	"context"
	"fmt"
	"os"
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
	info.Path = info.AllPaths[0]
	info.Version = shortBinaryVersion(ctx, info.Path)
	if len(info.AllPaths) > 1 {
		info.Shadowed = true
		info.Warning = fmt.Sprintf("%s resolves to %s; also found on PATH at %s",
			name, info.Path, strings.Join(info.AllPaths[1:], ", "))
	}
	return info
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
