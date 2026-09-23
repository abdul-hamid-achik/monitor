package sourcemap

import (
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// TestResolveFreshBuildWriteOrderIsNotStale reproduces how every real build
// tool actually writes its output: the .map file is written first, then the
// generated file a moment later, leaving the map's mtime a hair *older*.
// Flagging that as Stale would mark every freshly built pair stale.
func TestResolveFreshBuildWriteOrderIsNotStale(t *testing.T) {
	dir := t.TempDir()
	mapPath := writeFile(t, dir, "out.js.map", simpleMapJSON)
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n")

	now := time.Now()
	if err := os.Chtimes(mapPath, now, now); err != nil {
		t.Fatalf("chtimes map: %v", err)
	}
	if err := os.Chtimes(genPath, now.Add(50*time.Millisecond), now.Add(50*time.Millisecond)); err != nil {
		t.Fatalf("chtimes generated: %v", err)
	}

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Stale {
		t.Error("Stale = true, want false: a few milliseconds of write-order skew is not a rebuilt-without-the-map generated file")
	}
}

// TestResolveCachePicksUpMapOnlyChange proves the redesigned cache key: a
// map regenerated in place, with the generated file left untouched, must
// still invalidate the cached entry - the old (path, mtime, size) key on
// only the generated file could never see this.
func TestResolveCachePicksUpMapOnlyChange(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n")
	mapPath := writeFile(t, dir, "out.js.map", simpleMapJSON)

	now := time.Now()
	if err := os.Chtimes(mapPath, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes map: %v", err)
	}
	if err := os.Chtimes(genPath, now, now); err != nil {
		t.Fatalf("chtimes generated: %v", err)
	}

	r := NewResolver()
	first, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if first.Line != 1 || !first.Stale {
		t.Fatalf("first = %+v, want Line=1 Stale=true", first)
	}

	// Regenerate only the map: different content and a fresh mtime. The
	// generated file (out.js) is not touched at all.
	newMapJSON := `{"version":3,"sources":["src.ts"],"names":[],"mappings":"AACA"}` // srcLine delta +1
	if err := os.WriteFile(mapPath, []byte(newMapJSON), 0o644); err != nil {
		t.Fatalf("rewrite map: %v", err)
	}
	if err := os.Chtimes(mapPath, now, now); err != nil {
		t.Fatalf("chtimes new map: %v", err)
	}

	second, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if second.Line != 2 {
		t.Errorf("second.Line = %d, want 2 (a map-only change must invalidate the cache)", second.Line)
	}
	if second.Stale {
		t.Error("second.Stale = true, want false (the regenerated map is now fresh)")
	}
}

// TestResolveCacheStaysBoundedAcrossRebuilds proves the cache keeps exactly
// one entry per generated path no matter how many times that path is
// rebuilt, rather than growing a new entry per (path, mtime, size) tuple
// forever - the failure mode a long-running watch process would hit.
func TestResolveCacheStaysBoundedAcrossRebuilds(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n")
	writeFile(t, dir, "out.js.map", simpleMapJSON)

	r := NewResolver()
	base := time.Now()
	for i := 0; i < 100; i++ {
		mtime := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(genPath, mtime, mtime); err != nil {
			t.Fatalf("chtimes iteration %d: %v", i, err)
		}
		if _, err := r.Resolve(genPath, 1, 1); err != nil {
			t.Fatalf("Resolve iteration %d: %v", i, err)
		}
	}
	if len(r.cache) != 1 {
		t.Errorf("len(cache) = %d, want 1 (one entry per generated path, not one per rebuild)", len(r.cache))
	}
}

// TestPickSegmentColumnLessSkipsSegmentWithNoSource proves a column-less
// lookup (col <= 0) finds the first *mapped* segment on the line, rather
// than unconditionally returning the line's first segment even when that
// segment is a deliberate 1-field "unmapped" marker.
func TestPickSegmentColumnLessSkipsSegmentWithNoSource(t *testing.T) {
	dir := t.TempDir()
	// "A" is a 1-field (unmapped) segment at genCol 0; "EAAE" is a mapped
	// segment at genCol 2 (source line 0, source col 2).
	mapJSON := `{"version":3,"sources":["src.ts"],"names":[],"mappings":"A,EAAE"}`
	genPath := writeFile(t, dir, "u.js", "0123456789\n")
	writeFile(t, dir, "u.js.map", mapJSON)

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 0)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
	if pos.Col != 3 {
		t.Errorf("Col = %d, want 3 (the mapped segment's source column, not the unmapped leading one)", pos.Col)
	}
}

// TestResolveOutOfOrderSegmentsAreSorted proves decodeMappings' segments are
// sorted before Resolve's column lookup runs against them, so a document
// whose segments aren't already in ascending GeneratedColumn order (which
// the spec requires, but nothing enforces) still resolves the right one.
func TestResolveOutOfOrderSegmentsAreSorted(t *testing.T) {
	dir := t.TempDir()
	// Two segments in on-disk order (genCol 10, then genCol 2): "UAAA"
	// (genCol delta +10) then "RAAE" (genCol delta -8, srcCol delta +2).
	mapJSON := `{"version":3,"sources":["src.ts"],"names":[],"mappings":"UAAA,RAAE"}`
	genPath := writeFile(t, dir, "min.js", "0123456789012\n")
	writeFile(t, dir, "min.js.map", mapJSON)

	r := NewResolver()
	// col=3 (1-based) -> colIdx=2: exact on the genCol=2 segment, which
	// only sorting puts in the right place for pickSegment's scan.
	pos, err := r.Resolve(genPath, 1, 3)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
	if pos.Col != 3 {
		t.Errorf("Col = %d, want 3 (the genCol=2 segment's source column)", pos.Col)
	}
}

