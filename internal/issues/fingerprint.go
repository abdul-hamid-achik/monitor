package issues

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

var (
	uuidPattern  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	hexPattern   = regexp.MustCompile(`(?i)\b(?:0x)?[0-9a-f]{12,}\b`)
	numPattern   = regexp.MustCompile(`\b\d+\b`)
	spacePattern = regexp.MustCompile(`\s+`)
)

// anonFunctionMarkers are the generic frame names every runtime prints for
// code that has no stable symbol of its own: V8's "<anonymous>" (also seen
// qualified as "new <anonymous>", "async <anonymous>" or
// "Object.<anonymous>"), and Python's "<lambda>", "<listcomp>",
// "<dictcomp>", "<setcomp>", "<genexpr>" and "<module>". Rendered verbatim
// into the fingerprint token they make every such frame in a file the SAME
// token, so two different bugs in two inline callbacks (the normal
// Express/Koa/Fastify route style) merge into one issue whose culprit then
// flips between their lines (CC-5). See the naming ADR §6 revision.
var anonFunctionMarkers = map[string]bool{
	"<anonymous>": true,
	"<lambda>":    true,
	"<listcomp>":  true,
	"<dictcomp>":  true,
	"<setcomp>":   true,
	"<genexpr>":   true,
	"<module>":    true,
}

// v8QualifierPattern strips V8's method-kind qualifiers ("new ", "async ",
// "async* ") from the head of a printed frame function name, so the
// anonymity check below sees the bare symbol ("async <anonymous>" is still
// an anonymous frame).
var v8QualifierPattern = regexp.MustCompile(`^(?:async\*?|new)\s+`)

// goClosureSuffixPattern matches the numbered suffixes the Go toolchain
// synthesizes for function literals -- "main.main.func1", "pkg.f.gowrap1",
// "main.main.func1.2" (and "glob..func1") -- which renumber whenever an
// unrelated closure is added above the crashing one. In the fingerprint
// token only (never the display name or culprit), the number is normalized
// away and a context-line disambiguator is added instead (CC-8). See the
// naming ADR §6 revision.
var goClosureSuffixPattern = regexp.MustCompile(`\.(func|gowrap)\d+(\.\d+)*`)

// maxContextFileBytes bounds how much of a frame's source file the
// fingerprint reads for the anonymous/closure context line: any real source
// file fits, and a pathological multi-megabyte single-line file cannot make
// the crash-recording path do an unbounded read.
const maxContextFileBytes = 1 << 20

// FingerprintInput contains only stable issue identity. It intentionally has
// no PID, run, release, timestamp, artifact, or tree-hash fields.
type FingerprintInput struct {
	Project       string
	Service       string
	Kind          string
	ExceptionType string
	Message       string
	Symbols       []string
}

