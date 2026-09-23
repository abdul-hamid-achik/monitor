package stacktrace

import (
	"regexp"
	"time"
)

// timestamp patterns recognized at (or near) the start of a line. Order
// matters only in that more specific patterns are tried first where two
// could otherwise both match a prefix of the same text.
var (
	// RFC3339 / RFC3339Nano, optionally with a "Z" or numeric offset, as
	// zap's default console/JSON encoders and this package's synthetic
	// typescript-logging format print it.
	reRFC3339 = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})`)
	// Python's logging asctime default: "2026-09-22 10:04:37,123".
	rePyAsctime = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3}`)
	// Ruby's stdlib Logger default formatter: "I, [2026-09-22T10:04:37.123456 #123]".
	reRubyLogger = regexp.MustCompile(`[A-Z], \[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+) #\d+\]`)
)

// parseTimestamp scans line for the first timestamp it recognizes and
// returns it in UTC, or the zero time and false when none is found. It
// never uses the current time as a fallback -- that decision belongs to
// the caller (mtime, or "now" for a genuinely live event), per the
// package's "the event's own time, or nothing" rule.
func parseTimestamp(line string) (time.Time, bool) {
	if m := reRubyLogger.FindStringSubmatch(line); m != nil {
		if t, err := time.Parse("2006-01-02T15:04:05.000000", m[1]); err == nil {
			return t.UTC(), true
		}
	}
	if m := rePyAsctime.FindString(line); m != "" {
		if t, err := time.Parse("2006-01-02 15:04:05,000", m); err == nil {
			return t.UTC(), true
		}
	}
	if m := reRFC3339.FindString(line); m != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, m); err == nil {
				return t.UTC(), true
			}
		}
	}
	return time.Time{}, false
}
