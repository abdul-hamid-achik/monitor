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
	// MethodV8PositionTicks means every FUNCTION this heatmap reports (i.e.
	// every node that got its own Functions row — excluding pseudo-frames
	// like (idle), runtime bootstrap frames, and location-less native
	// builtins such as Bun's own JSON.stringify, none of which carry
	// positionTicks and none of which are reported as a function; see
	// buildFromCDP) carried real positionTicks wherever it contributed
	// self time: the reported hot line is a genuine statement-level
	// attribution.
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
	// MethodDarwinSample means the profile came from a macOS `sample <pid>`
	// capture (see BuildHeatmapFromSample): FUNCTION-LEVEL only — macOS
	// `sample` carries no per-line detail at all (no positionTicks, no
	// pprof line table), only a self/cum weight per function name, so a
	// Heatmap built this way always has empty Lines/StartLine/EndLine on
	// every function. It exists so a target `hot` otherwise has no working
	// CPU capture path for at all — Python, Ruby, or a Go binary with no
	// reachable pprof endpoint — still gets a real, honestly-labeled
	// answer on macOS instead of a dead end (AC-1's own documented
	// requirement for the `sample` producer).
	MethodDarwinSample HeatMethod = "darwin_sample"
)

// sampleFunctionLevelOnlyWarning is BuildHeatmapFromSample's own honesty
// disclosure, appended to every Heatmap it produces: macOS `sample` has no
// source-line detail at all (see MethodDarwinSample), so a caller (`monitor
// hot`) must not render this the way it renders a real per-line CodeFrame —
// the warning is what tells it (and any JSON/MCP consumer) so.
const sampleFunctionLevelOnlyWarning = "function-level only: macOS `sample` reports functions, not source lines — no positionTicks or pprof proto is available for this target"

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

	// DefaultTarget is the function CodeFrame renders by default when the
	// caller (monitor hot) is given no --func: whichever function has the
	// highest SELF share, computed from the FULL function list BEFORE
	// --func/--top filter/cap Functions. Populated by addWarnings, on the
	// same full list its own "diffuse" check reads, so a small --top can
	// never silently swap in some other, cooler function's CodeFrame (or a
	// false "diffuse" warning) just because it truncated the real hot one
	// out of Functions. nil only when the profile produced zero functions
	// at all. Excluded from JSON (json:"-") — the monitor.line_heatmap.v1
	// schema doesn't define it; it duplicates one entry of Functions.
	DefaultTarget *HeatFunction `json:"-"`
	// IdleMeasured is true when IdlePct reflects a real off-CPU share this
	// package actually computed — always true for a CDP source (which
	// always carries an (idle) pseudo-frame bucket, even at 0%), and true
	// for a pprof CPU source only when the proto's own DurationNanos let
	// IdlePct be derived from wall-clock time vs. CPU time consumed. false
	// for a pprof heap/goroutine source (an instant snapshot has no
	// "idle" concept) or a CPU pprof proto with no DurationNanos at all —
	// a caller must not print "idle 0%" in that case, since 0 there means
	// "not measured", not "fully utilized". Excluded from JSON: it's a
	// rendering hint, not part of the monitor.line_heatmap.v1 schema.
	IdleMeasured bool `json:"-"`
	// CaptureDurationNanos is the real wall-clock span this profile
	// covers: EndTime-StartTime (converted from V8's microseconds) for a
	// CDP source, or the pprof proto's own DurationNanos for a pprof
	// source. 0 when unknown (a CDP profile with no start/end times, or a
	// pprof source that doesn't set DurationNanos, e.g. most heap/
	// goroutine snapshots) — a caller must not print a duration in that
	// case. Excluded from JSON: display-only, not part of the
	// monitor.line_heatmap.v1 schema.
	CaptureDurationNanos int64 `json:"-"`
}

