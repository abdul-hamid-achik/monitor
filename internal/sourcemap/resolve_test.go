package sourcemap

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFile writes content to dir/name and returns the absolute path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

const simpleMapJSON = `{
	"version": 3,
	"sources": ["src.ts"],
	"sourcesContent": ["let x = 1;\n"],
	"names": [],
	"mappings": "AAAA"
}`

func TestResolveViaSiblingMapFile(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n")
	writeFile(t, dir, "out.js.map", simpleMapJSON)

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantSource := filepath.Join(dir, "src.ts")
	if pos.Source != wantSource {
		t.Errorf("Source = %q, want %q", pos.Source, wantSource)
	}
	if pos.Line != 1 || pos.Col != 1 {
		t.Errorf("got line:col %d:%d, want 1:1", pos.Line, pos.Col)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
	if pos.Stale {
		t.Error("Stale = true, want false (map and generated file are fresh siblings)")
	}
}

func TestResolveViaSourceMappingURLComment(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "app.js", "var x = 1;\n//# sourceMappingURL=app.js.map\n")
	writeFile(t, dir, "app.js.map", simpleMapJSON)

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}

	t.Run("legacy //@ comment", func(t *testing.T) {
		genPath := writeFile(t, dir, "legacy.js", "var x = 1;\n//@ sourceMappingURL=app.js.map\n")
		pos, err := r.Resolve(genPath, 1, 1)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if pos.Mapping != MappingExact {
			t.Errorf("Mapping = %q, want exact", pos.Mapping)
		}
	})
}

func TestResolveInlineDataURL(t *testing.T) {
	dir := t.TempDir()
	encoded := base64.StdEncoding.EncodeToString([]byte(simpleMapJSON))
	genPath := writeFile(t, dir, "inline.js",
		"var x = 1;\n//# sourceMappingURL=data:application/json;base64,"+encoded+"\n")

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantSource := filepath.Join(dir, "src.ts")
	if pos.Source != wantSource {
		t.Errorf("Source = %q, want %q", pos.Source, wantSource)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
	if pos.Stale {
		t.Error("an inline map travels with the file: it can never be stale")
	}
}

func TestResolveNoSourceMapFound(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "lonely.js", "var x = 1;\n")

	r := NewResolver()
	_, err := r.Resolve(genPath, 1, 1)
	if !errors.Is(err, ErrNoSourceMap) {
		t.Fatalf("got error %v, want ErrNoSourceMap", err)
	}
}

func TestResolveStaleMap(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n")
	mapPath := writeFile(t, dir, "out.js.map", simpleMapJSON)

	now := time.Now()
	// The map predates the generated file: a rebuild happened without
	// regenerating the map.
	if err := os.Chtimes(mapPath, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes map: %v", err)
	}
	if err := os.Chtimes(genPath, now, now); err != nil {
		t.Fatalf("chtimes generated: %v", err)
	}

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !pos.Stale {
		t.Error("Stale = false, want true (map is older than the generated file)")
	}
}

func TestResolveFreshMapIsNotStale(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n")
	mapPath := writeFile(t, dir, "out.js.map", simpleMapJSON)

	now := time.Now()
	if err := os.Chtimes(genPath, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes generated: %v", err)
	}
	if err := os.Chtimes(mapPath, now, now); err != nil {
		t.Fatalf("chtimes map: %v", err)
	}

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Stale {
		t.Error("Stale = true, want false (map is newer than the generated file)")
	}
}

func TestResolveAmbiguousFallsBackToPrecedingSegment(t *testing.T) {
	dir := t.TempDir()
	// Two segments on generated line 1: genCol 0 and genCol 4 (0-based).
	mapJSON := `{
		"version": 3,
		"sources": ["src.ts"],
		"names": [],
		"mappings": "AAAA,IAAA"
	}`
	genPath := writeFile(t, dir, "out.js", "0123456789\n")
	writeFile(t, dir, "out.js.map", mapJSON)

	r := NewResolver()

	// col=3 (1-based) -> colIdx=2, strictly between segment 0 (col 0) and
	// segment 1 (col 4): falls back to segment 0 and is marked ambiguous.
	pos, err := r.Resolve(genPath, 1, 3)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingAmbiguous {
		t.Errorf("Mapping = %q, want ambiguous", pos.Mapping)
	}

	// col=5 (1-based) -> colIdx=4: an exact match on segment 1.
	pos, err = r.Resolve(genPath, 1, 5)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
}

