package sourcemap

import (
	"errors"
	"testing"
)

func TestDecodeMappingsSingleSegment(t *testing.T) {
	// "AAAA": one segment on one line, all deltas zero (generated col 0 ->
	// source 0, line 0, col 0).
	lines, err := decodeMappings("AAAA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 1 || len(lines[0]) != 1 {
		t.Fatalf("got %+v, want exactly one line with one segment", lines)
	}
	seg := lines[0][0]
	if !seg.HasSource || seg.HasName {
		t.Fatalf("got %+v, want HasSource=true HasName=false", seg)
	}
	if seg.GeneratedColumn != 0 || seg.SourceIndex != 0 || seg.SourceLine != 0 || seg.SourceColumn != 0 {
		t.Errorf("got %+v, want all-zero position", seg)
	}
}

func TestDecodeMappingsCommaSeparatedSegments(t *testing.T) {
	// Two segments on one line: "AAAA" then "CAAC" (deltas +1,+0,+0,+1).
	lines, err := decodeMappings("AAAA,CAAC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 1 || len(lines[0]) != 2 {
		t.Fatalf("got %+v, want one line with two segments", lines)
	}
	first, second := lines[0][0], lines[0][1]
	if first.GeneratedColumn != 0 {
		t.Errorf("first.GeneratedColumn = %d, want 0", first.GeneratedColumn)
	}
	if second.GeneratedColumn != 1 || second.SourceIndex != 0 || second.SourceLine != 0 || second.SourceColumn != 1 {
		t.Errorf("second segment = %+v, want genCol=1 src=(0,0,1)", second)
	}
}

func TestDecodeMappingsGeneratedColumnResetsPerLine(t *testing.T) {
	// "AAAA;CAAC": line 0 has one segment at genCol 0; line 1's segment
	// delta (+1) is relative to genCol *reset to 0*, landing at genCol 1 -
	// not accumulated across the semicolon.
	lines, err := decodeMappings("AAAA;CAAC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if len(lines[0]) != 1 || lines[0][0].GeneratedColumn != 0 {
		t.Fatalf("line 0 = %+v, want one segment at genCol 0", lines[0])
	}
	if len(lines[1]) != 1 {
		t.Fatalf("line 1 = %+v, want one segment", lines[1])
	}
	seg := lines[1][0]
	// srcCol accumulates across the whole document (it was 0, +1 = 1),
	// unlike genCol which reset for this line.
	if seg.GeneratedColumn != 1 || seg.SourceColumn != 1 {
		t.Errorf("line 1 segment = %+v, want genCol=1 srcCol=1", seg)
	}
}

func TestDecodeMappingsEmptyLine(t *testing.T) {
	// A line with no segments at all: two semicolons back to back.
	lines, err := decodeMappings("AAAA;;CAAC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if len(lines[1]) != 0 {
		t.Errorf("line 1 = %+v, want no segments", lines[1])
	}
}

func TestDecodeMappingsFiveFieldSegmentHasName(t *testing.T) {
	lines, err := decodeMappings("AAAAC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	seg := lines[0][0]
	if !seg.HasName || seg.NameIndex != 1 {
		t.Errorf("got %+v, want HasName=true NameIndex=1", seg)
	}
}

func TestDecodeMappingsRejectsBadFieldCount(t *testing.T) {
	// "AA" decodes to two fields (0, 0), which is neither 1, 4 nor 5.
	if _, err := decodeMappings("AA"); err == nil {
		t.Fatal("expected an error for a 2-field segment")
	}
}

func TestDecodeMappingsEmptyString(t *testing.T) {
	lines, err := decodeMappings("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lines != nil {
		t.Errorf("got %+v, want nil", lines)
	}
}

func TestDecodeRejectsIndexMap(t *testing.T) {
	data := []byte(`{"version":3,"sections":[{"offset":{"line":0,"column":0},"map":{"version":3,"mappings":""}}]}`)
	_, err := Decode(data)
	if !errors.Is(err, ErrIndexMap) {
		t.Fatalf("got error %v, want ErrIndexMap", err)
	}
}

func TestDecodeRejectsUnsupportedVersion(t *testing.T) {
	data := []byte(`{"version":2,"mappings":""}`)
	_, err := Decode(data)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("got error %v, want ErrUnsupportedVersion", err)
	}
}

