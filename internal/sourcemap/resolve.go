package sourcemap

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ErrNoSourceMap is returned when no source map could be found for a
// generated file: no "//# sourceMappingURL=" / "//@ sourceMappingURL="
// comment in its last 8 KB, and no sibling "<file>.map".
var ErrNoSourceMap = errors.New("sourcemap: no source map found")

// ErrLineNotMapped is returned when a source map was found and decoded, but
// the requested generated line has no segment at all (an unmapped line,
// such as a banner comment or blank line the bundler inserted).
var ErrLineNotMapped = errors.New("sourcemap: generated line has no mapping")

// Mapping describes how confidently a Position was located.
type Mapping string

const (
	// MappingExact means the requested generated column matched a segment
	// exactly, or no column was given (col <= 0), which deterministically
	// selects the line's first segment.
	MappingExact Mapping = "exact"
	// MappingAmbiguous means no segment sat at the requested column; the
	// nearest preceding segment on the same generated line was used
	// instead.
	MappingAmbiguous Mapping = "ambiguous"
	// MappingTranspiled is not produced by Resolver itself: it exists so
	// callers that assemble a stacktrace.Frame from several sources (a
	// real decoded mapping here, or a heuristic guess when no map exists
	// at all) can share one Mapping vocabulary. Resolver only ever returns
	// MappingExact or MappingAmbiguous, because both of those describe a
	// position it actually decoded; "transpiled" describes a *guess* made
	// in the absence of a map, which belongs to that later caller, not to
	// this package.
	MappingTranspiled Mapping = "transpiled"
)

// Position is an original source location resolved from a generated one.
type Position struct {
	// Source is the absolute path to the original source file, resolved
	// against the source map's own location and its sourceRoot.
	Source string
	Line   int // 1-based
	Col    int // 1-based
	Name   string

	Mapping Mapping
	// Stale is true when the source map file is older than the generated
	// file it maps, which usually means the generated file was rebuilt
	// without regenerating the map.
	Stale bool
}

// sourceMappingURLRe finds a "//# sourceMappingURL=..." or legacy
// "//@ sourceMappingURL=..." comment, as emitted by tsc, bun build, webpack
// and esbuild.
var sourceMappingURLRe = regexp.MustCompile(`//[@#]\s*sourceMappingURL=(\S+)`)

// mapEntry is one Resolver cache slot: a decoded map plus everything needed
// to answer Resolve without touching disk again.
type mapEntry struct {
	doc *Map
	// dir is the directory the map's relative "sources" are resolved
	// against: the .map file's own directory, or (for an inline/data: URL
	// map) the generated file's directory.
	dir   string
	stale bool
}

type cacheKey struct {
	path  string
	mtime int64
	size  int64
}

// Resolver decodes and caches Source Map v3 documents for generated files,
// keyed by (path, mtime, size) so an edited-and-rebuilt file is re-resolved
// instead of served stale data from cache.
type Resolver struct {
	mu    sync.Mutex
	cache map[cacheKey]*mapEntry
}

// NewResolver returns an empty Resolver ready to use.
func NewResolver() *Resolver {
	return &Resolver{cache: make(map[cacheKey]*mapEntry)}
}

// Resolve maps a 1-based (line, col) in generatedPath back to its original
// source position. col <= 0 means "no column known"; it deterministically
// resolves to the first segment on that generated line.
//
// Discovery order for the map itself: a sourceMappingURL comment in the last
// 8 KB of generatedPath (inline data: URL or a path, absolute or relative to
// generatedPath's directory), then a sibling "<generatedPath>.map".
func (r *Resolver) Resolve(generatedPath string, line, col int) (Position, error) {
	if line < 1 {
		return Position{}, fmt.Errorf("sourcemap: line must be >= 1, got %d", line)
	}
	absGenerated, err := filepath.Abs(generatedPath)
	if err != nil {
		return Position{}, fmt.Errorf("sourcemap: %s: %w", generatedPath, err)
	}

	entry, err := r.load(absGenerated)
	if err != nil {
		return Position{}, err
	}

	lineIdx := line - 1
	if lineIdx >= len(entry.doc.Lines) || len(entry.doc.Lines[lineIdx]) == 0 {
		return Position{}, fmt.Errorf("%w: %s:%d", ErrLineNotMapped, generatedPath, line)
	}
	segs := entry.doc.Lines[lineIdx]

	seg, mapping := pickSegment(segs, col)
	if !seg.HasSource {
		return Position{}, fmt.Errorf("%w: %s:%d:%d", ErrLineNotMapped, generatedPath, line, col)
	}
	if seg.SourceIndex < 0 || seg.SourceIndex >= len(entry.doc.Sources) {
		return Position{}, fmt.Errorf("sourcemap: %s:%d:%d: source index %d out of range", generatedPath, line, col, seg.SourceIndex)
	}

	pos := Position{
		Source:  resolveSourcePath(entry.dir, entry.doc.SourceRoot, entry.doc.Sources[seg.SourceIndex]),
		Line:    seg.SourceLine + 1,
		Col:     seg.SourceColumn + 1,
		Mapping: mapping,
		Stale:   entry.stale,
	}
	if seg.HasName && seg.NameIndex >= 0 && seg.NameIndex < len(entry.doc.Names) {
		pos.Name = entry.doc.Names[seg.NameIndex]
	}
	return pos, nil
}

