package explain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestGitGrepFragmentPrefersSourceOverTestFixture is the review's exact
// repro: a decoy test/golden fixture that reproduces the rendered message
// verbatim, sorting BEFORE the real source file in git grep's own path
// order, must not win over the actual source line.
func TestGitGrepFragmentPrefersSourceOverTestFixture(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		// "tests/" sorts before "workload.py" -- without filtering, git
		// grep's first hit is the decoy.
		"tests/workload.py": "EXPECTED = \"flaky_parse: malformed payload near token 'bad-payl'\"\n",
		"workload.py": "def flaky_parse(payload):\n" +
			"    raise ValueError(f\"flaky_parse: malformed payload near token {payload!r}\")\n",
	})
	result, err := gitGrepFragment(context.Background(), root, "flaky_parse: malformed payload near token")
	if err != nil {
		t.Fatal(err)
	}
	if result.File != "workload.py" || result.Line != 2 {
		t.Fatalf("result = %+v, want workload.py:2 (the real source), not the tests/ fixture", result)
	}
}

// TestGitGrepFragmentRejectsAllNoisyMatches (LUX-2): when every git grep
// hit looks like test/fixture noise, the search must return no result
// (surfaced as a degraded note naming the noise) instead of blaming the
// fixture -- a golden fixture that reproduces the rendered message is not
// evidence about the code that printed it.
func TestGitGrepFragmentRejectsAllNoisyMatches(t *testing.T) {
	root := newGitRepo(t, map[string]string{"tests/only.go": "// TARGET FRAGMENT ONLY HERE\n"})
	result, err := gitGrepFragment(context.Background(), root, "TARGET FRAGMENT ONLY HERE")
	if result != nil {
		t.Fatalf("result = %+v, want no result for an all-noisy match set", result)
	}
	if err == nil || !strings.Contains(err.Error(), "noise") {
		t.Fatalf("err = %v, want the noise-naming error", err)
	}
}

// TestCulpritForMessageDegradesWhenOnlyNoiseMatches (LUX-2): a healthy
// vecgrep whose only hit is a fixture, followed by a git grep that finds
// the same fixture, must degrade with a note that says so -- never a
// culprit pointing at the fixture.
func TestCulpritForMessageDegradesWhenOnlyNoiseMatches(t *testing.T) {
	root := newGitRepo(t, map[string]string{"tests/only.go": "// TARGET FRAGMENT ONLY HERE\n"})
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  status) printf '%s' '{"stats":{"chunks":1},"freshness":{"state":"ready"}}'; exit 0 ;;
  search) printf '%s' '{"schema_version":1,"index":{"indexed":true,"fresh":true,"chunks":1},"hits":[{"relative_path":"tests/only.go","start_line":1,"end_line":1,"content":"// TARGET FRAGMENT ONLY HERE"}]}'; exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "vecgrep"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	setToolPATH(t, binDir)

	result, degraded := culpritForMessage(context.Background(), root, "TARGET FRAGMENT ONLY HERE")
	if result != nil {
		t.Fatalf("result = %+v, want nil (the only match is fixture noise)", result)
	}
	if degraded == nil || !strings.Contains(degraded.Detail, "noise") {
		t.Fatalf("degraded = %+v, want a note naming the fixture-only match set", degraded)
	}
}

func TestIsLikelySourceNoise(t *testing.T) {
	noisy := []string{
		"internal/explain/build_test.go", "internal/stacktrace/golden_test.go",
		"testdata/fixture.json", "specs/issues.yml", "spec/foo_spec.rb",
		"__tests__/foo.test.ts", "src/foo.spec.js", "python/test_workload.py",
		"docs/README.md", "notes.txt",
	}
	for _, p := range noisy {
		if !isLikelySourceNoise(p) {
			t.Errorf("isLikelySourceNoise(%q) = false, want true", p)
		}
	}
	clean := []string{
		"examples/polyglot/js/workload.js", "src/reconcile.py", "internal/explain/build.go",
	}
	for _, p := range clean {
		if isLikelySourceNoise(p) {
			t.Errorf("isLikelySourceNoise(%q) = true, want false", p)
		}
	}
}

func TestLongestLiteralFragmentStripsQuotedSegments(t *testing.T) {
	got := longestLiteralFragment("flaky_parse: malformed payload near token 'bad-payl'")
	want := "flaky_parse: malformed payload near token"
	if got != want {
		t.Errorf("longestLiteralFragment = %q, want %q", got, want)
	}
}