func TestResolveColumnLessLookupPicksFirstSegment(t *testing.T) {
	dir := t.TempDir()
	mapJSON := `{
		"version": 3,
		"sources": ["src.ts"],
		"names": [],
		"mappings": "AAAA,IAAA"
	}`
	genPath := writeFile(t, dir, "out.js", "0123456789\n")
	writeFile(t, dir, "out.js.map", mapJSON)

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 0)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact for a column-less lookup", pos.Mapping)
	}
	if pos.Col != 1 {
		t.Errorf("Col = %d, want 1 (the first segment's source column)", pos.Col)
	}
}

func TestResolveColumnBeforeFirstSegmentIsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	// A single segment starting at generated column 2 (0-based): "EAAA"
	// (genCol delta +2, source/line/col deltas 0).
	mapJSON := `{
		"version": 3,
		"sources": ["src.ts"],
		"names": [],
		"mappings": "EAAA"
	}`
	genPath := writeFile(t, dir, "out.js", "0123456789\n")
	writeFile(t, dir, "out.js.map", mapJSON)

	r := NewResolver()
	// col=2 (1-based) -> colIdx=1, before the only segment (colIdx 2).
	pos, err := r.Resolve(genPath, 1, 2)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingAmbiguous {
		t.Errorf("Mapping = %q, want ambiguous", pos.Mapping)
	}

	// col=3 (1-based) -> colIdx=2: an exact match.
	pos, err = r.Resolve(genPath, 1, 3)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
}

func TestResolveLineNotMapped(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "out.js", "line one\nline two\nline three\n")
	// Only one generated line is mapped; line 2 is a gap (";;").
	mapJSON := `{"version":3,"sources":["src.ts"],"names":[],"mappings":"AAAA;;AACA"}`
	writeFile(t, dir, "out.js.map", mapJSON)

	r := NewResolver()

	if _, err := r.Resolve(genPath, 2, 1); !errors.Is(err, ErrLineNotMapped) {
		t.Fatalf("line 2 (empty segments): got error %v, want ErrLineNotMapped", err)
	}
	if _, err := r.Resolve(genPath, 99, 1); !errors.Is(err, ErrLineNotMapped) {
		t.Fatalf("line 99 (past the end): got error %v, want ErrLineNotMapped", err)
	}
	if _, err := r.Resolve(genPath, 1, 1); err != nil {
		t.Fatalf("line 1: unexpected error: %v", err)
	}
}

func TestResolveRejectsNonPositiveLine(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n")
	writeFile(t, dir, "out.js.map", simpleMapJSON)

	r := NewResolver()
	if _, err := r.Resolve(genPath, 0, 1); err == nil {
		t.Fatal("expected an error for line 0")
	}
}

func TestResolveHonorsSourceRoot(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "dist/out.js", "var x = 1;\n")
	mapJSON := `{
		"version": 3,
		"sourceRoot": "../src",
		"sources": ["a.ts"],
		"names": [],
		"mappings": "AAAA"
	}`
	writeFile(t, dir, "dist/out.js.map", mapJSON)

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(dir, "src", "a.ts")
	if pos.Source != want {
		t.Errorf("Source = %q, want %q", pos.Source, want)
	}
}

// TestResolveCacheInvalidatesOnChange proves the (path, mtime, size) cache
// key: after the generated file is rewritten with different content (and
// therefore a different size, regardless of mtime granularity), a fresh
// Resolve call must see the new mapping, not a stale cached one.
func TestResolveCacheInvalidatesOnChange(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n")
	writeFile(t, dir, "out.js.map", simpleMapJSON)

	r := NewResolver()
	first, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if first.Line != 1 {
		t.Fatalf("first.Line = %d, want 1", first.Line)
	}

	// Rewrite both the generated file (different byte length) and its map
	// so the mapped source line moves from 1 to 2.
	writeFile(t, dir, "out.js", "var x = 1; // longer line now\n")
	mapJSON := `{"version":3,"sources":["src.ts"],"names":[],"mappings":"AACA"}` // srcLine delta +1
	writeFile(t, dir, "out.js.map", mapJSON)

	second, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if second.Line != 2 {
		t.Errorf("second.Line = %d, want 2 (cache must invalidate on size change)", second.Line)
	}
}