// MostlyIdle reports whether this Heatmap's IdlePct crossed AC-5's honesty
// threshold for "CPU heat can't explain this target's slowness" (the same
// threshold addWarnings' own "mostly idle" warning uses). A caller
// (monitor hot) uses this to decide whether rendering a confident-looking
// CodeFrame on what's mostly sampler noise would be dishonest.
func (hm *Heatmap) MostlyIdle() bool {
	return hm != nil && hm.IdlePct > idleWarningThresholdPct
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
	// addWarnings must run BEFORE finalizeFunctions: it picks
	// DefaultTarget and computes the "diffuse" check from the FULL
	// function list. Running it after --func/--top already filtered and
	// capped Functions would judge "how spread out is this profile" (and
	// pick monitor hot's default CodeFrame target) from whatever sliver of
	// functions survived filtering, not the real profile.
	addWarnings(hm)
	finalizeFunctions(hm, opts)
	return hm, nil
}

// BuildHeatmapFromSample builds a Heatmap from an already-captured macOS
// `sample <pid>` Profile (profiler.Profile.Symbols, from parseSampleTree) —
// the AC-1/AC-5 "darwin sample fallback" for a target with no working CDP
// or pprof capture path at all (Python, Ruby, an unlinked Go binary, or any
// process whose pprof endpoint isn't owned/explicit). Unlike BuildHeatmap,
// this is NOT dispatched from a Source/SourceKind: `sample`'s own call-graph
// text carries no file:line at all (see internal/profiler/sample_parse.go's
// own doc comment — Symbol.Line is always 0 here), so there is no
// SourceSample kind for it to join, and no per-line model to build —
// FUNCTION-LEVEL ONLY, honestly disclosed via MethodDarwinSample and
// sampleFunctionLevelOnlyWarning rather than silently degrading into a
// misleadingly empty-but-otherwise-normal-looking per-line Heatmap.
//
// A caller renders this exactly like any other Heatmap (the same CodeFrame,
// table, and --json shapes `monitor hot` always uses): every HeatFunction's
// Lines stays empty (CodeFrame.Render already handles that honestly, with
// "(no lines to show)"), and StartLine/EndLine/RangeSource stay unset (0/"")
// since there is no line to anchor a range to.
func BuildHeatmapFromSample(ctx context.Context, prof Profile, opts HeatOptions) (*Heatmap, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(prof.Symbols) == 0 {
		return nil, fmt.Errorf("heat.Build: this sample capture has no symbols to build a heatmap from")
	}

	runtime := opts.Runtime
	if runtime == "" {
		runtime = "unknown"
	}
	hm := &Heatmap{
		Schema:      HeatSchema,
		ProfileType: HeatCPU, // `sample` only ever measures CPU; there is no heap/goroutine sample capture.
		Unit:        "samples",
		Method:      MethodDarwinSample,
		Runtime:     runtime,
	}
	if prof.Stats != nil {
		hm.Samples = prof.Stats.Samples
		hm.ActiveSamples = prof.Stats.ActiveSamples
		hm.IdlePct = prof.Stats.IdlePct
		hm.GCPct = prof.Stats.GCPct
		hm.IdleMeasured = true
	} else {
		// parseSampleTree only omits Stats when it found nothing at all
		// (allTotal<=0) — which would already have left prof.Symbols empty
		// too (see its own early return), so this branch is defensive only.
		hm.Samples = len(prof.Symbols)
		hm.ActiveSamples = hm.Samples
	}

	for _, sym := range prof.Symbols {
		hm.Functions = append(hm.Functions, HeatFunction{
			Name: sym.Func,
			// File is `sample`'s own "image" (containing binary/dylib) —
			// the closest thing to a location this format has, never a
			// real source path — surfaced honestly as the table's
			// LOCATION column would otherwise be blank.
			File:    sym.File,
			SelfPct: sym.Weight,
			CumPct:  sym.Cum,
		})
	}

	resolveRanges(ctx, hm, opts) // no-op here (no rangeHintLine ever set), kept for pipeline symmetry with BuildHeatmap.
	if !opts.NoReadCode {
		readCode(hm) // no-op here too (every function's Lines is empty).
	}
	addWarnings(hm)
	hm.Warnings = append(hm.Warnings, sampleFunctionLevelOnlyWarning)
	finalizeFunctions(hm, opts)
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

// isRuntimeInternalCDPFile reports whether file names a JS runtime's own
// bootstrap/module-loader machinery rather than user code: Node's
// "node:internal/..." built-in module specifiers, and the analogous
// "ext:"/"deno:" schemes Deno's isolate uses for the same purpose. See the
// call site in buildFromCDP for why these are excluded from Functions
// while still counting toward Stats.ActiveSamples.
func isRuntimeInternalCDPFile(file string) bool {
	for _, prefix := range [...]string{"node:", "ext:", "deno:"} {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return false
}

// deriveCDPHits returns each node's sample count, keyed by node ID, reading
// straight from the node's own hitCount when at least one node carries a
// positive one. Falls back to counting each node id's occurrences in the
// profile's flat per-tick samples array — the same source hitCount is
// itself normally derived from — when every node's hitCount is zero: the
// brief describes a .cpuprofile as "JSON with nodes/samples/timeDeltas,
// positionTicks", and hitCount is genuinely optional in CDP's own
// ProfileNode shape, so a capture that has samples but omits hitCount must
// still produce a usable heatmap instead of reporting "no samples".
func deriveCDPHits(cp *cdpProfile) map[int64]int64 {
	hits := make(map[int64]int64, len(cp.Nodes))
	for i := range cp.Nodes {
		if cp.Nodes[i].HitCount > 0 {
			for j := range cp.Nodes {
				hits[cp.Nodes[j].ID] = cp.Nodes[j].HitCount
			}
			return hits
		}
	}
	for _, id := range cp.Samples {
		hits[id]++
	}
	return hits
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

	// hits gives each node's sample count. hitCount is optional in CDP's
	// own ProfileNode shape (only samples/timeDeltas are guaranteed); when
	// every node's hitCount is missing/zero, derive it by counting each
	// node id's occurrences in the flat per-tick samples array instead —
	// the same source hitCount is itself normally derived from — rather
	// than reporting "no samples" for a .cpuprofile that legitimately has
	// some.
	hits := deriveCDPHits(cp)

	// Pass 1: per-node self attribution, mirroring flattenCDPProfile's own
	// rules exactly (inspector.go) — positionTicks when present, else the
	// node's own declaration line with its whole hit count — plus the same
	// idle/gc/program/root pseudo-frame bookkeeping, so a live CDP capture
	// and a file-loaded one report identical Stats for the same data.
	nodeFunc := make(map[int64]heatFuncKey, len(cp.Nodes)) // absent entry marks a pseudo/excluded/unattributed node
	var totalHits, idleHits, gcHits, excludedHits int64
	anyPositionTicks := false
	for i := range cp.Nodes {
		n := &cp.Nodes[i]
		hc := hits[n.ID]
		totalHits += hc
		fn := n.CallFrame.FunctionName
		if fn == "" {
			fn = "(anonymous)"
		}
		if isPseudoCDPFrame(fn) {
			excludedHits += hc
			switch fn {
			case "(idle)":
				idleHits += hc
			case "(garbage collector)":
				gcHits += hc
			}
			continue
		}
		file := decodeCDPFileURL(n.CallFrame.URL)
		if isRuntimeInternalCDPFile(file) {
			// Real code, real CPU time (already counted in totalHits
			// above), but not a "function" worth its own row: Node's CJS
			// loader wraps every entry point in several such frames (see
			// testdata/v8-hot.cpuprofile — module require plumbing sits
			// directly on the path to heavyStringify), and since every
			// sample's stack passes through them, including them would
			// bury real user functions under a wall of zero-self
			// "(anonymous)" bootstrap wrappers. nodeFunc simply has no
			// entry for this node's ID; pass 3's tree walk still descends
			// into its children unconditionally, so a real function
			// further down the stack is unaffected.
			continue
		}
		funcLine := int(n.CallFrame.LineNumber) + 1
		k := heatFuncKey{Func: fn, File: file, FuncLine: funcLine}
		// nodeFunc is still set for a native builtin with no url and
		// lineNumber -1 (Bun's own JSON.stringify/String.prototype.repeat
		// and similar) so pass 3 can list it as a CALLEE of whichever
		// function invoked it — its real CPU cost (already counted toward
		// totalHits/active above) shouldn't just vanish. It never gets a
		// Functions row of its own, though (see the funcOrder emission
		// loop below): there is no real source location to report, and
		// giving it one would fabricate "line 0 of an empty file" as a
		// real, rankable location.
		nodeFunc[n.ID] = k
		acc := getFunc(k)
		if file == "" && funcLine <= 0 {
			continue
		}
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
		if hc > 0 {
			acc.addSelf(funcLine, hc)
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
		total := hits[id]
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
		// A CDP capture always carries an (idle) pseudo-frame bucket, even
		// at 0% — unlike a pprof CPU proto, which only measures idle when
		// it happens to carry a DurationNanos (see buildFromPprof) — so
		// IdlePct here is always a real measurement, never "not measured".
		IdleMeasured: true,
	}
	if totalHits > 0 {
		hm.IdlePct = float64(idleHits) / float64(totalHits) * 100
		hm.GCPct = float64(gcHits) / float64(totalHits) * 100
	}
	if cp.EndTime > cp.StartTime {
		hm.CaptureDurationNanos = int64((cp.EndTime - cp.StartTime) * 1000) // V8 times are microseconds
	}

	resolver := opts.Sourcemaps
	if resolver == nil && !opts.NoSourcemaps {
		resolver = sourcemap.NewResolver()
	}
	smCache := map[string]resolvedLoc{}
	genLines := map[string][]string{}

	// staleFiles collects, in first-seen order, every generated file whose
	// source map resolved at least one line as stale — deduped so a hot
	// function with many stale lines gets one warning, not one per line.
	var staleFiles []string
	seenStale := map[string]bool{}
	// spanAmbiguousFiles is staleFiles' mirror for resolveCDPLoc's span-
	// ambiguity downgrade (see resolvedLoc.spanAmbiguous): one warning per
	// generated file, not one per downgraded line.
	var spanAmbiguousFiles []string
	seenSpanAmbiguous := map[string]bool{}

	for _, k := range funcOrder {
		if k.File == "" && k.FuncLine <= 0 {
			// A native builtin (see above): no Functions row of its own —
			// it already appears in its caller's Callees, via addCallee
			// below, keyed by this same heatFuncKey.
			continue
		}
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

		f := HeatFunction{Name: k.Func, File: k.File, SelfPct: selfPct, CumPct: cumPct}
		sort.Ints(acc.lineOrder)

		// Resolve every line up front, BEFORE deciding the function's own
		// File: a real build can map a function's body while leaving its
		// own declaration line unmapped (a bare comment, or a line the
		// bundler dropped), so trusting only the declaration line's own
		// resolution would leave File pointing at the generated file while
		// every line number underneath it is already in original
		// coordinates — the wrong file paired with the right-looking line.
		lineResolved := make([]resolvedLoc, len(acc.lineOrder))
		for i, line := range acc.lineOrder {
			lineResolved[i] = resolveCDPLoc(resolver, smCache, genLines, k.File, line)
		}
		declRL := resolveCDPLoc(resolver, smCache, genLines, k.File, k.FuncLine)

		rangeFile, rangeLine := k.File, k.FuncLine
		switch {
		case declRL.resolved:
			f.File, rangeFile, rangeLine = declRL.file, declRL.file, declRL.line
		default:
			for _, rl := range lineResolved {
				if rl.resolved {
					f.File, rangeFile, rangeLine = rl.file, rl.file, rl.line
					break
				}
			}
		}
		f.setInternalRangeHint(rangeFile, rangeLine)

		// rawLines carries one entry per GENERATED line before merging: a
		// source map that compresses several generated lines onto one
		// original line (common in a minified or one-statement-per-line
		// bundle) would otherwise produce several HeatLines that all claim
		// the same Line number instead of one properly summed row.
		rawLines := make([]HeatLine, 0, len(acc.lineOrder))
		for i, line := range acc.lineOrder {
			self := acc.lineSelf[line]
			outLine, mapping, stale := line, "", false
			// Only trust a line's own resolution when it lands in the
			// SAME resolved file as the function itself: HeatLine has no
			// File of its own (lines nest under HeatFunction.File — see
			// the schema doc), so a line that maps somewhere else
			// entirely is kept in generated coordinates instead of
			// pairing the wrong file with the wrong line number.
			if rl := lineResolved[i]; rl.resolved && rl.file == f.File {
				outLine, mapping, stale = rl.line, rl.mapping, rl.stale
				if rl.spanAmbiguous && k.File != "" && !seenSpanAmbiguous[k.File] {
					seenSpanAmbiguous[k.File] = true
					spanAmbiguousFiles = append(spanAmbiguousFiles, k.File)
				}
			}
			if stale && f.File != "" && !seenStale[f.File] {
				seenStale[f.File] = true
				staleFiles = append(staleFiles, f.File)
			}
			rawLines = append(rawLines, HeatLine{Line: outLine, Self: self, Cum: self, Mapping: mapping, Stale: stale})
		}
		f.Lines = mergeHeatLines(rawLines, selfTotal)

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
	for _, file := range spanAmbiguousFiles {
		hm.Warnings = append(hm.Warnings, fmt.Sprintf(
			"one-line/minified bundle (%s): V8 positionTicks carry no column; per-line attribution not possible, only ambiguous", file))
	}
	for _, file := range staleFiles {
		hm.Warnings = append(hm.Warnings, fmt.Sprintf("source map older than %s: resolved lines may be wrong", file))
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

// mergeHeatLines groups raw, possibly-duplicate lines — several GENERATED
// lines that resolved to the same ORIGINAL line — by their final Line
// number: summing Self/Cum, keeping the LEAST-certain Mapping of the group
// (claiming the group's best-case certainty would overstate how precisely
// it was actually located), and OR-ing Stale. Returns them sorted by Line
// ascending, which resolveRanges' observed-range fallback (min/max of
// Lines) and a human reading the CodeFrame top to bottom both assume. A
// no-source-map source (every raw Line already unique, one per generated
// line) merges to a no-op, byte-for-byte the same as before.
func mergeHeatLines(raw []HeatLine, selfTotal int64) []HeatLine {
	type agg struct {
		self, cum int64
		mapping   string
		stale     bool
	}
	byLine := map[int]*agg{}
	var order []int
	for _, l := range raw {
		a, ok := byLine[l.Line]
		if !ok {
			a = &agg{mapping: l.Mapping, stale: l.Stale}
			byLine[l.Line] = a
			order = append(order, l.Line)
		} else {
			a.mapping = leastCertainMapping(a.mapping, l.Mapping)
			a.stale = a.stale || l.Stale
		}
		a.self += l.Self
		a.cum += l.Cum
	}
	sort.Ints(order)
	out := make([]HeatLine, 0, len(order))
	for _, line := range order {
		a := byLine[line]
		hl := HeatLine{Line: line, Self: a.self, Cum: a.cum, Mapping: a.mapping, Stale: a.stale}
		if selfTotal > 0 {
			hl.PctOfFunction = float64(a.self) / float64(selfTotal) * 100
		}
		out = append(out, hl)
	}
	return out
}

// mappingRank orders Mapping confidence from least to most certain: ""
// (generated coordinates; no source map applies) < "ambiguous" < "exact" —
// the same three outcomes internal/sourcemap.Resolver (plus this package's
// own span-ambiguity downgrade, see resolveCDPLoc) ever produces here.
func mappingRank(m string) int {
	switch m {
	case "exact":
		return 2
	case "ambiguous":
		return 1
	default:
		return 0
	}
}

// leastCertainMapping returns whichever of a, b is the LESS certain
// mapping, per mappingRank.
func leastCertainMapping(a, b string) string {
	if mappingRank(b) < mappingRank(a) {
		return b
	}
	return a
}

// resolvedLoc is the (possibly unchanged) result of trying to resolve one
// generated (file,line) through a source map.
type resolvedLoc struct {
	resolved bool
	file     string
	line     int
	mapping  string
	stale    bool
	// spanAmbiguous is true only when resolveCDPLoc itself downgraded an
	// otherwise-"exact" resolver result to "ambiguous" because the
	// generated line's segments span more than one original line (see
	// resolveCDPLoc) — distinct from the resolver returning "ambiguous" on
	// its own, so buildFromCDP can warn specifically about the ONE-LINE/
	// MINIFIED-BUNDLE case rather than a merely-uncertain-but-real mapping.
	spanAmbiguous bool
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
		mapping := string(pos.Mapping)
		spanAmbiguous := false
		// A generated line whose segments span MORE than one original
		// line — a minified or one-statement-per-line bundle — can't
		// honestly be called "exact" attribution just because col landed
		// on a real segment: V8 positionTicks carry no column at all, so
		// col here is itself only a guess (see above). Re-querying the
		// SAME generated line at its LAST non-blank column and comparing
		// catches this: when the far end of the line resolves to a
		// different original (file, line) than the near end did, the
		// whole line's attribution is ambiguous, not exact — verified
		// live against a `bun build --minify` output, which puts an
		// entire loop body on one generated line.
		if endCol := lastCodeColumn(genLines, file, line); endCol > col {
			if endPos, endErr := resolver.Resolve(file, line, endCol); endErr == nil && endPos.Source != "" {
				if endPos.Source != pos.Source || endPos.Line != pos.Line {
					mapping = string(sourcemap.MappingAmbiguous)
					spanAmbiguous = true
				}
			}
		}
		out = resolvedLoc{resolved: true, file: pos.Source, line: pos.Line, mapping: mapping, stale: pos.Stale, spanAmbiguous: spanAmbiguous}
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

// lastCodeColumn is firstCodeColumn's mirror: the 1-based column of the
// LAST non-blank (not space/tab) rune on the generated file's given
// 1-based line. Used alongside firstCodeColumn to detect a generated line
// whose mapped segments span more than one original line — see
// resolveCDPLoc's span-ambiguity check.
func lastCodeColumn(cache map[string][]string, file string, line int) int {
	lines, ok := cache[file]
	if !ok {
		lines, _ = readFileLines(file)
		cache[file] = lines
	}
	if line < 1 || line > len(lines) {
		return 0
	}
	text := []rune(lines[line-1])
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] != ' ' && text[i] != '\t' {
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

// findPprofSampleType returns the index of prof's first SampleType whose
// Type matches name case-insensitively.
func findPprofSampleType(prof *profile.Profile, name string) (int, bool) {
	for i, st := range prof.SampleType {
		if strings.EqualFold(st.Type, name) {
			return i, true
		}
	}
	return 0, false
}

// pprofLooksLikeCPUOrHeap reports whether prof carries a SampleType this
// package recognizes, by name, as CPU or heap data.
func pprofLooksLikeCPUOrHeap(prof *profile.Profile) bool {
	for _, name := range [...]string{"cpu", "samples", "inuse_space", "alloc_space", "inuse_objects", "alloc_objects"} {
		if _, ok := findPprofSampleType(prof, name); ok {
			return true
		}
	}
	return false
}

// heatFindValueIndex resolves an EXPLICITLY requested HeatProfileType
// (monitor hot --type) to a real sample-value column, or reports that it
// couldn't. Unlike pprof.go's pprofValueIndex — built for `monitor
// profile`'s unconditional flat summary, where falling back to the last
// column is the right default — this never silently substitutes a
// different column for a request that named a specific one: doing so would
// mislabel the heatmap's own profile_type/unit fields as something the
// profile never measured (see the E3.5 review).
func heatFindValueIndex(prof *profile.Profile, want HeatProfileType) (int, bool) {
	switch want {
	case HeatHeapInuse:
		return findPprofSampleType(prof, "inuse_space")
	case HeatHeapAlloc:
		return findPprofSampleType(prof, "alloc_space")
	case HeatCPU:
		if idx, ok := findPprofSampleType(prof, "cpu"); ok {
			return idx, true
		}
		return findPprofSampleType(prof, "samples")
	case HeatGoroutine:
		if idx, ok := findPprofSampleType(prof, "goroutine"); ok {
			return idx, true
		}
		// No column is literally named "goroutine" — some Go versions'
		// writers emit a single unnamed count column instead. Accept that
		// shape only when the profile carries no cpu/heap signature at
		// all, so `--type goroutine` against an actual cpu or heap
		// capture still fails instead of silently reporting whatever
		// column happens to be last.
		if !pprofLooksLikeCPUOrHeap(prof) && len(prof.SampleType) > 0 {
			return len(prof.SampleType) - 1, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// detectPprofProfileType picks a HeatProfileType and its sample-value
// column from the profile's OWN SampleType names, for the common case
// where the caller (monitor hot with no --type) didn't ask for one
// explicitly — auto-detecting what the profile actually measured instead
// of hard-defaulting to "cpu" regardless of what was captured, which used
// to mislabel a heap or goroutine .pb.gz as a cpu one (see the E3.5
// review).
func detectPprofProfileType(prof *profile.Profile) (HeatProfileType, int) {
	if idx, ok := findPprofSampleType(prof, "inuse_space"); ok {
		return HeatHeapInuse, idx
	}
	if idx, ok := findPprofSampleType(prof, "alloc_space"); ok {
		return HeatHeapAlloc, idx
	}
	if idx, ok := findPprofSampleType(prof, "cpu"); ok {
		return HeatCPU, idx
	}
	if idx, ok := findPprofSampleType(prof, "samples"); ok {
		return HeatCPU, idx
	}
	if idx, ok := findPprofSampleType(prof, "goroutine"); ok {
		return HeatGoroutine, idx
	}
	if len(prof.SampleType) == 1 {
		// A single, unnamed count column with none of the signatures
		// above: Go's own convention for a goroutine/threadcreate-shaped
		// snapshot profile.
		return HeatGoroutine, 0
	}
	// Genuinely unrecognized shape: fall back to pprof's own "last column"
	// display convention rather than refusing outright.
	idx := 0
	if n := len(prof.SampleType); n > 0 {
		idx = n - 1
	}
	return HeatCPU, idx
}

// describePprofSampleTypes renders prof's SampleType columns for an error
// message, e.g. "cpu/nanoseconds, samples/count".
func describePprofSampleTypes(prof *profile.Profile) string {
	if len(prof.SampleType) == 0 {
		return "(none)"
	}
	parts := make([]string, len(prof.SampleType))
	for i, st := range prof.SampleType {
		parts[i] = fmt.Sprintf("%s/%s", st.Type, st.Unit)
	}
	return strings.Join(parts, ", ")
}

// buildFromPprof is FromPprof: the decoded-pprof-proto producer.
func buildFromPprof(prof *profile.Profile, opts HeatOptions) (*Heatmap, error) {
	if prof == nil {
		return nil, fmt.Errorf("heat.Build: nil pprof profile")
	}
	var ptype HeatProfileType
	var valueIdx int
	if opts.ProfileType != "" {
		idx, ok := heatFindValueIndex(prof, opts.ProfileType)
		if !ok {
			return nil, fmt.Errorf("heat.Build: this pprof profile has no %s sample column (available: %s)",
				opts.ProfileType, describePprofSampleTypes(prof))
		}
		ptype, valueIdx = opts.ProfileType, idx
	} else {
		ptype, valueIdx = detectPprofProfileType(prof)
	}

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
	// A CPU proto with a real capture DURATION but zero samples in the
	// selected column is an honestly, fully idle target — not an error:
	// AC-5 says point at nothing (with an idle warning) rather than refuse
	// to render at all, the same rule a mostly-idle CDP capture already
	// gets.
	idleCapture := ptype == HeatCPU && prof.DurationNanos > 0
	if total <= 0 && !idleCapture {
		return nil, fmt.Errorf("heat.Build: pprof profile has no samples for the selected value column")
	}

	unit := "samples"
	if valueIdx >= 0 && valueIdx < len(prof.SampleType) && prof.SampleType[valueIdx].Unit != "" {
		unit = prof.SampleType[valueIdx].Unit
	}

	hm := &Heatmap{
		Schema:      HeatSchema,
		ProfileType: ptype,
		Unit:        unit,
		Method:      MethodPprofProto,
		Runtime:     "go",
		// Samples/ActiveSamples default to the raw value-column total —
		// right for heap/goroutine (instant snapshots with no idle
		// concept) — and are overwritten below for a CPU capture whose
		// real wall-clock duration lets an honest idle share be computed.
		Samples:       int(total),
		ActiveSamples: int(total),
	}
	if prof.DurationNanos > 0 {
		hm.CaptureDurationNanos = prof.DurationNanos
	}
	if idleCapture {
		hm.IdleMeasured = true
		dur := prof.DurationNanos
		activeNanos := total
		if activeNanos > dur {
			// Multi-core CPU time can exceed one wall-clock window's
			// worth of nanoseconds; report full utilization instead of a
			// nonsensical negative idle share.
			activeNanos = dur
		}
		hm.Samples = int(dur)
		hm.ActiveSamples = int(activeNanos)
		if dur > 0 {
			hm.IdlePct = float64(dur-activeNanos) / float64(dur) * 100
		}
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
				l.Code = truncateCode(lines[l.Line-1])
			}
		}
	}
}

// maxHeatLineCodeRunes caps HeatLine.Code so one pathological source line —
// a minified bundle's whole body on one line, seen for real against a `bun
// build --minify` fixture — can't blow up the JSON document's size (this
// matters for the E3.6 MCP payload budget, which the whole heatmap has to
// fit inside).
const maxHeatLineCodeRunes = 240

// truncateCode caps code at maxHeatLineCodeRunes runes, an ellipsis marking
// a truncation so a consumer can tell "this is the whole line" from "this
// was cut off" — never silently swallowing bytes past the cap.
func truncateCode(code string) string {
	r := []rune(code)
	if len(r) <= maxHeatLineCodeRunes {
		return code
	}
	return string(r[:maxHeatLineCodeRunes]) + "…"
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
	// "The hottest function" here must mean the same thing `monitor hot`
	// actually renders as its default CodeFrame target — the highest SELF
	// share, not Functions[0] (sorted by CUM, so a near-zero-self wrapper
	// that merely calls a genuinely hot function would otherwise trigger a
	// false "diffuse" warning on data that isn't diffuse at all).
	top := hm.Functions[0]
	for _, f := range hm.Functions[1:] {
		if f.SelfPct > top.SelfPct {
			top = f
		}
	}
	// Stash it as DefaultTarget too: this runs on the FULL, pre-filter/cap
	// list (see BuildHeatmap's ordering comment), so a caller (monitor hot
	// with no --func) that reads DefaultTarget instead of re-deriving
	// "hottest" from the already-filtered/capped Functions gets the real
	// answer even when --top truncated the real hot function out of the
	// visible table.
	topCopy := top
	hm.DefaultTarget = &topCopy
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
