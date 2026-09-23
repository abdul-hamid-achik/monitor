package explain

import (
	"os"
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