func TestDecodeFullDocument(t *testing.T) {
	data := []byte(`{
		"version": 3,
		"file": "out.js",
		"sourceRoot": "",
		"sources": ["foo.js", "bar.js"],
		"sourcesContent": ["content of foo", null],
		"names": ["a", "b"],
		"mappings": "AAAA,CAAC;AACA"
	}`)
	m, err := Decode(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Version != 3 || m.File != "out.js" {
		t.Errorf("got version=%d file=%q", m.Version, m.File)
	}
	if len(m.Sources) != 2 || m.Sources[0] != "foo.js" || m.Sources[1] != "bar.js" {
		t.Errorf("got sources %v", m.Sources)
	}
	if m.SourcesContent[0] != "content of foo" || m.SourcesContent[1] != "" {
		t.Errorf("got sourcesContent %#v, want [\"content of foo\", \"\"] (null -> empty string)", m.SourcesContent)
	}
	if len(m.Lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(m.Lines))
	}
}

func TestDecodeInvalidJSON(t *testing.T) {
	if _, err := Decode([]byte("not json")); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

// TestDecodeStripsXSSIPrefix proves a map that leads with the spec-allowed
// ")]}'" XSSI-protection line still decodes, instead of failing on it as
// invalid JSON.
func TestDecodeStripsXSSIPrefix(t *testing.T) {
	data := []byte(")]}'\n" + `{"version":3,"mappings":""}`)
	m, err := Decode(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Version != 3 {
		t.Errorf("got version %d, want 3", m.Version)
	}
}

// TestDecodeStripsUTF8BOM proves a map saved with a leading UTF-8
// byte-order mark still decodes.
func TestDecodeStripsUTF8BOM(t *testing.T) {
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"version":3,"mappings":""}`)...)
	m, err := Decode(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Version != 3 {
		t.Errorf("got version %d, want 3", m.Version)
	}
}

// TestDecodeStripsBOMThenXSSIPrefix proves the two preambles compose in the
// order a real tool would emit them: BOM first (the file's own encoding
// marker), then the XSSI-protection line.
func TestDecodeStripsBOMThenXSSIPrefix(t *testing.T) {
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(")]}'\n"+`{"version":3,"mappings":""}`)...)
	if _, err := Decode(data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestDecodeSectionsNullIsNotAnIndexMap proves an explicit "sections":
// null - which encoding/json hands back as the raw bytes "null", not as an
// empty/absent field - is not mistaken for a real index map.
func TestDecodeSectionsNullIsNotAnIndexMap(t *testing.T) {
	data := []byte(`{"version":3,"sections":null,"mappings":"AAAA"}`)
	m, err := Decode(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Lines) != 1 {
		t.Errorf("got %d lines, want 1", len(m.Lines))
	}
}

// TestDecodeMappingsRejectsNegativeSourcePosition proves a corrupted
// "mappings" string that drives a cumulative field negative is a decode
// error, not a silently wrong position. "AADA" is a=0 (genCol +0), A=0
// (srcIdx +0), D=-1 (srcLine -1), A=0 (srcCol +0): the srcLine total goes
// negative on the very first segment.
func TestDecodeMappingsRejectsNegativeSourcePosition(t *testing.T) {
	if _, err := decodeMappings("AADA"); err == nil {
		t.Fatal("expected an error for a segment with a negative source line")
	}
}

// TestDecodeMappingsRejectsNegativeGeneratedColumn covers the same
// validation for the generated column itself: "D" alone is a 1-field
// segment with delta -1, which cannot be a valid first generated column on
// a line (columns start at 0 and only ever accumulate non-negative
// deltas in a well-formed document).
func TestDecodeMappingsRejectsNegativeGeneratedColumn(t *testing.T) {
	if _, err := decodeMappings("D"); err == nil {
		t.Fatal("expected an error for a negative generated column")
	}
}

// TestDecodeMappingsRejectsNegativeNameIndex covers the 5-field case: the
// name index delta alone goes negative.
func TestDecodeMappingsRejectsNegativeNameIndex(t *testing.T) {
	if _, err := decodeMappings("AAAAD"); err == nil {
		t.Fatal("expected an error for a negative name index")
	}
}

// TestDecodeMappingsSortsOutOfOrderSegments proves decodeMappings restores
// ascending GeneratedColumn order within a line even when the document
// doesn't emit segments that way (the spec requires it, but nothing
// enforces it, and Resolve's column lookup assumes it). "UAAA" is a
// generated-column delta of +10; "RAAE" is a delta of -8, landing at
// generated column 2 - after "UAAA" already put the running column at 10.
func TestDecodeMappingsSortsOutOfOrderSegments(t *testing.T) {
	lines, err := decodeMappings("UAAA,RAAE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 1 || len(lines[0]) != 2 {
		t.Fatalf("got %+v, want one line with two segments", lines)
	}
	if lines[0][0].GeneratedColumn != 2 || lines[0][1].GeneratedColumn != 10 {
		t.Errorf("got columns [%d, %d], want [2, 10] (sorted ascending)",
			lines[0][0].GeneratedColumn, lines[0][1].GeneratedColumn)
	}
}
