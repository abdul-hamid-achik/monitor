package explain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readSnippet reads +-context lines around line from root/relFile, confined
// to root (EvalSymlinks'd on both the root and the resolved file, so a
// symlink cannot walk the read outside the git root a caller trusted). It
// never reads outside root: a relFile that is empty, absolute, or climbs
// above root (".." anywhere in its cleaned form) is rejected outright,
// before any filesystem call.
//
// Returns (nil, reason) -- never an error -- when the read cannot be
// trusted or completed; the caller (culprit.go) turns that into a
// Degraded entry instead of a fabricated snippet.
func readSnippet(root, relFile string, line, context int) (*Snippet, string) {
	if root == "" {
		return nil, "no git root resolved"
	}
	if relFile == "" || line <= 0 {
		return nil, "culprit has no file:line"
	}
	clean := filepath.Clean(relFile)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, fmt.Sprintf("refusing to read outside the git root: %q", relFile)
	}

	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Sprintf("resolve git root: %v", err)
	}
	candidate := filepath.Join(absRoot, clean)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, fmt.Sprintf("read %s: %v", relFile, err)
	}
	rootWithSep := absRoot + string(filepath.Separator)
	if resolved != absRoot && !strings.HasPrefix(resolved, rootWithSep) {
		return nil, fmt.Sprintf("resolved path escapes the git root: %q", relFile)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Sprintf("read %s: %v", relFile, err)
	}
	all := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	// A file ending in a newline (the common case) splits into one trailing
	// "" element that is not a real source line; drop it so a 3-line file
	// "a\nb\nc\n" reports 3 lines, not 4.
	if n := len(all); n > 0 && all[n-1] == "" {
		all = all[:n-1]
	}
	if line > len(all) {
		return nil, fmt.Sprintf("line %d is past %s's current end (%d lines) -- the file has likely changed since this was recorded", line, relFile, len(all))
	}

	if context < 0 {
		context = 0
	}
	start := line - context
	if start < 1 {
		start = 1
	}
	end := line + context
	if end > len(all) {
		end = len(all)
	}
	lines := append([]string(nil), all[start-1:end]...)

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return &Snippet{
		Start:     start,
		Lines:     lines,
		Highlight: line,
		SHA256:    hex.EncodeToString(sum[:]),
	}, ""
}
