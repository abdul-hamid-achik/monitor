package stacktrace

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxTimestampOffset is how far into a line a timestamp may begin and still
// count as the line's own timestamp ("E, [2026-..." or "[2026-..." or a bare
// leading stamp). Anything later is part of the message ("failed at
// 2026-...") and must not become the event time.
const maxTimestampOffset = 8

// Timestamp patterns recognized near the start of a line.
var (
	// ISO 8601 / RFC 3339 with a zone: "Z", "-07:00" or zap's
	// ISO8601TimeEncoder "-0700".
	reISOZoned = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})`)
	// Python logging's default asctime: "2026-09-22 10:04:37,123" (local
	// wall-clock time, no zone).
	rePyAsctime = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:[,.]\d+)?`)
	// Ruby's stdlib Logger: "E, [2026-09-22T10:04:37.123456 #123]" (local,
	// no zone), and any other zone-less ISO stamp.
	reISOLocal = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?`)
	// Go's log package default (log.LstdFlags): "2026/09/22 10:04:37" (local).
	reGoLog = regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?`)
)

func locOrLocal(loc *time.Location) *time.Location {
	if loc == nil {
		return time.Local
	}
	return loc
}

// parseTimestamp returns the timestamp a line starts with (within
// maxTimestampOffset bytes), or the zero time and false when there is none.
// Zoned stamps keep their offset; zone-less ones (Python asctime, Ruby
// Logger, Go's log package) are wall-clock times in loc (time.Local when
// nil). It never falls back to the current time -- that decision belongs to
// the caller (mtime, or "now" for a genuinely live event).
func parseTimestamp(line string, loc *time.Location) (time.Time, bool) {
	loc = locOrLocal(loc)
	if m := findEarly(reISOZoned, line); m != "" {
		m = strings.Replace(m, ",", ".", 1)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700"} {
			if t, err := time.Parse(layout, m); err == nil {
				return t, true
			}
		}
	}
	if m := findEarly(rePyAsctime, line); m != "" {
		m = strings.Replace(m, ",", ".", 1)
		if t, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", m, loc); err == nil {
			return t, true
		}
	}
	if m := findEarly(reISOLocal, line); m != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", m, loc); err == nil {
			return t, true
		}
	}
	if m := findEarly(reGoLog, line); m != "" {
		if t, err := time.ParseInLocation("2006/01/02 15:04:05.999999999", m, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func findEarly(re *regexp.Regexp, line string) string {
	head := line
	if len(head) > maxTimestampOffset+40 {
		head = head[:maxTimestampOffset+40]
	}
	loc := re.FindStringIndex(head)
	if loc == nil || loc[0] > maxTimestampOffset {
		return ""
	}
	return head[loc[0]:loc[1]]
}

// blockTimestamp is the ObservedAt rule shared by every parser: the block's
// first line's own timestamp, else the one on the logger line immediately
// before the block.
func blockTimestamp(b Block) time.Time {
	if len(b.Lines) > 0 {
		if t, ok := parseTimestamp(b.Lines[0], b.Location); ok {
			return t
		}
	}
	if b.Prev != "" {
		if t, ok := parseTimestamp(b.Prev, b.Location); ok {
			return t
		}
	}
	return time.Time{}
}

// parseEpoch reads a numeric epoch timestamp as zap's Epoch*TimeEncoders
// print it (seconds as a float such as 1790137826.333802 or
// 1.790137826326457e+09, or integer millis/micros/nanos), telling the unit
// apart by magnitude.
func parseEpoch(s string) (time.Time, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return time.Time{}, false
	}
	switch {
	case f >= 1e17: // nanoseconds
		return time.Unix(0, int64(f)), true
	case f >= 1e14: // microseconds
		return time.UnixMicro(int64(f)), true
	case f >= 1e11: // milliseconds
		return time.UnixMilli(int64(f)), true
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(math.Round(frac*1e6))*1e3), true
}

// parseAnyTimestamp reads a timestamp field value that may be either an
// epoch number or an ISO/asctime string (zap JSON "ts", tslog headers).
func parseAnyTimestamp(s string, loc *time.Location) (time.Time, bool) {
	if t, ok := parseEpoch(s); ok {
		return t, true
	}
	return parseTimestamp(s, loc)
}
