package profiler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/pprof/profile"
)

// symbolLineKey identifies one (function, file, line) attribution point.
// pprof reuses the same Location across every sample that hits the same PC,
// and inlining means one Location can carry several Lines (the innermost
// inlined function first, its callers after), so this is keyed on the
// resolved (func,file,line) triple rather than the Location/Line pointers
// themselves.
type symbolLineKey struct {
	Func string
	File string
	Line int
}

// pprofLineIdentity resolves one profile.Line to its (func,file,line) key.
// A Line with no Function (should not happen for a well-formed profile, but
// pprof does not guarantee it) degrades honestly to "(unknown)" instead of
// silently mislabeling a different, unrelated function the way the old
// text-parsing heuristic did (renaming any function whose name happened to
// end in "s" to "unknown").
func pprofLineIdentity(ln profile.Line) symbolLineKey {
	if ln.Function == nil {
		return symbolLineKey{Func: "(unknown)"}
	}
	fn := ln.Function.Name
	if fn == "" {
		fn = "(unknown)"
	}
	return symbolLineKey{Func: fn, File: ln.Function.Filename, Line: int(ln.Line)}
}

// symbolsFromPprof computes flat (self) and cum (cumulative) percentages per
// (func, file, line) directly from a decoded pprof profile, replacing the
// old `go tool pprof -top -lines` text-scrape (which required the go
// toolchain on PATH, deduplicated by function name alone — dropping
// same-file siblings whose name happened to collide after a text-parsing
// bug renamed them — and never saw more than a flat top-25).
//
// For each sample whose selected value is > 0:
//   - flat is credited only to the innermost inlined line of the leaf
//     Location (Sample.Location[0], Line[0]): the exact statement that was
//     executing when the sample landed.
//   - cum is credited to every (func,file,line) touched anywhere in the
//     sample's stack — every Location, every inlined Line within it — but
//     only ONCE per sample per key. That recursion guard is what makes cum
//     a true "was this line on the stack" measure: without it, a
//     recursive function would inflate its own cum by the recursion depth
//     of a single sample instead of counting that sample once.
//
// A caller's own Location/Line for the exact statement where it invoked a
// callee (e.g. heavyStringify's `json.Marshal(...)` call) appears as an
// ancestor frame in every sample whose leaf is inside that callee, so it
// naturally accumulates a large cum even though its own flat time is
// near-zero — this is how a wrapper function's hot *line* surfaces without
// any special-casing.
func symbolsFromPprof(prof *profile.Profile, valueIndex int) []Symbol {
	if prof == nil || valueIndex < 0 {
		return nil
	}
	flat := make(map[symbolLineKey]int64)
	cum := make(map[symbolLineKey]int64)
	var order []symbolLineKey
	seen := func(k symbolLineKey) {
		_, inFlat := flat[k]
		_, inCum := cum[k]
		if !inFlat && !inCum {
			order = append(order, k)
		}
	}

	var total int64
	for _, s := range prof.Sample {
		if valueIndex >= len(s.Value) {
			continue
		}
		v := s.Value[valueIndex]
		if v == 0 {
			continue
		}
		total += v

		if len(s.Location) > 0 && len(s.Location[0].Line) > 0 {
			k := pprofLineIdentity(s.Location[0].Line[0])
			seen(k)
			flat[k] += v
		}

		// Recursion guard: one (func,file,line) counts once per sample no
		// matter how many locations/inlined lines in this stack resolve to
		// it (direct or mutual recursion would otherwise inflate cum by
		// the recursion depth of a single sample).
		seenInSample := make(map[symbolLineKey]bool)
		for _, loc := range s.Location {
			for _, ln := range loc.Line {
				k := pprofLineIdentity(ln)
				if seenInSample[k] {
					continue
				}
				seenInSample[k] = true
				seen(k)
				cum[k] += v
			}
		}
	}
	if total <= 0 {
		return nil
	}

	out := make([]Symbol, 0, len(order))
	for _, k := range order {
		c := cum[k]
		if c <= 0 {
			// Reached only via flat (shouldn't happen: flat always implies
			// the same location is also walked for cum), but skip rather
			// than emit a symbol with no cumulative weight at all.
			continue
		}
		out = append(out, Symbol{
			Func:   k.Func,
			File:   k.File,
			Line:   k.Line,
			Weight: float64(flat[k]) / float64(total) * 100,
			Cum:    float64(c) / float64(total) * 100,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cum != out[j].Cum {
			return out[i].Cum > out[j].Cum
		}
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// pprofValueIndex finds the sample-value column to weight symbols by,
// matching prof.SampleType[i].Type case-insensitively against preferred, in
// preference order (e.g. heap prefers "inuse_space" over "alloc_space" so
// the default view answers "what's using memory right now", not "what has
// ever been allocated"). Falls back to the last value column — pprof's own
// convention for which column a profile's tools default to displaying —
// when none of preferred match, and to 0 for a profile with no SampleType
// metadata at all.
func pprofValueIndex(prof *profile.Profile, preferred ...string) int {
	for _, want := range preferred {
		for i, st := range prof.SampleType {
			if strings.EqualFold(st.Type, want) {
				return i
			}
		}
	}
	if n := len(prof.SampleType); n > 0 {
		return n - 1
	}
	return 0
}

// summarizeSymbols renders a readable top-N text summary of aggregated pprof
// symbols for Profile.Text (heap/goroutine no longer fetch a ?debug=1 text
// dump — the proto is now the only wire format — so this is what a human
// reads in `monitor profile` without --json).
func summarizeSymbols(syms []Symbol, n int) string {
	if len(syms) == 0 {
		return ""
	}
	if n <= 0 || n > len(syms) {
		n = len(syms)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-8s %-8s  %-40s %s\n", "flat%", "cum%", "func", "file:line")
	for _, s := range syms[:n] {
		loc := s.File
		if s.Line > 0 {
			loc = fmt.Sprintf("%s:%d", s.File, s.Line)
		}
		fmt.Fprintf(&b, "%6.2f%%  %6.2f%%  %-40s %s\n", s.Weight, s.Cum, s.Func, loc)
	}
	return b.String()
}
