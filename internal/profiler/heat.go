// heat.go builds monitor.line_heatmap.v1 (see docs/contracts/line-heatmap-v1.md)
// from a loaded profile (LoadFile, in loadfile.go): which LINE inside a
// function is hot, not just which function. Two producers feed one shared
// model:
//
//   - FromCDP walks a V8/Bun .cpuprofile's call tree. Per-line SELF comes
//     straight from positionTicks (already statement-granular); per-line
//     CUM is not computable at that granularity — V8 positionTicks are
//     self-time only — so a line's Cum always equals its Self, and a
//     wrapper function's real cost surfaces instead under its Callees.
//     Function-level Cum, by contrast, IS computable, by summing the
//     call-tree subtree each function's node(s) root.
//   - FromPprof walks a decoded pprof proto's samples directly (no
//     dependency on the go toolchain or `go tool pprof`), crediting flat
//     to a sample's leaf line and cum to every (func,line) on its stack
//     once per sample — the same rules pprof.go's symbolsFromPprof uses for
//     the flat, ungrouped `monitor profile` view, restructured here into
//     BuildHeatmap's per-function model with raw integer counts (the
//     schema's self/cum fields), not just percentages.
package profiler

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/pprof/profile"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/sourcemap"
)

// HeatSchema is the schema id stamped on every monitor.line_heatmap.v1
// document BuildHeatmap produces.
const HeatSchema = "monitor.line_heatmap.v1"

// HeatProfileType is the heatmap's own profile-type discriminator. It is
// richer than ProfileType (which has one undifferentiated ProfileHeap):
// heap has two genuinely different questions ("what's using memory right
// now" vs "what has ever been allocated"), and a heatmap has to pick one
// pprof sample-value column to weight lines by, so it needs to say which.
type HeatProfileType string

const (
	HeatCPU       HeatProfileType = "cpu"
	HeatHeapInuse HeatProfileType = "heap_inuse"
	HeatHeapAlloc HeatProfileType = "heap_alloc"
	HeatGoroutine HeatProfileType = "goroutine"
)

// HeatMethod identifies which producer, and which fidelity of it, built a
// Heatmap.
type HeatMethod string

const (
	// MethodV8PositionTicks means every node that contributed self time
	// carried real positionTicks: the reported hot line is a genuine
	// statement-level attribution.
	MethodV8PositionTicks HeatMethod = "v8_position_ticks"
	// MethodCPUProfileFile means the loaded .cpuprofile had no
	// positionTicks anywhere (an older V8/inspector build): self time
	// degrades to each node's function-declaration line, the same
	// lower-fidelity fallback flattenCDPProfile itself uses when a node
	// carries no positionTicks. Reported honestly as a distinct method
	// rather than silently passed off as v8_position_ticks.
	MethodCPUProfileFile HeatMethod = "cpuprofile_file"
	// MethodPprofProto means the profile came from a decoded pprof proto
	// (Go cpu/heap/goroutine), read in-process with no `go` toolchain
	// dependency.
	MethodPprofProto HeatMethod = "pprof_proto"
)

// Heatmap is monitor.line_heatmap.v1: heat.Build's per-line CPU/heap/
// goroutine attribution model. See docs/contracts/line-heatmap-v1.md.
type Heatmap struct {
	Schema      string          `json:"schema"`
	ProfileType HeatProfileType `json:"profile_type"`
	Unit        string          `json:"unit"`
	Method      HeatMethod      `json:"method"`
	Runtime     string          `json:"runtime"`

	Samples       int     `json:"samples"`
	ActiveSamples int     `json:"active_samples"`
	IdlePct       float64 `json:"idle_pct"`
	GCPct         float64 `json:"gc_pct"`

	Functions []HeatFunction `json:"functions"`

	Warnings    []string `json:"warnings,omitempty"`
	Limitations []string `json:"limitations,omitempty"`
}

// HeatFunction is one function's line-level breakdown within a Heatmap.
type HeatFunction struct {
	Name        string  `json:"name"`
	File        string  `json:"file"`
	StartLine   int     `json:"start_line,omitempty"`
	EndLine     int     `json:"end_line,omitempty"`
	RangeSource string  `json:"range_source,omitempty"` // "codemap" | "observed"
	SelfPct     float64 `json:"self_pct"`
	CumPct      float64 `json:"cum_pct"`

	Lines   []HeatLine   `json:"lines,omitempty"`
	Callees []HeatCallee `json:"callees,omitempty"`

	// rangeHintFile/rangeHintLine carry no json tag (unexported, so
	// encoding/json skips them automatically): the function's own
	// generated (file, declaration-or-representative line), used by
	// resolveRanges as the codemap symbol-at input and as the last-resort
	// observed range for a function with zero attributed Lines (a pure
	// ancestor whose only signal is its Callees).
	rangeHintFile string
	rangeHintLine int
}

