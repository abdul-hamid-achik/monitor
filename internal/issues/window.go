package issues

import (
	"fmt"
	"strings"
	"time"
)

// ParseWindowBound parses a --since/--until value (the `monitor issues list`
// CLI flags) or an MCP monitor_issues since/until filter string: either an
// RFC3339 timestamp, or a Go duration (e.g. "10m", "24h") interpreted as
// "that long before now". Empty input returns the zero time.Time and no
// error, meaning "no bound" (see ListOptions.Since/Until). This is the one
// place both surfaces parse these filters, so a CLI --since and an MCP
// since with the same value always mean the same instant.
func ParseWindowBound(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: want RFC3339 (e.g. 2026-09-22T18:00:00Z) or a duration like 10m, 24h", raw)
	}
	if d < 0 {
		return time.Time{}, fmt.Errorf("invalid duration %q: must be positive (it is measured back from now)", raw)
	}
	return now.Add(-d).UTC(), nil
}
