package stacktrace

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// lineTimestampRE matches the RFC 3339 timestamp `docker compose logs
// --timestamps --no-log-prefix` (and journald's short-iso-precise date
// field) puts at the start of every line: a date, a time with optional
// fraction, and a zone (Z or a numeric offset, with or without a colon),
// then one space.
var lineTimestampRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:?\d{2})) `)

// LineTimestamps strips a per-line timestamp prefix before lines reach the
// Joiner and remembers each one, so a block can still take its ObservedAt
// from the log line that opened it. Without stripping, the prefix hides
// every grammar's opening line ("TypeError: ...", "Traceback ...") and a
// timestamped container log yields no exceptions at all.
//
// Strip must see every line fed to the Joiner, exactly once and in order:
// its counter is how a block's 1-based LineStart maps back to a timestamp.
type LineTimestamps struct {
	line  int
	times map[int]time.Time
}

// NewLineTimestamps returns an empty recorder.
func NewLineTimestamps() *LineTimestamps {
	return &LineTimestamps{times: map[int]time.Time{}}
}

// Strip removes a leading timestamp (when present) and records it against
// this line's number. A line without one passes through unchanged.
func (l *LineTimestamps) Strip(line string) string {
	l.line++
	m := lineTimestampRE.FindStringSubmatchIndex(line)
	if m == nil {
		return line
	}
	if t, ok := parseLineTimestamp(line[m[2]:m[3]]); ok {
		l.times[l.line] = t
	}
	return line[m[1]:]
}

// At returns the timestamp recorded for line n (1-based), or the nearest
// earlier one: a stack frame line can lack its own stamp when a runtime
// writes a multi-line message in one go. Zero when none precedes it.
func (l *LineTimestamps) At(n int) time.Time {
	for i := n; i >= 1 && i > n-64; i-- {
		if t, ok := l.times[i]; ok {
			return t
		}
	}
	return time.Time{}
}

func parseLineTimestamp(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// PathMap rewrites an absolute path prefix, e.g. a container's /app to the
// host checkout it was built from, so frames printed inside a container can
// be recognised as in-app and made root-relative against the host's git
// root.
type PathMap struct {
	From, To string
}

// ParsePathMap reads a "FROM=TO" mapping. Both sides must be absolute and
// are cleaned, so "/app/" and "/app" map the same way.
func ParsePathMap(spec string) (PathMap, error) {
	from, to, ok := strings.Cut(spec, "=")
	if !ok || from == "" || to == "" {
		return PathMap{}, fmt.Errorf("path map %q: want FROM=TO", spec)
	}
	if !path.IsAbs(from) || !path.IsAbs(to) {
		return PathMap{}, errors.New("path map: FROM and TO must both be absolute paths")
	}
	return PathMap{From: path.Clean(from), To: path.Clean(to)}, nil
}

// ApplyPathMap rewrites every frame path (and every chained cause's) that
// sits under a mapping's From; the first matching mapping wins. It runs
// before ApplyGitRoot, which then judges in_app against the rewritten path.
func ApplyPathMap(ex *Exception, maps []PathMap) {
	if ex == nil || len(maps) == 0 {
		return
	}
	for i := range ex.Frames {
		f := &ex.Frames[i]
		f.AbsPath = mapPath(f.AbsPath, maps)
		f.Filename = mapPath(f.Filename, maps)
	}
	for i := range ex.Chained {
		ApplyPathMap(&ex.Chained[i], maps)
	}
}

func mapPath(p string, maps []PathMap) string {
	if p == "" || !path.IsAbs(p) {
		return p
	}
	for _, m := range maps {
		if p == m.From {
			return m.To
		}
		if strings.HasPrefix(p, m.From+"/") || m.From == "/" {
			return path.Join(m.To, strings.TrimPrefix(p, m.From))
		}
	}
	return p
}