// HeatLine is one line's contribution within its enclosing HeatFunction.
type HeatLine struct {
	Line int `json:"line"`
	// Self/Cum are raw sample counts (or bytes, for heap profiles — see
	// Heatmap.Unit), never percentages, so a consumer can re-derive any
	// ratio it needs instead of trusting a lossy pre-rounded one.
	Self int64 `json:"self"`
	Cum  int64 `json:"cum"`
	// PctOfFunction is this line's share of its OWN function's weight
	// (self when the function has any self samples, else cum for a pure
	// wrapper) — never the overall profile total. See
	// docs/contracts/line-heatmap-v1.md's "Notes on specific fields".
	PctOfFunction float64 `json:"pct_of_function"`
	Code          string  `json:"code,omitempty"`
	// Mapping is "" (no source map applies), "exact", or "ambiguous" — the
	// two outcomes internal/sourcemap.Resolver itself ever returns; see the
	// naming ADR's shared Frame.Mapping enum. Never fabricated as
	// "transpiled" or "inferred" here: those describe a guess made in the
	// ABSENCE of a map, which is stacktrace.Frame's business, not this
	// package's.
	Mapping string `json:"mapping,omitempty"`
	Stale   bool   `json:"stale,omitempty"`
	// Issues is additive (v1.17, E3.4): always empty here. heat.Build never
	// computes or caches issue membership itself — a future caller
	// overlays it from the issues store at render time, so a heatmap and
	// the store never disagree about an issue's current status. The field
	// exists now purely so a later, additive change doesn't need another
	// schema bump.
	Issues []HeatLineIssue `json:"issues,omitempty"`
}

// HeatLineIssue is one issue whose culprit lands on this line (E3.4;
// unused by BuildHeatmap itself — see HeatLine.Issues).
type HeatLineIssue struct {
	ShortID string `json:"short_id"`
	Count   int    `json:"count"`
	Status  string `json:"status"`
}

// HeatCallee is one function this HeatFunction called, and how much
// cumulative weight flowed through that call.
type HeatCallee struct {
	Func string `json:"func"`
	Cum  int64  `json:"cum"`
}

// Default caps, exported so callers (monitor hot --top) can reference them
// in help text instead of hard-coding the number again.
const (
	DefaultTopFunctions = 25
	maxCalleesPerFunc   = 5
)

// idleWarningThresholdPct and diffuseWarningThresholdPct are AC-5's honesty
// thresholds: past idleWarningThresholdPct idle, CPU heat can't explain
// slowness (it's off-CPU); below diffuseWarningThresholdPct for the top
// function, no single line dominates enough to call it "the" hot line.
const (
	idleWarningThresholdPct    = 50.0
	diffuseWarningThresholdPct = 5.0
)

// HeatOptions configures BuildHeatmap.
type HeatOptions struct {
	// Func filters to one function by exact, case-sensitive name match. ""
	// keeps every function BuildHeatmap found.
	Func string
	// Top caps how many functions remain after filtering and sorting
	// (by Cum% desc, then Self% desc, then Name). <=0 uses
	// DefaultTopFunctions.
	Top int
	// ProfileType selects the pprof sample-value column (cpu/heap_inuse/
	// heap_alloc/goroutine). Ignored for a CDP source, which is always cpu.
	// "" defaults to HeatCPU.
	ProfileType HeatProfileType
	// Runtime is a caller-supplied hint (e.g. from procbind, for a live
	// capture) for the CDP path, which cannot distinguish Node/Bun/Deno
	// from a .cpuprofile's shape alone. "" degrades honestly to "unknown"
	// rather than guessing.
	Runtime string
	// Codebase scopes codemap symbol-at lookups exactly as `codemap -C
	// <path>` does. "" uses codemap's own cwd resolution.
	Codebase string
	// Sourcemaps resolves CDP frames through internal/sourcemap (E3.3a
	// wiring). nil disables source-map resolution outright (tests that
	// want purely-generated coordinates); BuildHeatmap otherwise builds one
	// lazily the first time a CDP source needs it, so callers normally
	// leave this nil and still get resolution for free.
	Sourcemaps *sourcemap.Resolver
	// NoSourcemaps disables source-map resolution even though Sourcemaps
	// is nil (BuildHeatmap would otherwise lazily construct one). Set by
	// tests that want to assert the un-mapped, generated-coordinate
	// fallback.
	NoSourcemaps bool
	// NoCodemap forces range_source "observed" even when codemap is
	// healthy, for tests that don't want a real `codemap` subprocess.
	NoCodemap bool
	// NoReadCode skips reading HeatLine.Code from disk.
	NoReadCode bool
}

