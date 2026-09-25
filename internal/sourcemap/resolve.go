package sourcemap

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrNoSourceMap is returned when no source map could be found for a
// generated file: no "//# sourceMappingURL=" / "//@ sourceMappingURL="
// comment on its last non-empty line (or one whose target can't be loaded
// from disk), and no sibling "<file>.map".
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
	// selects the line's first mapped segment.
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
	// against the source map's own location and its sourceRoot. A source
	// (or sourceRoot) carrying a non-"file" URL scheme, such as
	// "webpack://" or "http://", is not a real filesystem location and is
	// returned unchanged instead.
	Source string
	Line   int // 1-based
	Col    int // 1-based
	Name   string

	Mapping Mapping
	// Stale is true when the source map file is older than the generated
	// file it maps by more than staleTolerance, which usually means the
	// generated file was rebuilt without regenerating the map.
	Stale bool
}

// staleTolerance absorbs the sub-second clock skew every real build tool
// exhibits between writing its .map and its generated file: tsc and bun
// both write the .map first, so on a fresh build the map's mtime is always
// a fraction of a millisecond to a few milliseconds *older* than the
// generated file's, never newer. Flagging that as stale would make every
// freshly built pair look stale. Only a gap wider than this tolerance means
// the generated file was actually rebuilt without a matching regenerate of
// the map.
const staleTolerance = 2 * time.Second

func isStale(mapInfo, genInfo os.FileInfo) bool {
	return genInfo.ModTime().Sub(mapInfo.ModTime()) > staleTolerance
}

// sourceMappingURLRe matches a "//# sourceMappingURL=..." or legacy
// "//@ sourceMappingURL=..." comment that is the *entire* line it appears
// on (surrounding horizontal whitespace aside), as emitted by tsc, bun
// build, webpack and esbuild. Anchoring to the whole line (rather than
// searching for the substring anywhere) means text that merely contains
// that phrase - inside a string literal, for instance - is never mistaken
// for the real comment.
var sourceMappingURLRe = regexp.MustCompile(`^[ \t]*//[@#][ \t]*sourceMappingURL=(\S+)[ \t]*$`)

// mapEntry is one Resolver cache slot: a decoded map plus everything needed
// both to answer Resolve without touching disk again and to tell whether
// the cached answer is still valid.
type mapEntry struct {
	doc *Map
	// dir is the directory the map's relative "sources" are resolved
	// against: the .map file's own directory, or (for an inline/data: URL
	// map) the generated file's directory.
	dir   string
	stale bool

	// genMTime/genSize are the generated file's stat at the time this
	// entry was built. mapPath/mapMTime/mapSize are the same for the
	// external map file this entry was decoded from; mapPath is "" for an
	// inline data: URL map, which has no separate file to go stale or
	// change independently of the generated file.
	genMTime int64
	genSize  int64
	mapPath  string
	mapMTime int64
	mapSize  int64
}

// freshFor reports whether e is still valid for the generated file
// described by genInfo: neither the generated file nor (for an external
// map) the map file it was decoded from has changed size or mtime since.
// Re-checking the map file here - not just the generated file - means a
// map regenerated in place (the generated file untouched) is picked up,
// and keeping exactly one entry per generated path (see Resolver) means
// the cache never grows past the number of distinct paths in use.
func (e *mapEntry) freshFor(genInfo os.FileInfo) bool {
	if e.genMTime != genInfo.ModTime().UnixNano() || e.genSize != genInfo.Size() {
		return false
	}
	if e.mapPath == "" {
		return true
	}
	mapInfo, err := os.Stat(e.mapPath)
	if err != nil {
		return false
	}
	return e.mapMTime == mapInfo.ModTime().UnixNano() && e.mapSize == mapInfo.Size()
}

// Resolver decodes and caches Source Map v3 documents for generated files,
// keyed by the generated file's absolute path. Each lookup re-validates the
// cached entry against both the generated file's and (when applicable) the
// map file's current (mtime, size), so an edited-and-rebuilt file - or a
// map regenerated on its own - is re-resolved instead of served stale data.
type Resolver struct {
	mu    sync.Mutex
	cache map[string]*mapEntry
}

// NewResolver returns an empty Resolver ready to use.
func NewResolver() *Resolver {
	return &Resolver{cache: make(map[string]*mapEntry)}
}