// TestResolveDataURLPercentEncodedPreservesPlusSign proves the
// percent-encoded (non-base64) inline map branch uses url.PathUnescape, not
// url.QueryUnescape: '+' is a valid, common Base64 VLQ digit that must
// survive unescaping unchanged, not become a space.
func TestResolveDataURLPercentEncodedPreservesPlusSign(t *testing.T) {
	dir := t.TempDir()
	mapJSON := `{"version":3,"sources":["src.ts"],"names":[],"mappings":"AAAA,+BAAC"}`
	encoded := url.PathEscape(mapJSON)
	genPath := writeFile(t, dir, "inline.js",
		"var x = 1;\n//# sourceMappingURL=data:application/json,"+encoded+"\n")

	r := NewResolver()
	if _, err := r.Resolve(genPath, 1, 1); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

// TestResolveDataURLUnpaddedBase64 proves the base64 inline map branch
// accepts base64 with its trailing '=' padding stripped, which is how some
// real encoders (and the finding that reported this) emit it.
func TestResolveDataURLUnpaddedBase64(t *testing.T) {
	dir := t.TempDir()
	encoded := base64.StdEncoding.EncodeToString([]byte(simpleMapJSON))
	encoded = strings.TrimRight(encoded, "=")
	genPath := writeFile(t, dir, "inline.js",
		"var x = 1;\n//# sourceMappingURL=data:application/json;base64,"+encoded+"\n")

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
}

// TestResolveSourceMappingURLFileScheme proves a "file://" comment target
// resolves to the local path it names.
func TestResolveSourceMappingURLFileScheme(t *testing.T) {
	dir := t.TempDir()
	mapPath := writeFile(t, dir, "out.js.map", simpleMapJSON)
	genPath := writeFile(t, dir, "out.js", "var x = 1;\n//# sourceMappingURL=file://"+mapPath+"\n")

	r := NewResolver()
	if _, err := r.Resolve(genPath, 1, 1); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

// TestResolveSourceMappingURLQueryStringIsStripped proves a webpack-style
// "<file>.map?<contenthash>" comment target still resolves: the query
// string is not part of the filesystem path.
func TestResolveSourceMappingURLQueryStringIsStripped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "q.js.map", simpleMapJSON)
	genPath := writeFile(t, dir, "q.js", "var x = 1;\n//# sourceMappingURL=q.js.map?v=abc123\n")

	r := NewResolver()
	if _, err := r.Resolve(genPath, 1, 1); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

// TestResolveSourceMappingURLRemoteFallsBackToSibling proves an http(s) (or
// any other non-file-scheme) comment target is not itself an error: it
// simply isn't loadable from disk, so discovery falls through to the
// sibling "<file>.map", per the documented order.
func TestResolveSourceMappingURLRemoteFallsBackToSibling(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "remote.js",
		"var x = 1;\n//# sourceMappingURL=https://cdn.example.invalid/remote.js.map\n")
	writeFile(t, dir, "remote.js.map", simpleMapJSON)

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pos.Mapping != MappingExact {
		t.Errorf("Mapping = %q, want exact", pos.Mapping)
	}
}

// TestResolveCommentLookalikeInStringLiteralDoesNotHijackDiscovery proves
// the anchored sourceMappingURL regex isn't fooled by the phrase appearing
// inside a string literal on a later line: since that line isn't itself a
// "//# sourceMappingURL=..." comment, discovery correctly finds nothing on
// the last line and falls through to the sibling ".map" - which must be
// the real map, never the bogus one the lookalike text names.
func TestResolveCommentLookalikeInStringLiteralDoesNotHijackDiscovery(t *testing.T) {
	dir := t.TempDir()
	genPath := writeFile(t, dir, "real.js",
		"//# sourceMappingURL=real.js.map\n"+
			"const help = \"append //# sourceMappingURL=bogus.map to your file\";\n")
	realMapJSON := `{"version":3,"sources":["src.ts"],"names":[],"mappings":"AAAA"}`
	bogusMapJSON := `{"version":3,"sources":["bogus.ts"],"names":[],"mappings":"AAAA"}`
	writeFile(t, dir, "real.js.map", realMapJSON)
	writeFile(t, dir, "bogus.map", bogusMapJSON)

	r := NewResolver()
	pos, err := r.Resolve(genPath, 1, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantSource := filepath.Join(dir, "src.ts")
	if pos.Source != wantSource {
		t.Errorf("Source = %q, want %q (the real map, via the sibling fallback - never bogus.map)", pos.Source, wantSource)
	}
}

// TestResolveSourcePathFileAndWebpackSchemes exercises resolveSourcePath
// directly against the two shapes the review found it mishandled: a
// "file://" source (which names a real local path) and a "webpack://"
// source (which doesn't and must be returned unchanged rather than joined
// onto mapDir into a bogus path).
func TestResolveSourcePathFileAndWebpackSchemes(t *testing.T) {
	got := resolveSourcePath("/map/dir", "", "file:///abs/src/a.ts")
	if want := filepath.FromSlash("/abs/src/a.ts"); got != want {
		t.Errorf("file:// source = %q, want %q", got, want)
	}

	got = resolveSourcePath("/map/dir", "", "webpack:///./src/a.ts")
	if want := "webpack:///./src/a.ts"; got != want {
		t.Errorf("webpack:// source = %q, want it returned unchanged, got %q", got, want)
	}
}
