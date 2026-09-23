package sourcemap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFixtureTSCLineMapsExact is the E3.3a golden case: a real TypeScript ->
// JavaScript build (bun build greeter.ts --sourcemap=external) resolves a
// known generated position back to the exact .ts line and column it came
// from. testdata/bun-external is committed with relative paths only; see
// testdata/README.md for how it was produced.
func TestFixtureTSCLineMapsExact(t *testing.T) {
	genPath := "testdata/bun-external/greeter.js"

	// Generated line 9, col 10 is "function main() {" -> the "main"
	// identifier; it maps to greeter.ts line 14, col 10 -> "main" in
	// "function main(): void {".
	r := NewResolver()
	pos, err := r.Resolve(genPath, 9, 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
	if !strings.HasSuffix(pos.Source, filepath.FromSlash("testdata/greeter.ts")) {
		t.Errorf("Source = %q, want it to end in testdata/greeter.ts", pos.Source)
	}
	if pos.Line != 14 || pos.Col != 10 {
		t.Errorf("got %d:%d, want 14:10", pos.Line, pos.Col)
	}
	if pos.Stale {
		t.Error("Stale = true, want false: the committed fixture pair was built together")
	}

	// The resolved source path must actually exist on disk and contain the
	// "main" identifier at the claimed line, so this test would fail if the
	// fixture and the hard-coded expectation ever drifted apart.
	content, err := os.ReadFile(pos.Source)
	if err != nil {
		t.Fatalf("reading resolved source %s: %v", pos.Source, err)
	}
	sourceLines := strings.Split(string(content), "\n")
	if pos.Line-1 >= len(sourceLines) {
		t.Fatalf("resolved line %d is past the end of %s (%d lines)", pos.Line, pos.Source, len(sourceLines))
	}
	got := sourceLines[pos.Line-1]
	if !strings.Contains(got, "function main") {
		t.Errorf("greeter.ts:%d = %q, want it to contain \"function main\"", pos.Line, got)
	}
}

// TestFixtureInlineDataURLMapWorks proves discovery order step 1 (a
// sourceMappingURL comment) with a real inline base64 data: URL, produced by
// `bun build greeter.ts --sourcemap=inline`.
func TestFixtureInlineDataURLMapWorks(t *testing.T) {
	genPath := "testdata/bun-inline/greeter.js"
	data, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if !strings.Contains(string(data), "sourceMappingURL=data:") {
		t.Fatalf("fixture %s does not contain an inline sourceMappingURL comment; did the fixture change?", genPath)
	}

	r := NewResolver()
	pos, err := r.Resolve(genPath, 9, 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
	if pos.Line != 14 || pos.Col != 10 {
		t.Errorf("got %d:%d, want 14:10", pos.Line, pos.Col)
	}
	if !strings.HasSuffix(pos.Source, filepath.FromSlash("testdata/greeter.ts")) {
		t.Errorf("Source = %q, want it to end in testdata/greeter.ts", pos.Source)
	}
}

// TestFixtureStaleMapIsFlagged copies the committed external fixture into a
// scratch directory and back-dates the .map file, proving Stale detection
// against a real bun-produced map rather than a hand-built one.
func TestFixtureStaleMapIsFlagged(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"greeter.js", "greeter.js.map"} {
		data, err := os.ReadFile(filepath.Join("testdata/bun-external", name))
		if err != nil {
			t.Fatalf("reading fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "greeter.js.map"), now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes map: %v", err)
	}
	if err := os.Chtimes(filepath.Join(dir, "greeter.js"), now, now); err != nil {
		t.Fatalf("chtimes generated: %v", err)
	}

	r := NewResolver()
	pos, err := r.Resolve(filepath.Join(dir, "greeter.js"), 9, 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !pos.Stale {
		t.Error("Stale = false, want true: the map is an hour older than the generated file")
	}
}

// TestFixtureMissingMapReturnsErrNoSourceMap proves the honest-degradation
// path: a generated file with neither a sourceMappingURL comment nor a
// sibling .map returns the typed ErrNoSourceMap, never a guess.
func TestFixtureMissingMapReturnsErrNoSourceMap(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/bun-external/greeter.js")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	// Deliberately do not copy greeter.js.map alongside it.
	genPath := filepath.Join(dir, "greeter.js")
	if err := os.WriteFile(genPath, data, 0o644); err != nil {
		t.Fatalf("writing generated file: %v", err)
	}

	r := NewResolver()
	_, err = r.Resolve(genPath, 9, 10)
	if !errors.Is(err, ErrNoSourceMap) {
		t.Fatalf("got error %v, want ErrNoSourceMap", err)
	}
}