// pickSegment chooses which decoded segment answers a lookup at the given
// 1-based column within a line's segments (sorted by GeneratedColumn).
// col <= 0 always returns the first segment, marked exact: it is a
// deliberate "no column known" query, not a fallback. Otherwise it looks for
// an exact column match; failing that, it falls back to the nearest
// preceding segment on the line and marks the result ambiguous.
func pickSegment(segs []Segment, col int) (Segment, Mapping) {
	if col <= 0 {
		return segs[0], MappingExact
	}
	colIdx := col - 1
	best := -1
	for i, s := range segs {
		if s.GeneratedColumn > colIdx {
			break
		}
		best = i
	}
	if best < 0 {
		return segs[0], MappingAmbiguous
	}
	if segs[best].GeneratedColumn == colIdx {
		return segs[best], MappingExact
	}
	return segs[best], MappingAmbiguous
}

func (r *Resolver) load(absGenerated string) (*mapEntry, error) {
	info, err := os.Stat(absGenerated)
	if err != nil {
		return nil, fmt.Errorf("sourcemap: stat %s: %w", absGenerated, err)
	}
	key := cacheKey{path: absGenerated, mtime: info.ModTime().UnixNano(), size: info.Size()}

	r.mu.Lock()
	entry, ok := r.cache[key]
	r.mu.Unlock()
	if ok {
		return entry, nil
	}

	entry, err = buildEntry(absGenerated, info)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[key] = entry
	r.mu.Unlock()
	return entry, nil
}

func buildEntry(absGenerated string, genInfo os.FileInfo) (*mapEntry, error) {
	data, err := os.ReadFile(absGenerated)
	if err != nil {
		return nil, fmt.Errorf("sourcemap: read %s: %w", absGenerated, err)
	}
	genDir := filepath.Dir(absGenerated)

	if rawURL := findSourceMappingURL(data); rawURL != "" {
		if strings.HasPrefix(rawURL, "data:") {
			payload, derr := decodeDataURL(rawURL)
			if derr != nil {
				return nil, fmt.Errorf("sourcemap: %s: inline source map: %w", absGenerated, derr)
			}
			doc, derr := Decode(payload)
			if derr != nil {
				return nil, fmt.Errorf("sourcemap: %s: %w", absGenerated, derr)
			}
			// An inline map travels with the generated file itself, so it
			// can never independently go stale.
			return &mapEntry{doc: doc, dir: genDir, stale: false}, nil
		}
		mapPath := rawURL
		if !filepath.IsAbs(mapPath) {
			mapPath = filepath.Join(genDir, filepath.FromSlash(mapPath))
		}
		return loadExternalMap(mapPath, genInfo)
	}

	siblingPath := absGenerated + ".map"
	if _, statErr := os.Stat(siblingPath); statErr == nil {
		return loadExternalMap(siblingPath, genInfo)
	}

	return nil, fmt.Errorf("%w: %s", ErrNoSourceMap, absGenerated)
}

func loadExternalMap(mapPath string, genInfo os.FileInfo) (*mapEntry, error) {
	mapInfo, err := os.Stat(mapPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNoSourceMap, mapPath, err)
	}
	data, err := os.ReadFile(mapPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNoSourceMap, mapPath, err)
	}
	doc, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("sourcemap: %s: %w", mapPath, err)
	}
	return &mapEntry{
		doc:   doc,
		dir:   filepath.Dir(mapPath),
		stale: mapInfo.ModTime().Before(genInfo.ModTime()),
	}, nil
}

// findSourceMappingURL looks for a sourceMappingURL comment in the last
// 8 KB of the generated file (the whole file, if it is smaller), returning
// the last one found — a file should only ever have one, but if a bundler
// concatenated files that each had one, the last comment wins, matching
// every browser and Node's own resolution.
func findSourceMappingURL(data []byte) string {
	const tailSize = 8192
	tail := data
	if len(tail) > tailSize {
		tail = tail[len(tail)-tailSize:]
	}
	matches := sourceMappingURLRe.FindAllSubmatch(tail, -1)
	if len(matches) == 0 {
		return ""
	}
	return string(matches[len(matches)-1][1])
}

// decodeDataURL decodes a "data:application/json[;charset=...];base64,..."
// (or percent-encoded, non-base64) URL into its raw JSON payload.
func decodeDataURL(raw string) ([]byte, error) {
	const prefix = "data:"
	if !strings.HasPrefix(raw, prefix) {
		return nil, fmt.Errorf("not a data: URL")
	}
	rest := raw[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return nil, fmt.Errorf("malformed data: URL: no comma")
	}
	meta, payload := rest[:comma], rest[comma+1:]
	if strings.Contains(meta, "base64") {
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, fmt.Errorf("base64 decode: %w", err)
		}
		return decoded, nil
	}
	unescaped, err := url.QueryUnescape(payload)
	if err != nil {
		return nil, fmt.Errorf("percent-decode: %w", err)
	}
	return []byte(unescaped), nil
}

// resolveSourcePath turns a map's "sources" entry into an absolute path,
// resolved against the map's own directory and sourceRoot, per the spec:
// the effective path is sourceRoot + "/" + source when sourceRoot is set.
func resolveSourcePath(mapDir, sourceRoot, source string) string {
	p := source
	if sourceRoot != "" {
		p = strings.TrimSuffix(sourceRoot, "/") + "/" + strings.TrimPrefix(source, "/")
	}
	p = filepath.FromSlash(p)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(mapDir, p))
}