// FingerprintV1 returns a deterministic SHA-256 fingerprint. Dynamic values in
// messages are normalized, and symbol order does not affect the result.
func FingerprintV1(in FingerprintInput) string {
	symbols := normalizedSymbols(in.Symbols)
	parts := []string{
		FingerprintVersionV1,
		normalizeIdentity(in.Project),
		normalizeIdentity(in.Service),
		normalizeIdentity(in.Kind),
		normalizeIdentity(in.ExceptionType),
		normalizeMessage(in.Message),
		strings.Join(symbols, "\x1e"),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func normalizeIdentity(value string) string {
	return strings.ToLower(spacePattern.ReplaceAllString(strings.TrimSpace(value), " "))
}

func normalizeMessage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = uuidPattern.ReplaceAllString(value, "<uuid>")
	value = hexPattern.ReplaceAllString(value, "<hex>")
	value = numPattern.ReplaceAllString(value, "<n>")
	return spacePattern.ReplaceAllString(value, " ")
}

// maxFingerprintFrames is the exception-chain fingerprint's "top-5 in_app
// frames" cap; see the naming ADR §6.
const maxFingerprintFrames = 5

// FingerprintV2Exception is the exception-chain fingerprint rule
// (the naming ADR §6):
//
//	sha256("v2", "exception", project, outer.Type, outerFrames, innermost.Type)
//
// outerFrames is the outer exception's top-5 in_app frames -- counted from
// the crash frame backward, so the LAST 5 of the in_app-filtered subset, not
// the first 5 -- rendered "func@relfile" with line/column numbers stripped.
// When the outer exception has zero in_app frames, outerFrames is replaced
// by the outer exception's normalized message instead (the same
// normalization FingerprintV1 uses), so two events that differ only in a
// dynamic value (an IP, an id) still group. The innermost cause is
// ex.Chained's last entry, or ex itself when there is no chain.
//
// Two token rules (the naming ADR §6 revision, CC-5 and CC-8) keep frames
// with no stable symbol from collapsing into one issue or fragmenting on
// unrelated renumbering; both apply to the FINGERPRINT token only -- the
// display culprit keeps the frame exactly as printed:
//
//   - An anonymous frame (Function empty or a generic marker such as
//     "<anonymous>", "<lambda>", "<module>") renders
//     "<anon>:<ctx>@relfile" instead of "@relfile", where <ctx> is a short
//     hash of the frame's whitespace-normalized source context line (the
//     line number itself when the source is not readable). Two different
//     anonymous callbacks in one file therefore group apart, while the same
//     callback keeps a stable token across crashes.
//   - A Go closure frame ("main.main.func1", "pkg.f.gowrap1.2") normalizes
//     the numbered suffix to "func*"/"gowrap*" and appends the same <ctx>
//     disambiguator, so adding an unrelated closure above the crashing one
//     does not fork the issue, while two closures in one function still
//     differ.
//
// ex must already have had stacktrace.ApplyGitRoot applied (Frame.InApp and
// the git-root-relative Filename set) -- RecordException does this before
// calling. Sampled/profiler symbols, codemap FQNs, vecgrep scores, PIDs,
// releases, and the service name never enter this hash.
func FingerprintV2Exception(ex stacktrace.Exception, project string) string {
	outer := topInAppFrames(ex.Frames, maxFingerprintFrames)
	var outerPart string
	if len(outer) == 0 {
		outerPart = normalizeMessage(ex.Value)
	} else {
		fileCache := make(map[string][]byte)
		rendered := make([]string, len(outer))
		for i, f := range outer {
			rendered[i] = fingerprintFrameToken(f, ex.Runtime == "go", fileCache)
		}
		outerPart = strings.Join(rendered, "\x1e")
	}
	parts := []string{
		FingerprintVersionV2,
		"exception",
		normalizeIdentity(project),
		normalizeIdentity(ex.Type),
		outerPart,
		normalizeIdentity(innermostCauseType(ex)),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// fingerprintFrameToken renders one frame's FINGERPRINT token (never the
// display culprit, which keeps Function/Filename/Lineno exactly as
// printed). Named, non-closure functions render the plain "func@relfile"
// the naming ADR §6 always had, so their token keeps ignoring line numbers.
// Anonymous and Go-closure frames carry the context-line disambiguator
// instead (see FingerprintV2Exception's doc comment). fileCache memoizes
// source reads per FingerprintV2Exception call, so five frames from one
// file cost one read.
func fingerprintFrameToken(f stacktrace.Frame, goRuntime bool, fileCache map[string][]byte) string {
	if isAnonymousFrameFunction(f.Function) {
		return "<anon>:" + frameContextToken(f, fileCache) + "@" + f.Filename
	}
	fn := f.Function
	if goRuntime && goClosureSuffixPattern.MatchString(fn) {
		fn = goClosureSuffixPattern.ReplaceAllString(fn, ".$1*")
		return fn + ":" + frameContextToken(f, fileCache) + "@" + f.Filename
	}
	return fn + "@" + f.Filename
}

// isAnonymousFrameFunction reports whether fn names no stable symbol: an
// empty name (V8's "at /path/file.js:3:47"), or a generic marker either
// bare or as the last dot-separated segment ("Object.<anonymous>"). V8's
// "new "/"async " qualifiers are stripped first (CC-5).
func isAnonymousFrameFunction(fn string) bool {
	fn = v8QualifierPattern.ReplaceAllString(fn, "")
	if fn == "" {
		return true
	}
	segment := fn
	if i := strings.LastIndex(fn, "."); i >= 0 {
		segment = fn[i+1:]
	}
	return anonFunctionMarkers[segment]
}

// frameContextToken returns the disambiguator for an anonymous or
// Go-closure token: a short hash of the frame's whitespace-normalized
// source context line. The line is read from AbsPath when that file is
// readable (bounded by maxContextFileBytes); otherwise the token falls
// back to the line number, which still separates two anonymous frames in
// one file at the cost of shifting when code is inserted above them.
// Only a fixed-size digest ever enters the fingerprint, never source text.
func frameContextToken(f stacktrace.Frame, fileCache map[string][]byte) string {
	if f.AbsPath != "" && f.Lineno > 0 {
		data, cached := fileCache[f.AbsPath]
		if !cached {
			data = readContextFile(f.AbsPath)
			fileCache[f.AbsPath] = data
		}
		if line, ok := lineAt(data, f.Lineno); ok {
			line = strings.TrimRight(line, "\r")
			normalized := strings.TrimSpace(spacePattern.ReplaceAllString(line, " "))
			sum := sha256.Sum256([]byte(normalized))
			return hex.EncodeToString(sum[:4])
		}
	}
	return strconv.Itoa(f.Lineno)
}

// readContextFile reads at most maxContextFileBytes of path. It returns nil
// on any error (unreadable path, removed file, symlink loop), which
// frameContextToken treats as "fall back to the line number".
func readContextFile(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxContextFileBytes))
	if err != nil {
		return nil
	}
	return data
}

// lineAt returns the 1-based lineno'th line of data ("line" meaning
// newline-terminated or final partial). ok is false when lineno is out of
// range, which is the caller's signal that the frame's source no longer
// matches its line number.
func lineAt(data []byte, lineno int) (string, bool) {
	if lineno <= 0 || len(data) == 0 {
		return "", false
	}
	start := 0
	for i := 1; i < lineno; i++ {
		idx := bytes.IndexByte(data[start:], '\n')
		if idx < 0 {
			return "", false
		}
		start += idx + 1
	}
	if start >= len(data) {
		return "", false
	}
	end := len(data) - start
	if idx := bytes.IndexByte(data[start:], '\n'); idx >= 0 {
		end = idx
	}
	return string(data[start : start+end]), true
}

// topInAppFrames filters frames to those with InApp set and returns at most
// n of them, closest to the crash frame (frames' last element) -- i.e. the
// filtered subsequence's own last n entries, in their original relative
// (oldest-to-newest) order.
func topInAppFrames(frames []stacktrace.Frame, n int) []stacktrace.Frame {
	inApp := make([]stacktrace.Frame, 0, len(frames))
	for _, f := range frames {
		if f.InApp {
			inApp = append(inApp, f)
		}
	}
	if len(inApp) > n {
		inApp = inApp[len(inApp)-n:]
	}
	return inApp
}

// innermostCauseType returns the innermost cause's Type: ex.Chained's last
// entry when there is a chain, else ex's own Type (ex.Chained is already a
// flat outer-to-innermost list; every parser in internal/stacktrace builds
// it that way, so no recursive walk is needed).
func innermostCauseType(ex stacktrace.Exception) string {
	if len(ex.Chained) == 0 {
		return ex.Type
	}
	return ex.Chained[len(ex.Chained)-1].Type
}

func normalizedSymbols(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeIdentity(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