// Resolve maps a 1-based (line, col) in generatedPath back to its original
// source position. col <= 0 means "no column known"; it deterministically
// resolves to the first mapped segment on that generated line.
//
// Discovery order for the map itself: a sourceMappingURL comment on the
// last non-empty line of generatedPath (inline data: URL, or a path -
// absolute, relative to generatedPath's directory, or a "file://" URL),
// then, if there is no such comment or its target can't be loaded, a
// sibling "<generatedPath>.map".
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
// col <= 0 is a deliberate "no column known" query: it returns the line's
// first segment that actually carries a source position (skipping any
// leading segment that is deliberately unmapped), marked exact. Otherwise
// it looks for an exact column match; failing that, it falls back to the
// nearest preceding segment on the line and marks the result ambiguous. If
// col is before every segment on the line, it falls back to the first
// segment instead, also marked ambiguous.
func pickSegment(segs []Segment, col int) (Segment, Mapping) {
	if col <= 0 {
		for _, s := range segs {
			if s.HasSource {
				return s, MappingExact
			}
		}
		return Segment{}, MappingExact
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
	genInfo, err := os.Stat(absGenerated)
	if err != nil {
		return nil, fmt.Errorf("sourcemap: stat %s: %w", absGenerated, err)
	}

	r.mu.Lock()
	entry, ok := r.cache[absGenerated]
	r.mu.Unlock()
	if ok && entry.freshFor(genInfo) {
		return entry, nil
	}

	entry, err = buildEntry(absGenerated, genInfo)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[absGenerated] = entry
	r.mu.Unlock()
	return entry, nil
}

func buildEntry(absGenerated string, genInfo os.FileInfo) (*mapEntry, error) {
	genDir := filepath.Dir(absGenerated)

	line, err := lastNonEmptyLine(absGenerated, genInfo.Size())
	if err != nil {
		return nil, fmt.Errorf("sourcemap: read %s: %w", absGenerated, err)
	}

	if rawURL := findSourceMappingURL(line); rawURL != "" {
		entry, ok, err := resolveMappingURL(rawURL, genDir, genInfo)
		if err != nil {
			return nil, err
		}
		if ok {
			return entry, nil
		}
		// The comment pointed somewhere this package can't load from disk
		// (a remote URL, or a file that doesn't exist): fall through to
		// the sibling ".map", per the documented discovery order, instead
		// of giving up immediately.
	}

	siblingPath := absGenerated + ".map"
	if mapInfo, statErr := os.Stat(siblingPath); statErr == nil {
		return loadExternalMap(siblingPath, mapInfo, genInfo)
	}

	return nil, fmt.Errorf("%w: %s", ErrNoSourceMap, absGenerated)
}

// resolveMappingURL loads the map a sourceMappingURL comment points at: a
// decoded inline map for a "data:" URL, or the external map file it names
// on disk. ok is false (with a nil error) when rawURL names something this
// package cannot load from disk at all - a remote URL, or a local path that
// doesn't exist - so the caller can fall back to the sibling ".map" instead
// of failing outright.
func resolveMappingURL(rawURL, genDir string, genInfo os.FileInfo) (*mapEntry, bool, error) {
	if strings.HasPrefix(rawURL, "data:") {
		payload, err := decodeDataURL(rawURL)
		if err != nil {
			return nil, false, fmt.Errorf("sourcemap: inline source map: %w", err)
		}
		doc, err := Decode(payload)
		if err != nil {
			return nil, false, fmt.Errorf("sourcemap: %w", err)
		}
		return &mapEntry{
			doc:      doc,
			dir:      genDir,
			stale:    false, // an inline map travels with the file: it can never independently go stale.
			genMTime: genInfo.ModTime().UnixNano(),
			genSize:  genInfo.Size(),
		}, true, nil
	}

	mapPath, ok := mappingURLToPath(rawURL, genDir)
	if !ok {
		return nil, false, nil
	}
	mapInfo, err := os.Stat(mapPath)
	if err != nil {
		return nil, false, nil
	}
	entry, err := loadExternalMap(mapPath, mapInfo, genInfo)
	if err != nil {
		return nil, false, err
	}
	return entry, true, nil
}

func loadExternalMap(mapPath string, mapInfo, genInfo os.FileInfo) (*mapEntry, error) {
	data, err := os.ReadFile(mapPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNoSourceMap, mapPath, err)
	}
	doc, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("sourcemap: %s: %w", mapPath, err)
	}
	return &mapEntry{
		doc:      doc,
		dir:      filepath.Dir(mapPath),
		stale:    isStale(mapInfo, genInfo),
		genMTime: genInfo.ModTime().UnixNano(),
		genSize:  genInfo.Size(),
		mapPath:  mapPath,
		mapMTime: mapInfo.ModTime().UnixNano(),
		mapSize:  mapInfo.Size(),
	}, nil
}

// initialTailSize is the first, common-case chunk lastNonEmptyLine reads
// from the end of the generated file. maxTailSize bounds how far back it
// will ever look: generous enough for even a very large inline source-map
// comment, without risking an unbounded read on a pathological input.
const (
	initialTailSize = 8192
	maxTailSize     = 64 << 20
)

