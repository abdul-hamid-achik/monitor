package widgets

import "time"

// activityEmptyChar/activityActiveChar are RenderActivityLine's two output
// characters.
const (
	activityEmptyChar  = '.'
	activityActiveChar = '#'
)

// RenderActivityLine renders a compact, ANSI-free activity sparkline: one
// character per bucket, '.' for an empty bucket and '#' for a bucket with at
// least one occurrence -- the `monitor issues` list's 24h column (see the
// local-sentry roadmap's UX mockup, e.g. "......####").
//
// This is deliberately NOT Sparkline (above): Sparkline emits
// lipgloss-styled, ANSI-colored block glyphs sized for the Studio TUI, but
// `monitor issues`' plain-text table prints through a text/tabwriter, which
// counts raw bytes for column alignment -- an ANSI escape sequence in one
// cell would silently misalign every column after it. RenderActivityLine
// never emits color and is always exactly len(buckets) runes wide, so it
// composes safely with tabwriter.
func RenderActivityLine(buckets []int64) string {
	line := make([]byte, len(buckets))
	for i, count := range buckets {
		if count > 0 {
			line[i] = activityActiveChar
		} else {
			line[i] = activityEmptyChar
		}
	}
	return string(line)
}

// BucketCounts sorts each of times into one of bucketCount equal-width
// buckets spanning [since, until) and returns how many fall in each,
// oldest bucket first -- the shape RenderActivityLine's caller
// (`monitor issues`) needs from a raw list of occurrence timestamps. A
// timestamp before since or at/after until is dropped (out of window); one
// exactly on a bucket boundary lands in the later bucket, except the final
// boundary (until itself), which -- being exclusive -- has no bucket to its
// right and is dropped instead of panicking or silently landing outside the
// slice.
//
// bucketCount <= 0 or until <= since returns an empty, non-nil slice.
func BucketCounts(times []time.Time, since, until time.Time, bucketCount int) []int64 {
	if bucketCount <= 0 || !until.After(since) {
		return []int64{}
	}
	buckets := make([]int64, bucketCount)
	span := until.Sub(since)
	width := span / time.Duration(bucketCount)
	for _, t := range times {
		if t.Before(since) || !t.Before(until) {
			continue
		}
		idx := int(t.Sub(since) / width)
		if idx >= bucketCount {
			idx = bucketCount - 1
		}
		if idx < 0 {
			idx = 0
		}
		buckets[idx]++
	}
	return buckets
}