func (o HeatOptions) top() int {
	if o.Top > 0 {
		return o.Top
	}
	return DefaultTopFunctions
}

func (o HeatOptions) profileType() HeatProfileType {
	if o.ProfileType == "" {
		return HeatCPU
	}
	return o.ProfileType
}

// BuildHeatmap is heat.Build: it dispatches to FromCDP or FromPprof
// depending on src.Kind, then applies the shared post-processing every
// producer needs — function-range resolution (codemap, falling back to an
// observed min/max), reading Code from disk, filtering/sorting/capping
// Functions, and the idle/diffuse honesty warnings.
func BuildHeatmap(ctx context.Context, src *Source, opts HeatOptions) (*Heatmap, error) {
	if src == nil {
		return nil, fmt.Errorf("heat.Build: nil source")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var hm *Heatmap
	var err error
	switch src.Kind {
	case SourceCDP:
		hm, err = buildFromCDP(src.CDP, opts)
	case SourcePprof:
		hm, err = buildFromPprof(src.Pprof, opts)
	default:
		return nil, fmt.Errorf("heat.Build: unknown source kind %q", src.Kind)
	}
	if err != nil {
		return nil, err
	}

	resolveRanges(ctx, hm, opts)
	if !opts.NoReadCode {
		readCode(hm)
	}
	finalizeFunctions(hm, opts)
	addWarnings(hm)
	return hm, nil
}

// ---------------------------------------------------------------------------
// FromCDP: V8/Bun .cpuprofile call tree
// ---------------------------------------------------------------------------

// heatFuncKey identifies one function across a CDP call tree — the same
// identity flattenCDPProfile (inspector.go) keys its flat per-line list by:
// (Func, File, FuncLine). FuncLine (the function's own declaration line)
// disambiguates two textually-identical anonymous closures in one file,
// which V8 labels "" / "(anonymous)" indiscriminately — without it they'd
// collide into one, wrong, merged function.
type heatFuncKey struct {
	Func     string
	File     string
	FuncLine int
}

// cdpFuncAccum accumulates one function's data across every node in the
// call tree that shares its heatFuncKey, as buildFromCDP walks the tree.
type cdpFuncAccum struct {
	lineSelf    map[int]int64
	lineOrder   []int
	cum         int64
	callees     map[heatFuncKey]int64
	calleeOrder []heatFuncKey
}

func newCDPFuncAccum() *cdpFuncAccum {
	return &cdpFuncAccum{lineSelf: map[int]int64{}, callees: map[heatFuncKey]int64{}}
}

func (a *cdpFuncAccum) addSelf(line int, ticks int64) {
	if _, ok := a.lineSelf[line]; !ok {
		a.lineOrder = append(a.lineOrder, line)
	}
	a.lineSelf[line] += ticks
}

func (a *cdpFuncAccum) addCallee(k heatFuncKey, cum int64) {
	if cum <= 0 {
		return
	}
	if _, ok := a.callees[k]; !ok {
		a.calleeOrder = append(a.calleeOrder, k)
	}
	a.callees[k] += cum
}

// buildFromCDP is FromCDP: the V8/Bun .cpuprofile producer.
func buildFromCDP(cp *cdpProfile, opts HeatOptions) (*Heatmap, error) {
	if cp == nil || len(cp.Nodes) == 0 {
		return nil, fmt.Errorf("heat.Build: empty CDP profile")
	}
	nodeByID := make(map[int64]*cdpNode, len(cp.Nodes))
	for i := range cp.Nodes {
		nodeByID[cp.Nodes[i].ID] = &cp.Nodes[i]
	}

	funcs := map[heatFuncKey]*cdpFuncAccum{}
	var funcOrder []heatFuncKey
	getFunc := func(k heatFuncKey) *cdpFuncAccum {
		a, ok := funcs[k]
		if !ok {
			a = newCDPFuncAccum()
			funcs[k] = a
			funcOrder = append(funcOrder, k)
		}
		return a
	}

	// Pass 1: per-node self attribution, mirroring flattenCDPProfile's own
	// rules exactly (inspector.go) — positionTicks when present, else the
	// node's own declaration line with its whole hitCount — plus the same
	// idle/gc/program/root pseudo-frame bookkeeping, so a live CDP capture
	// and a file-loaded one report identical Stats for the same data.
	nodeFunc := make(map[int64]heatFuncKey, len(cp.Nodes)) // absent entry marks a pseudo/excluded node
	var totalHits, idleHits, gcHits, excludedHits int64
	anyPositionTicks := false
	for i := range cp.Nodes {
		n := &cp.Nodes[i]
		totalHits += n.HitCount
		fn := n.CallFrame.FunctionName
		if fn == "" {
			fn = "(anonymous)"
		}
		if isPseudoCDPFrame(fn) {
			excludedHits += n.HitCount
			switch fn {
			case "(idle)":
				idleHits += n.HitCount
			case "(garbage collector)":
				gcHits += n.HitCount
			}
			continue
		}
		file := decodeCDPFileURL(n.CallFrame.URL)
		funcLine := int(n.CallFrame.LineNumber) + 1
		k := heatFuncKey{Func: fn, File: file, FuncLine: funcLine}
		nodeFunc[n.ID] = k
		acc := getFunc(k)
		if len(n.PositionTicks) > 0 {
			anyPositionTicks = true
			for _, pt := range n.PositionTicks {
				if pt.Ticks <= 0 {
					continue
				}
				acc.addSelf(pt.Line, pt.Ticks)
			}
			continue
		}
		if n.HitCount > 0 {
			acc.addSelf(funcLine, n.HitCount)
		}
	}
	if totalHits <= 0 {
		return nil, fmt.Errorf("heat.Build: CDP profile has no samples")
	}
	active := totalHits - excludedHits

	// Pass 2: subtree hit-sum per node, bottom-up. A CDP call tree is a
	// proper tree (each node has exactly one parent), so this plain
	// post-order sum can never double-count — the recursion hazard only
	// shows up one step later, in pass 3, when the SAME function occurs at
	// more than one tree node.
	subtree := make(map[int64]int64, len(cp.Nodes))
	visiting := make(map[int64]bool, len(cp.Nodes))
	var sumSubtree func(id int64) (int64, error)
	sumSubtree = func(id int64) (int64, error) {
		if v, ok := subtree[id]; ok {
			return v, nil
		}
		n, ok := nodeByID[id]
		if !ok {
			return 0, nil
		}
		if visiting[id] {
			return 0, fmt.Errorf("heat.Build: CDP profile node %d cycles back to itself", id)
		}
		visiting[id] = true
		total := n.HitCount
		for _, c := range n.Children {
			cv, err := sumSubtree(c)
			if err != nil {
				return 0, err
			}
			total += cv
		}
		visiting[id] = false
		subtree[id] = total
		return total, nil
	}
	var roots []int64
	childOf := make(map[int64]bool, len(cp.Nodes))
	for i := range cp.Nodes {
		for _, c := range cp.Nodes[i].Children {
			childOf[c] = true
		}
	}
	for i := range cp.Nodes {
		if !childOf[cp.Nodes[i].ID] {
			roots = append(roots, cp.Nodes[i].ID)
		}
	}
	for _, r := range roots {
		if _, err := sumSubtree(r); err != nil {
			return nil, err
		}
	}
	for id := range nodeByID {
		if _, err := sumSubtree(id); err != nil {
			return nil, err
		}
	}

	// Pass 3: function-level Cum and Callees, walked from every root with
	// the ancestor-identity guard recursion needs: a node's subtree Cum is
	// only credited to its function's total the first time that function
	// appears along THIS root-to-node path (the "outermost occurrence").
	// Without the guard, direct or mutual recursion would let an inner
	// occurrence's subtree — already included in its own outer occurrence's
	// subtree sum — double up the function's reported Cum.
	var walk func(id int64, ancestors map[heatFuncKey]bool)
	walk = func(id int64, ancestors map[heatFuncKey]bool) {
		n := nodeByID[id]
		if n == nil {
			return
		}
		k, isFunc := nodeFunc[id]
		if isFunc {
			if !ancestors[k] {
				getFunc(k).cum += subtree[id]
			}
			for _, c := range n.Children {
				if ck, ok := nodeFunc[c]; ok {
					getFunc(k).addCallee(ck, subtree[c])
				}
			}
			next := make(map[heatFuncKey]bool, len(ancestors)+1)
			for a := range ancestors {
				next[a] = true
			}
			next[k] = true
			ancestors = next
		}
		for _, c := range n.Children {
			walk(c, ancestors)
		}
	}
	for _, r := range roots {
		walk(r, map[heatFuncKey]bool{})
	}

	method := MethodV8PositionTicks
	if !anyPositionTicks {
		method = MethodCPUProfileFile
	}
	runtime := opts.Runtime
	if runtime == "" {
		runtime = "unknown"
	}

	hm := &Heatmap{
		Schema:        HeatSchema,
		ProfileType:   HeatCPU,
		Unit:          "samples",
		Method:        method,
		Runtime:       runtime,
		Samples:       int(totalHits),
		ActiveSamples: int(active),
	}
	if totalHits > 0 {
		hm.IdlePct = float64(idleHits) / float64(totalHits) * 100
		hm.GCPct = float64(gcHits) / float64(totalHits) * 100
	}

	resolver := opts.Sourcemaps
	if resolver == nil && !opts.NoSourcemaps {
		resolver = sourcemap.NewResolver()
	}
	smCache := map[string]resolvedLoc{}
	genLines := map[string][]string{}

	for _, k := range funcOrder {
		acc := funcs[k]
		var selfTotal int64
		for _, line := range acc.lineOrder {
			selfTotal += acc.lineSelf[line]
		}
		var selfPct, cumPct float64
		if active > 0 {
			selfPct = float64(selfTotal) / float64(active) * 100
			cumPct = float64(acc.cum) / float64(active) * 100
		}

		// The function's displayed File — and the (file,line) resolveRanges
		// uses for both its codemap symbol-at lookup and its "observed"
		// fallback — is resolved ONCE from the function's own declaration
		// line, rather than independently per line: HeatLine carries no
		// file field of its own (lines nest under HeatFunction.File), and
		// a source map that maps a function's body lines at all almost
		// always maps its declaration line too, so this keeps File and
		// every line's resolved Line number in the same coordinate space
		// instead of risking a File from one line's mapping paired with a
		// range computed from another's.
		f := HeatFunction{Name: k.Func, File: k.File, SelfPct: selfPct, CumPct: cumPct}
		rangeFile, rangeLine := k.File, k.FuncLine
		if declRL := resolveCDPLoc(resolver, smCache, genLines, k.File, k.FuncLine); declRL.resolved {
			f.File, rangeFile, rangeLine = declRL.file, declRL.file, declRL.line
		}
		f.setInternalRangeHint(rangeFile, rangeLine)

		sort.Ints(acc.lineOrder)
		for _, line := range acc.lineOrder {
			self := acc.lineSelf[line]
			outLine, mapping, stale := line, "", false
			if rl := resolveCDPLoc(resolver, smCache, genLines, k.File, line); rl.resolved {
				outLine, mapping, stale = rl.line, rl.mapping, rl.stale
			}
			hl := HeatLine{Line: outLine, Self: self, Cum: self, Mapping: mapping, Stale: stale}
			if selfTotal > 0 {
				hl.PctOfFunction = float64(self) / float64(selfTotal) * 100
			}
			f.Lines = append(f.Lines, hl)
		}

		sort.Slice(acc.calleeOrder, func(i, j int) bool {
			return acc.callees[acc.calleeOrder[i]] > acc.callees[acc.calleeOrder[j]]
		})
		for i, ck := range acc.calleeOrder {
			if i >= maxCalleesPerFunc {
				break
			}
			f.Callees = append(f.Callees, HeatCallee{Func: ck.Func, Cum: acc.callees[ck]})
		}

		hm.Functions = append(hm.Functions, f)
	}
	if method == MethodCPUProfileFile {
		hm.Limitations = append(hm.Limitations,
			"this .cpuprofile has no positionTicks; self time degrades to each function's declaration line instead of its actual hot statement")
	} else {
		hm.Limitations = append(hm.Limitations,
			"V8 positionTicks are self-time; a callee's own time is listed under callees, not folded into the caller's line")
	}
	return hm, nil
}

// resolvedLoc is the (possibly unchanged) result of trying to resolve one
// generated (file,line) through a source map.
type resolvedLoc struct {
	resolved bool
	file     string
	line     int
	mapping  string
	stale    bool
}

// resolveCDPLoc resolves one generated CDP (file,line) through resolver,
// memoized per (file,line) since the same generated line is often visited
// by several call-tree nodes (recursion, or several call paths into one
// hot statement). A resolver of nil (source maps disabled) or any
// resolution failure (no map, unmapped line, file missing) returns
// resolved:false — never an error — so a JS profile with no build step at
// all degrades to its own generated coordinates exactly like plain Go does.
//
// V8 positionTicks carry only a line, never a column, but Resolver.Resolve
// needs one: col<=0 deterministically picks a generated line's FIRST
// segment, which real bundler output (verified live against a bun-built
// .cpuprofile fixture, see testdata/tssrc) often spends on the line's
// leading indentation — several bundlers carry that whitespace's mapping
// over from the tail of the PREVIOUS statement, so col=0 on
// "    const s = JSON.stringify(...)" resolved one full original line too
// early. Querying at the column of the line's first non-blank character
// instead — read once from the generated file itself via genLines — lands
// on the segment for the statement that's actually there.
func resolveCDPLoc(resolver *sourcemap.Resolver, cache map[string]resolvedLoc, genLines map[string][]string, file string, line int) resolvedLoc {
	if resolver == nil || file == "" || line <= 0 {
		return resolvedLoc{}
	}
	key := fmt.Sprintf("%s\x00%d", file, line)
	if v, ok := cache[key]; ok {
		return v
	}
	col := firstCodeColumn(genLines, file, line)
	pos, err := resolver.Resolve(file, line, col)
	var out resolvedLoc
	if err == nil && pos.Source != "" {
		out = resolvedLoc{resolved: true, file: pos.Source, line: pos.Line, mapping: string(pos.Mapping), stale: pos.Stale}
	}
	cache[key] = out
	return out
}

// firstCodeColumn returns the 1-based column of the first non-blank
// (not space/tab) rune on the generated file's given 1-based line, reading
// (and memoizing in cache) the file at most once. Returns 0 — Resolve's own
// "no column known" query — when the file can't be read, the line is out
// of range, or the line is entirely blank, so a missing/unreadable
// generated file degrades to the old, less precise behavior instead of
// losing the resolution outright.
func firstCodeColumn(cache map[string][]string, file string, line int) int {
	lines, ok := cache[file]
	if !ok {
		lines, _ = readFileLines(file) // nil on any error; cached so a missing file isn't re-opened per line
		cache[file] = lines
	}
	if line < 1 || line > len(lines) {
		return 0
	}
	text := lines[line-1]
	for i, r := range text {
		if r != ' ' && r != '\t' {
			return i + 1
		}
	}
	return 0
}

// setInternalRangeHint stashes the function's own generated (file,
// funcLine) — the fallback range-resolution input when no line resolved
// through a source map at all — as observed start/end. resolveRanges
// overwrites StartLine/EndLine/RangeSource afterward when codemap (or a
// wider observed line span) has something better to say.
func (f *HeatFunction) setInternalRangeHint(genFile string, genLine int) {
	f.rangeHintFile = genFile
	f.rangeHintLine = genLine
}

// ---------------------------------------------------------------------------
// FromPprof: decoded pprof proto samples
// ---------------------------------------------------------------------------

// pprofFuncKey identifies one function for a pprof-sourced heatmap. Unlike
// CDP's heatFuncKey, Go function names are already globally unique — no
// V8-style anonymous-closure collision is possible — so (Func, File) alone
// is enough; there is no declaration-line disambiguator to carry.
type pprofFuncKey struct {
	Func string
	File string
}

type pprofLineAgg struct{ self, cum int64 }

type pprofFuncAccum struct {
	lines     map[int]*pprofLineAgg
	lineOrder []int
	self      int64
	cum       int64
}

// heatPprofPreferred maps a HeatProfileType to the pprof SampleType names
// (in preference order) it should be weighted by — the heatmap's own
// mapping, richer than pprofPreferredValue's (pprof.go), which has no way
// to ask for alloc_space specifically since ProfileType only has one
// undifferentiated ProfileHeap.
func heatPprofPreferred(t HeatProfileType) []string {
	switch t {
	case HeatCPU:
		return []string{"cpu", "samples"}
	case HeatHeapInuse:
		return []string{"inuse_space"}
	case HeatHeapAlloc:
		return []string{"alloc_space"}
	default:
		return nil // goroutine: single unnamed count column; pprofValueIndex's last-column fallback is exactly right.
	}
}

// buildFromPprof is FromPprof: the decoded-pprof-proto producer.
func buildFromPprof(prof *profile.Profile, opts HeatOptions) (*Heatmap, error) {
	if prof == nil {
		return nil, fmt.Errorf("heat.Build: nil pprof profile")
	}
	ptype := opts.profileType()
	valueIdx := pprofValueIndex(prof, heatPprofPreferred(ptype)...)

	funcs := map[pprofFuncKey]*pprofFuncAccum{}
	var funcOrder []pprofFuncKey
	getFunc := func(k pprofFuncKey) *pprofFuncAccum {
		a, ok := funcs[k]
		if !ok {
			a = &pprofFuncAccum{lines: map[int]*pprofLineAgg{}}
			funcs[k] = a
			funcOrder = append(funcOrder, k)
		}
		return a
	}
	lineOf := func(k pprofFuncKey, line int) *pprofLineAgg {
		a := getFunc(k)
		la, ok := a.lines[line]
		if !ok {
			la = &pprofLineAgg{}
			a.lines[line] = la
			a.lineOrder = append(a.lineOrder, line)
		}
		return la
	}

	var total int64
	for _, s := range prof.Sample {
		if valueIdx < 0 || valueIdx >= len(s.Value) {
			continue
		}
		v := s.Value[valueIdx]
		if v == 0 {
			continue
		}
		total += v

		// Flat: credited only to the leaf location's innermost inlined
		// line — mirrors symbolsFromPprof (pprof.go) exactly.
		if len(s.Location) > 0 && len(s.Location[0].Line) > 0 {
			id := pprofLineIdentity(s.Location[0].Line[0])
			k := pprofFuncKey{Func: id.Func, File: id.File}
			la := lineOf(k, id.Line)
			la.self += v
			getFunc(k).self += v
		}

		// Cum: every (func,line) on the stack, once per sample — the same
		// recursion guard symbolsFromPprof uses — PLUS a coarser,
		// function-only guard for the function-level total below (a
		// function with several of its own lines/recursive frames on one
		// stack must still only count that sample once toward its own
		// Cum, or direct/mutual recursion would inflate it past the
		// sample's own weight).
		seenLine := map[pprofFuncKey]map[int]bool{}
		seenFunc := map[pprofFuncKey]bool{}
		for _, loc := range s.Location {
			for _, ln := range loc.Line {
				id := pprofLineIdentity(ln)
				k := pprofFuncKey{Func: id.Func, File: id.File}
				if seenLine[k] == nil {
					seenLine[k] = map[int]bool{}
				}
				if !seenLine[k][id.Line] {
					seenLine[k][id.Line] = true
					lineOf(k, id.Line).cum += v
				}
				if !seenFunc[k] {
					seenFunc[k] = true
					getFunc(k).cum += v
				}
			}
		}
	}
	if total <= 0 {
		return nil, fmt.Errorf("heat.Build: pprof profile has no samples for the selected value column")
	}

	unit := "samples"
	if valueIdx >= 0 && valueIdx < len(prof.SampleType) && prof.SampleType[valueIdx].Unit != "" {
		unit = prof.SampleType[valueIdx].Unit
	}

	hm := &Heatmap{
		Schema:        HeatSchema,
		ProfileType:   ptype,
		Unit:          unit,
		Method:        MethodPprofProto,
		Runtime:       "go",
		Samples:       int(total),
		ActiveSamples: int(total), // pprof CPU/heap/goroutine profiles carry no idle/GC pseudo-bucket the way V8's CDP capture does.
	}

	for _, k := range funcOrder {
		acc := funcs[k]
		var selfPct, cumPct float64
		if total > 0 {
			selfPct = float64(acc.self) / float64(total) * 100
			cumPct = float64(acc.cum) / float64(total) * 100
		}
		f := HeatFunction{Name: k.Func, File: k.File, SelfPct: selfPct, CumPct: cumPct}

		sort.Ints(acc.lineOrder)
		normBy := acc.self
		if normBy <= 0 {
			normBy = acc.cum // a pure wrapper with zero self: normalize the (still-informative) cum share instead.
		}
		for _, line := range acc.lineOrder {
			la := acc.lines[line]
			hl := HeatLine{Line: line, Self: la.self, Cum: la.cum}
			if normBy > 0 {
				basis := la.self
				if acc.self <= 0 {
					basis = la.cum
				}
				hl.PctOfFunction = float64(basis) / float64(normBy) * 100
			}
			f.Lines = append(f.Lines, hl)
		}

		var repLine int
		if len(acc.lineOrder) > 0 {
			repLine = acc.lineOrder[0]
		}
		f.setInternalRangeHint(k.File, repLine)
		hm.Functions = append(hm.Functions, f)
	}
	return hm, nil
}

// ---------------------------------------------------------------------------
// Shared post-processing: ranges, code, sorting, warnings
// ---------------------------------------------------------------------------

// codemapRangeCache dedupes symbol-at calls within one BuildHeatmap run:
// several functions in the same file (or the same function looked up via
// more than one representative line) would otherwise each pay a codemap
// subprocess round trip.
type codemapRangeCache struct {
	entries map[string]ecosystem.SymbolAt
	errs    map[string]error
}

func resolveRanges(ctx context.Context, hm *Heatmap, opts HeatOptions) {
	codemapOK := !opts.NoCodemap && ecosystem.CodemapAvailable()
	if codemapOK {
		health := ecosystem.ProbeCodemap(ctx, opts.Codebase)
		codemapOK = health.State == ecosystem.HealthOK
	}
	cache := &codemapRangeCache{entries: map[string]ecosystem.SymbolAt{}, errs: map[string]error{}}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	for i := range hm.Functions {
		f := &hm.Functions[i]
		file, line := f.rangeHintFile, f.rangeHintLine
		if len(f.Lines) > 0 {
			file, line = f.File, f.Lines[0].Line
		}
		if codemapOK && cctx.Err() == nil && file != "" && line > 0 {
			sym, err := lookupCodemapRange(cctx, cache, file, line, opts.Codebase)
			if err == nil && sym.Resolution != "" && sym.Resolution != "none" && sym.StartLine > 0 {
				f.StartLine, f.EndLine, f.RangeSource = sym.StartLine, sym.EndLine, "codemap"
				continue
			}
		}
		// Observed fallback: the min/max line actually seen for this
		// function, or (for a function with zero attributed lines — an
		// ancestor whose only signal is its callees) the single generated
		// declaration line as both start and end, honestly marked observed
		// rather than left as a fabricated range.
		if len(f.Lines) > 0 {
			f.StartLine, f.EndLine = f.Lines[0].Line, f.Lines[len(f.Lines)-1].Line
		} else if line > 0 {
			f.StartLine, f.EndLine = line, line
		}
		if f.StartLine > 0 {
			f.RangeSource = "observed"
		}
	}
}

func lookupCodemapRange(ctx context.Context, cache *codemapRangeCache, file string, line int, codebase string) (ecosystem.SymbolAt, error) {
	key := fmt.Sprintf("%s\x00%d", file, line)
	if sym, ok := cache.entries[key]; ok {
		return sym, nil
	}
	if err, ok := cache.errs[key]; ok {
		return ecosystem.SymbolAt{}, err
	}
	sym, err := ecosystem.CodemapSymbolAtPath(ctx, file, line, ecosystem.CodemapOpts{Path: codebase})
	if err != nil {
		cache.errs[key] = err
		return ecosystem.SymbolAt{}, err
	}
	cache.entries[key] = sym
	return sym, nil
}

// readCode fills HeatLine.Code from disk, one open+scan per distinct file
// (never per line). A missing file, an out-of-range line, or any read
// error just leaves Code empty — per AC-5, a profile taken against a
// binary that has since moved or been rebuilt must still render, honestly
// missing only the source snippet, not the whole heatmap.
func readCode(hm *Heatmap) {
	cache := map[string][]string{}
	loaded := map[string]bool{}
	for fi := range hm.Functions {
		f := &hm.Functions[fi]
		if f.File == "" {
			continue
		}
		if !loaded[f.File] {
			loaded[f.File] = true
			if lines, err := readFileLines(f.File); err == nil {
				cache[f.File] = lines
			}
		}
		lines := cache[f.File]
		if lines == nil {
			continue
		}
		for li := range f.Lines {
			l := &f.Lines[li]
			if l.Line >= 1 && l.Line <= len(lines) {
				l.Code = lines[l.Line-1]
			}
		}
	}
}

func readFileLines(path string) ([]string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var lines []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// finalizeFunctions applies the Func filter, sorts by Cum% desc / Self%
// desc / Name, and caps at Top — the same shape order the roadmap's
// TOTAL/SELF/FUNCTION table renders in (sorted by TOTAL=cum, a wrapper's
// near-zero self doesn't bury it below its own callee).
func finalizeFunctions(hm *Heatmap, opts HeatOptions) {
	fns := hm.Functions
	if opts.Func != "" {
		filtered := fns[:0:0]
		for _, f := range fns {
			if f.Name == opts.Func {
				filtered = append(filtered, f)
			}
		}
		fns = filtered
	}
	sort.SliceStable(fns, func(i, j int) bool {
		if fns[i].CumPct != fns[j].CumPct {
			return fns[i].CumPct > fns[j].CumPct
		}
		if fns[i].SelfPct != fns[j].SelfPct {
			return fns[i].SelfPct > fns[j].SelfPct
		}
		return fns[i].Name < fns[j].Name
	})
	if top := opts.top(); len(fns) > top {
		fns = fns[:top]
	}
	hm.Functions = fns
}

// addWarnings appends AC-5's two honesty warnings when the data calls for
// them: idle_pct past idleWarningThresholdPct (CPU heat can't explain
// off-CPU slowness) and a top function whose own self share is under
// diffuseWarningThresholdPct (no single line dominates enough to call it
// "the" hot line).
func addWarnings(hm *Heatmap) {
	if hm.IdlePct > idleWarningThresholdPct {
		hm.Warnings = append(hm.Warnings, "mostly idle: slowness is off-CPU")
	}
	if len(hm.Functions) == 0 {
		return
	}
	top := hm.Functions[0]
	basis := top.SelfPct
	if basis <= 0 {
		basis = top.CumPct
	}
	if basis < diffuseWarningThresholdPct {
		hm.Warnings = append(hm.Warnings, fmt.Sprintf(
			"diffuse: the hottest function (%s) has only %.1f%% of active samples; this profile may be too spread out to point at one line",
			nonEmpty(top.Name, "(unknown)"), basis))
	}
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
