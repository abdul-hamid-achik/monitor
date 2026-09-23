package explain

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLongestLiteralFragment(t *testing.T) {
	cases := []struct {
		message string
		want    string
	}{
		{"reconcile failed: batch 42 has no rows", "reconcile failed: batch"},
		{"connection refused: 10.0.4.12:5432", "connection refused"},
		{"42", ""},               // all-dynamic
		{"no", ""},               // too short even though literal
		{"bad row 7", "bad row"}, // trailing digit trimmed, remaining literal long enough
		{"   ", ""},              // whitespace only
	}
	for _, c := range cases {
		if got := longestLiteralFragment(c.message); got != c.want {
			t.Errorf("longestLiteralFragment(%q) = %q, want %q", c.message, got, c.want)
		}
	}
}

func TestLongestLiteralFragmentCapsLength(t *testing.T) {
	long := ""
	for i := 0; i < 50; i++ {
		long += "reconcile failed badly in a very very long diagnostic sentence "
	}
	got := longestLiteralFragment(long)
	if len(got) > maxSearchFragmentLen {
		t.Fatalf("fragment length = %d, want <= %d", len(got), maxSearchFragmentLen)
	}
}

func TestCulpritForMessageUsesVecgrepWhenHealthy(t *testing.T) {
	root := newGitRepo(t, map[string]string{"src/reconcile.py": "def reconcile():\n    if not rows:\n        log.error(\"reconcile failed: batch has no rows\")\n"})
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  status) printf '%s' '{"stats":{"chunks":3},"freshness":{"state":"ready"}}'; exit 0 ;;
  search) printf '%s' '{"schema_version":1,"index":{"indexed":true,"fresh":true,"chunks":3},"hits":[{"relative_path":"src/reconcile.py","start_line":1,"end_line":3,"content":"def reconcile():\n    if not rows:\n        log.error(\"reconcile failed: batch has no rows\")\n"}]}'; exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "vecgrep"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	setToolPATH(t, binDir)

	result, degraded := culpritForMessage(context.Background(), root, "reconcile failed: batch 42 has no rows")
	if degraded != nil {
		t.Fatalf("degraded = %+v, want none", degraded)
	}
	if result == nil || result.Via != "vecgrep" || result.File != "src/reconcile.py" {
		t.Fatalf("result = %+v", result)
	}
	if result.Line != 3 {
		t.Errorf("line = %d, want 3 (refined to the line actually containing the fragment)", result.Line)
	}
}

func TestCulpritForMessageFallsBackToGitGrepWhenVecgrepNotIndexed(t *testing.T) {
	root := newGitRepo(t, map[string]string{"src/reconcile.py": "def reconcile():\n    if not rows:\n        log.error(\"reconcile failed: batch has no rows\")\n"})
	binDir := t.TempDir()
	script := `#!/bin/sh
printf 'Error: not in a vecgrep project\n' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "vecgrep"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	setToolPATH(t, binDir)

	result, degraded := culpritForMessage(context.Background(), root, "reconcile failed: batch 42 has no rows")
	if result == nil || result.Via != "git_grep" || result.File != "src/reconcile.py" || result.Line != 3 {
		t.Fatalf("result = %+v", result)
	}
	// A successful git_grep fallback still reports vecgrep's own
	// not-ready state as an informational degraded entry (roadmap: "en una
	// rama sin índice, degraded incluye la recuperación").
	if degraded == nil || degraded.Component != "vecgrep" {
		t.Fatalf("degraded = %+v, want an informational vecgrep entry", degraded)
	}
}

func TestCulpritForMessageDegradesWhenFragmentTooGeneric(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	result, degraded := culpritForMessage(context.Background(), root, "42 500 0x1")
	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	if degraded == nil || degraded.Component != "message_search" {
		t.Fatalf("degraded = %+v", degraded)
	}
}

func TestCulpritForMessageDegradesWhenNothingMatches(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	binDir := t.TempDir()
	script := `#!/bin/sh
printf 'Error: not in a vecgrep project\n' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "vecgrep"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	setToolPATH(t, binDir)

	result, degraded := culpritForMessage(context.Background(), root, "a message fragment that matches nothing at all")
	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	if degraded == nil {
		t.Fatal("expected a degraded entry")
	}
}

func TestGitGrepFragmentParsesFirstMatch(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		"a.go": "package a\n// TARGET FRAGMENT\n",
		"b.go": "package b\n// TARGET FRAGMENT\n",
	})
	result, err := gitGrepFragment(context.Background(), root, "TARGET FRAGMENT")
	if err != nil {
		t.Fatal(err)
	}
	if result.Line != 2 || result.Via != "git_grep" {
		t.Fatalf("result = %+v", result)
	}
}

func TestGitGrepFragmentNoMatch(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	if _, err := gitGrepFragment(context.Background(), root, "nothing to see here"); err == nil {
		t.Fatal("expected an error for no match")
	}
}
