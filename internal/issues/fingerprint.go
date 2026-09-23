package issues

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"

	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

var (
	uuidPattern  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	hexPattern   = regexp.MustCompile(`(?i)\b(?:0x)?[0-9a-f]{12,}\b`)
	numPattern   = regexp.MustCompile(`\b\d+\b`)
	spacePattern = regexp.MustCompile(`\s+`)
)

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
// frames" cap; see docs/contracts/local-sentry-naming.md §6.
const maxFingerprintFrames = 5

// FingerprintV2Exception is the exception-chain fingerprint rule
// (docs/contracts/local-sentry-naming.md §6):
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
		rendered := make([]string, len(outer))
		for i, f := range outer {
			rendered[i] = f.Function + "@" + f.Filename
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
