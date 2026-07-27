package issues

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
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
