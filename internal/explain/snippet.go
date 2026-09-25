package explain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// SEC-2: the frame's file must be a file git tracks under the root the
	// caller trusted. A forged frame (log injection of untrusted stderr)
	// naming .env, a gitignored secrets file or anything else the repo
	// never committed must not have its contents lifted into the MCP brief
	// or the --md page, next to an attacker-written title.
	if allowed, reason := snippetTrackedUnderRoot(absRoot, clean); !allowed {
		return nil, reason
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

// snippetTrackedUnderRoot is SEC-2's ingest gate for readSnippet: relFile
// (already cleaned and prefix-checked against the root) is readable only
// when git tracks it under absRoot -- `git -C absRoot ls-files
// --error-unmatch -- relFile` succeeds. A tracked file is committed source;
// anything else (a gitignored .env, a local secrets file, an untracked
// scratch file) stays out of the snippet even when it exists on disk under
// the root. When provenance cannot be verified at all -- git itself is
// unavailable, or absRoot is not inside a git work tree (a marker- or
// service-rooted project) -- the gate degrades to the gitlessFallback
// refusal below instead of pretending to have verified anything. The
// returned reason is the Degraded detail when refused.
func snippetTrackedUnderRoot(absRoot, relFile string) (bool, string) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		if gitlessFallbackRefused(relFile) {
			return false, fmt.Sprintf("git unavailable; refusing to read sensitive file %q unverified", relFile)
		}
		return true, ""
	}
	cmd := exec.Command(gitPath, "-C", absRoot, "ls-files", "--error-unmatch", "--", filepath.ToSlash(relFile))
	if err := cmd.Run(); err == nil {
		return true, ""
	}
	if errors.Is(err, exec.ErrNotFound) {
		// git vanished between LookPath and Run; degrade the same way.
		if gitlessFallbackRefused(relFile) {
			return false, fmt.Sprintf("git unavailable; refusing to read sensitive file %q unverified", relFile)
		}
		return true, ""
	}
	// ls-files failed. Outside a work tree that failure is git's "not a
	// git repository", which says nothing about the file -- degrade to
	// the fallback instead of locking snippets out of non-git projects.
	// (The rev-parse runs only on this failure path, so the common
	// tracked-file case still costs a single git invocation.)
	if !dirIsGitWorkTree(gitPath, absRoot) {
		if gitlessFallbackRefused(relFile) {
			return false, fmt.Sprintf("not a git repository; refusing to read sensitive file %q unverified", relFile)
		}
		return true, ""
	}
	// Inside a work tree, exit status 1 is git's "pathspec did not match
	// any file(s) known to git" (untracked or ignored): refuse.
	return false, fmt.Sprintf("refusing to read %s: not tracked by git under the snippet root", relFile)
}

// dirIsGitWorkTree reports whether dir sits inside a git work tree.
func dirIsGitWorkTree(gitPath, dir string) bool {
	out, err := exec.Command(gitPath, "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// gitlessFallbackRefused reports whether relFile must stay unreadable when
// git cannot be asked at all (SEC-2): dotfiles (which covers .env and
// .env.* variants) and anything under a .git/ directory are refused
// outright, while ordinary-looking paths are allowed through -- the gate
// degrades, it does not lock the whole snippet feature down.
func gitlessFallbackRefused(relFile string) bool {
	for _, part := range strings.Split(filepath.ToSlash(relFile), "/") {
		if part == ".git" || strings.HasPrefix(part, ".") || strings.HasPrefix(part, ".env") {
			return true
		}
	}
	return false
}
