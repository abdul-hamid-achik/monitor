package explain

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSnippetReturnsWindowAroundLine(t *testing.T) {
	root := t.TempDir()
	content := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	snippet, reason := readSnippet(root, "app.go", 5, 2)
	if reason != "" {
		t.Fatalf("reason = %q, want none", reason)
	}
	if snippet.Start != 3 || snippet.Highlight != 5 {
		t.Fatalf("snippet = %+v", snippet)
	}
	if got := strings.Join(snippet.Lines, ","); got != "3,4,5,6,7" {
		t.Fatalf("lines = %q", got)
	}
	if snippet.SHA256 == "" {
		t.Fatal("expected a non-empty sha256")
	}
}

func TestReadSnippetClampsWindowAtFileBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snippet, reason := readSnippet(root, "app.go", 1, 4)
	if reason != "" || snippet.Start != 1 {
		t.Fatalf("snippet = %+v, reason = %q", snippet, reason)
	}
	if got := strings.Join(snippet.Lines, ","); got != "a,b,c" {
		t.Fatalf("lines = %q", got)
	}
}

func TestReadSnippetRefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../secret.go", "/etc/passwd", "a/../../b.go"} {
		if snippet, reason := readSnippet(root, bad, 1, 2); snippet != nil || reason == "" {
			t.Fatalf("path %q: snippet = %+v, reason = %q, want a rejection reason", bad, snippet, reason)
		}
	}
}

func TestReadSnippetRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(root, "link.go")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	snippet, reason := readSnippet(root, "link.go", 1, 2)
	if snippet != nil || reason == "" {
		t.Fatalf("snippet = %+v, reason = %q, want a rejection reason", snippet, reason)
	}
}

func TestReadSnippetReportsLinePastEOF(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snippet, reason := readSnippet(root, "app.go", 50, 2)
	if snippet != nil || reason == "" {
		t.Fatalf("snippet = %+v, reason = %q, want a past-EOF reason", snippet, reason)
	}
}

func TestReadSnippetMissingRootOrFile(t *testing.T) {
	if snippet, reason := readSnippet("", "app.go", 1, 2); snippet != nil || reason == "" {
		t.Fatalf("empty root: snippet = %+v, reason = %q", snippet, reason)
	}
	if snippet, reason := readSnippet(t.TempDir(), "", 1, 2); snippet != nil || reason == "" {
		t.Fatalf("empty file: snippet = %+v, reason = %q", snippet, reason)
	}
	if snippet, reason := readSnippet(t.TempDir(), "app.go", 0, 2); snippet != nil || reason == "" {
		t.Fatalf("zero line: snippet = %+v, reason = %q", snippet, reason)
	}
}

// TestReadSnippetGateTracksGitProvenance is SEC-2's regression test: inside
// a git work tree only tracked files get snippets -- a forged frame naming
// a gitignored .env (the review's log-injection shape) or an untracked
// scratch file is refused instead of leaking its contents into the brief.
func TestReadSnippetGateTracksGitProvenance(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=spec", "GIT_AUTHOR_EMAIL=spec@example.com",
			"GIT_COMMITTER_NAME=spec", "GIT_COMMITTER_EMAIL=spec@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "seed")
	// .env is ignored and scratch.go stays untracked: both must be
	// refused even though they exist on disk under the root.
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD=dummy-value-for-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scratch.go"), []byte("x\ny\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if snippet, reason := readSnippet(root, "app.go", 2, 1); snippet == nil || reason != "" {
		t.Fatalf("tracked app.go: snippet = %+v, reason = %q, want a snippet", snippet, reason)
	}
	if snippet, reason := readSnippet(root, ".env", 1, 1); snippet != nil || reason == "" {
		t.Fatalf("ignored .env: snippet = %+v, reason = %q, want a refusal", snippet, reason)
	} else if !strings.Contains(reason, "not tracked by git") {
		t.Fatalf("ignored .env reason = %q, want the tracked-gate refusal", reason)
	}
	if snippet, reason := readSnippet(root, "scratch.go", 1, 1); snippet != nil || reason == "" {
		t.Fatalf("untracked scratch.go: snippet = %+v, reason = %q, want a refusal", snippet, reason)
	}
}

// TestReadSnippetGateDegradesOutsideGitRepo pins the gate's honest
// degradation: a root that is not a git work tree has no provenance to
// verify, so ordinary files still get snippets while dotfiles stay
// refused.
func TestReadSnippetGateDegradesOutsideGitRepo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD=dummy-value-for-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if snippet, reason := readSnippet(root, "app.go", 1, 1); snippet == nil || reason != "" {
		t.Fatalf("non-repo app.go: snippet = %+v, reason = %q, want a snippet", snippet, reason)
	}
	if snippet, reason := readSnippet(root, ".env", 1, 1); snippet != nil || reason == "" {
		t.Fatalf("non-repo .env: snippet = %+v, reason = %q, want a refusal", snippet, reason)
	}
}