// lastNonEmptyLine returns the generated file's last non-blank line
// (trailing "\r", "\n" and horizontal whitespace trimmed), reading only as
// much of the file as necessary from the end. Most generated files need
// only the last few KB; a single-line inline "data:" URL comment can run
// past that, so the read doubles until it has captured a complete line
// (found the newline before it) or reached the start of the file.
func lastNonEmptyLine(path string, size int64) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	for n := int64(initialTailSize); ; n *= 2 {
		readSize := n
		if readSize > size {
			readSize = size
		}
		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, size-readSize); err != nil {
			return nil, err
		}
		trimmed := bytes.TrimRight(buf, "\r\n \t")
		if nl := bytes.LastIndexByte(trimmed, '\n'); nl >= 0 {
			return trimmed[nl+1:], nil
		}
		if readSize == size || readSize >= maxTailSize {
			return trimmed, nil
		}
	}
}

// findSourceMappingURL returns the target of a sourceMappingURL comment
// that is the whole of the given line, or "" if the line isn't one.
func findSourceMappingURL(line []byte) string {
	m := sourceMappingURLRe.FindSubmatch(line)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// decodeDataURL decodes a "data:application/json[;charset=...][;base64],..."
// URL into its raw JSON payload. The base64 branch tolerates both padded and
// unpadded input (bun and tsc's own encoders differ on this). The
// percent-encoded branch uses url.PathUnescape rather than
// url.QueryUnescape: the latter also turns '+' into a space, which is wrong
// here since '+' is a valid, common Base64 VLQ digit that appears verbatim
// in an unencoded "mappings" string.
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
		decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "="))
		if err != nil {
			return nil, fmt.Errorf("base64 decode: %w", err)
		}
		return decoded, nil
	}
	unescaped, err := url.PathUnescape(payload)
	if err != nil {
		return nil, fmt.Errorf("percent-decode: %w", err)
	}
	return []byte(unescaped), nil
}

// splitURLScheme reports whether s begins with "<scheme>://" (e.g.
// "file://", "webpack://", "https://") and, if so, returns the scheme name
// and everything after the "://". It requires the full "://" separator, not
// just ":", so a Windows-style absolute path such as "C:\Users\x" is never
// mistaken for a URL with scheme "C".
func splitURLScheme(s string) (scheme, rest string, ok bool) {
	idx := strings.Index(s, "://")
	if idx <= 0 {
		return "", "", false
	}
	for _, c := range s[:idx] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '+', c == '-', c == '.':
		default:
			return "", "", false
		}
	}
	return s[:idx], s[idx+len("://"):], true
}

// mappingURLToPath converts a sourceMappingURL comment's non-"data:" target
// into a local filesystem path resolved against the generated file's
// directory. It strips a trailing query string or fragment (webpack emits
// "<file>.map?<contenthash>"), percent-unescapes the rest, and returns
// ok=false for a scheme this package cannot read from disk directly (http,
// https, or anything else besides a plain path or "file://"), so the
// caller falls back to the sibling "<file>.map" instead of failing.
func mappingURLToPath(rawURL, genDir string) (string, bool) {
	clean := rawURL
	if i := strings.IndexAny(clean, "?#"); i >= 0 {
		clean = clean[:i]
	}
	if scheme, rest, ok := splitURLScheme(clean); ok {
		if scheme != "file" {
			return "", false
		}
		clean = rest
	}
	if unescaped, err := url.PathUnescape(clean); err == nil {
		clean = unescaped
	}
	p := filepath.FromSlash(clean)
	if filepath.IsAbs(p) {
		return filepath.Clean(p), true
	}
	return filepath.Clean(filepath.Join(genDir, p)), true
}

// resolveSourcePath turns a map's "sources" entry into a path, per the
// spec: the effective path is sourceRoot + "/" + source when sourceRoot is
// set, resolved against the map's own directory. A source or sourceRoot
// carrying a "file://" URL is converted to the local path it names; one
// carrying any other scheme (webpack://, http(s)://, ...) isn't a real
// filesystem location, so it - and, for a scheme-carrying sourceRoot, the
// source alone - is returned unchanged rather than silently joined onto
// mapDir into a path that looks real but doesn't exist.
func resolveSourcePath(mapDir, sourceRoot, source string) string {
	if scheme, rest, ok := splitURLScheme(source); ok {
		if scheme == "file" {
			return filepath.Clean(filepath.FromSlash(rest))
		}
		return source
	}

	root := sourceRoot
	if scheme, rest, ok := splitURLScheme(sourceRoot); ok {
		if scheme == "file" {
			root = filepath.FromSlash(rest)
		} else {
			root = ""
		}
	}

	p := source
	if root != "" {
		p = strings.TrimSuffix(root, "/") + "/" + strings.TrimPrefix(source, "/")
	}
	p = filepath.FromSlash(p)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(mapDir, p))
}
