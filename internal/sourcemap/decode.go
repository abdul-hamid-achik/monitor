package sourcemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrIndexMap is returned when a Source Map v3 "index map" (a document with
// a top-level "sections" array instead of "mappings") is decoded. Index maps
// stitch several sub-maps together at generated offsets; monitor does not
// need them yet, so Decode refuses to guess rather than silently mapping
// wrong. Callers see a clear, typed reason instead of corrupt positions.
var ErrIndexMap = errors.New("sourcemap: index maps (\"sections\") are not supported")

// ErrUnsupportedVersion is returned for any "version" other than 3.
var ErrUnsupportedVersion = errors.New("sourcemap: unsupported version")

// Segment is one decoded entry from the "mappings" field: a generated
// column on some generated line, plus (usually) the original source
// position it came from. Line and column are 0-based, matching the spec;
// see Resolver for the 1-based public API.
type Segment struct {
	GeneratedColumn int

	HasSource    bool
	SourceIndex  int
	SourceLine   int
	SourceColumn int

	HasName   bool
	NameIndex int
}

// Map is a decoded Source Map v3 document.
type Map struct {
	Version    int
	File       string
	SourceRoot string
	Sources    []string
	// SourcesContent mirrors Sources; an entry is "" when the map did not
	// embed that source's content (sourcesContent is optional and may
	// contain explicit nulls for individual sources).
	SourcesContent []string
	Names          []string

	// Lines holds the decoded segments for each generated line (0-based
	// index), sorted by GeneratedColumn as the spec requires them to be
	// emitted. A line with no segments is a nil/empty slice, which callers
	// read as "nothing mapped on this line".
	Lines [][]Segment
}

// rawMap mirrors the on-disk JSON shape closely enough for encoding/json;
// Decode turns it into the friendlier Map/Segment shapes above.
type rawMap struct {
	Version        int             `json:"version"`
	File           string          `json:"file"`
	SourceRoot     string          `json:"sourceRoot"`
	Sources        []string        `json:"sources"`
	SourcesContent []*string       `json:"sourcesContent"`
	Names          []string        `json:"names"`
	Mappings       string          `json:"mappings"`
	Sections       json.RawMessage `json:"sections"`
}

// Decode parses a Source Map v3 JSON document.
func Decode(data []byte) (*Map, error) {
	var raw rawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("sourcemap: decode: %w", err)
	}
	if len(raw.Sections) > 0 {
		return nil, ErrIndexMap
	}
	if raw.Version != 3 {
		return nil, fmt.Errorf("%w: got %d", ErrUnsupportedVersion, raw.Version)
	}

	m := &Map{
		Version:    raw.Version,
		File:       raw.File,
		SourceRoot: raw.SourceRoot,
		Sources:    raw.Sources,
		Names:      raw.Names,
	}
	m.SourcesContent = make([]string, len(raw.Sources))
	for i, c := range raw.SourcesContent {
		if i >= len(m.SourcesContent) {
			break
		}
		if c != nil {
			m.SourcesContent[i] = *c
		}
	}

	lines, err := decodeMappings(raw.Mappings)
	if err != nil {
		return nil, err
	}
	m.Lines = lines
	return m, nil
}

// decodeMappings decodes the semicolon/comma-delimited "mappings" field into
// one segment slice per generated line.
//
// Field deltas: per the spec, every field in a segment is relative to the
// previous value of the *same field*, except the generated column, which is
// relative to the previous segment's generated column *on the same line*
// (and therefore resets to 0 at every ";"). The other running totals
// (source index/line/column, name index) accumulate across the whole
// document.
func decodeMappings(mappings string) ([][]Segment, error) {
	if mappings == "" {
		return nil, nil
	}
	lineStrs := strings.Split(mappings, ";")
	lines := make([][]Segment, len(lineStrs))

	var srcIdx, srcLine, srcCol, nameIdx int
	for i, lineStr := range lineStrs {
		genCol := 0
		if lineStr == "" {
			continue
		}
		var segs []Segment
		for _, segStr := range strings.Split(lineStr, ",") {
			if segStr == "" {
				continue
			}
			values, err := decodeVLQSegment(segStr)
			if err != nil {
				return nil, fmt.Errorf("sourcemap: mappings line %d: %w", i, err)
			}
			seg := Segment{SourceIndex: -1, NameIndex: -1}
			switch len(values) {
			case 1:
				genCol += values[0]
			case 4:
				genCol += values[0]
				srcIdx += values[1]
				srcLine += values[2]
				srcCol += values[3]
				seg.HasSource = true
			case 5:
				genCol += values[0]
				srcIdx += values[1]
				srcLine += values[2]
				srcCol += values[3]
				nameIdx += values[4]
				seg.HasSource = true
				seg.HasName = true
			default:
				return nil, fmt.Errorf("sourcemap: mappings line %d: segment has %d fields, want 1, 4 or 5", i, len(values))
			}
			seg.GeneratedColumn = genCol
			if seg.HasSource {
				seg.SourceIndex = srcIdx
				seg.SourceLine = srcLine
				seg.SourceColumn = srcCol
			}
			if seg.HasName {
				seg.NameIndex = nameIdx
			}
			segs = append(segs, seg)
		}
		lines[i] = segs
	}
	return lines, nil
}
