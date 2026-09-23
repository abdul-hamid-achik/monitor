package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

const v8HotFixture = "../profiler/testdata/v8-hot.cpuprofile"

func TestHotFileHumanOutputNamesHotLine(t *testing.T) {
	cmd := newHotCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "heavyStringify") {
		t.Errorf("output missing heavyStringify:\n%s", text)
	}
	if !strings.Contains(text, "> 5 |") && !strings.Contains(text, ">  5 |") {
		t.Errorf("output missing a '>' marker on line 5:\n%s", text)
	}
	if !strings.Contains(text, "method: v8 positionTicks") {
		t.Errorf("output missing the human method label:\n%s", text)
	}
	if !strings.Contains(text, "next  monitor hot --file") {
		t.Errorf("output missing a next line:\n%s", text)
	}
}

// TestHotFileHumanOutputShowsActiveIdleGCProgramBreakdown asserts the
// header's active/idle clause adds up: v8-hot.cpuprofile carries real (idle)
// and (garbage collector) pseudo-frame time (see testdata/README.md), so
// the header must show gc explicitly, not just idle, when gc is nonzero.
func TestHotFileHumanOutputShowsActiveIdleGCProgramBreakdown(t *testing.T) {
	cmd := newHotCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	header := strings.SplitN(out.String(), "\n", 2)[0]
	if !strings.Contains(header, "gc") {
		t.Errorf("header = %q, want a gc%% clause (this fixture has real gc ticks)", header)
	}
}

// TestHotFileDefaultTargetIgnoresTopCap is the CLI-level regression for the
// review's finding: with --top 1 capping the visible table to one (possibly
// cooler) function, the CodeFrame must still target the real hottest-by-self
// function across the WHOLE profile (Heatmap.DefaultTarget), not whatever
// survived the cap.
func TestHotFileDefaultTargetIgnoresTopCap(t *testing.T) {
	cmd := newHotCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--top", "1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "heavyStringify") {
		t.Errorf("output missing heavyStringify even though --top 1 must not change the CodeFrame target:\n%s", text)
	}
	if !strings.Contains(text, "> 5 |") && !strings.Contains(text, ">  5 |") {
		t.Errorf("output missing the real hot line 5 under --top 1:\n%s", text)
	}
	if strings.Contains(text, "diffuse") {
		t.Errorf("output must not warn diffuse: heavyStringify has 95.8%% self, --top 1 must not change that:\n%s", text)
	}
}

// TestHotFileBunFixtureDefaultOutputNamesHotLineWithoutFunc is the CLI-level
// regression for the "Bun native builtins" finding: with NO --func, the
// default target must be heavyStringify (line 5), not a location-less
// native builtin frame (Bun's own stringify/repeat) rendered as "line 0 of
// an empty file".
func TestHotFileBunFixtureDefaultOutputNamesHotLineWithoutFunc(t *testing.T) {
	cmd := newHotCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", "../profiler/testdata/bun-cpu-prof.cpuprofile"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := out.String()
	if strings.Contains(text, ">  0 |") || strings.Contains(text, "> 0 |") {
		t.Errorf("output fabricated a line-0 hot line:\n%s", text)
	}
	if !strings.Contains(text, "> 5 |") && !strings.Contains(text, ">  5 |") {
		t.Errorf("default output (no --func) must name line 5 of heavyStringify:\n%s", text)
	}
	// The native builtins' real cost (88%+ of this fixture's samples)
	// should still surface, as heavyStringify's callees, not vanish.
	if !strings.Contains(text, "calls->") || !strings.Contains(text, "stringify") {
		t.Errorf("output missing the native builtins as callees:\n%s", text)
	}
}

// TestHotFileIdleProfileSkipsCodeFrame is the CLI-level regression for the
// AC-5 honesty finding: an idle profile must show its warning and its
// (honest) table, but never a CodeFrame '>' marker — there is no real hot
// line to point at.
func TestHotFileIdleProfileSkipsCodeFrame(t *testing.T) {
	cmd := newHotCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", "../profiler/testdata/idle.cpuprofile"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "mostly idle") {
		t.Errorf("output missing the mostly-idle warning:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, ">") {
			t.Errorf("output must not print a '>' hot-line marker for a mostly-idle profile: %q", line)
		}
	}
}

// TestHotUnknownFuncErrorsConsistentlyAcrossJSONAndHuman is the CLI-level
// regression for the finding that --func nope --json exited 0 with an
// empty functions list while human mode failed: both modes must refuse the
// same way.
func TestHotUnknownFuncErrorsConsistentlyAcrossJSONAndHuman(t *testing.T) {
	human := newHotCmd()
	human.SetOut(&bytes.Buffer{})
	human.SetErr(&bytes.Buffer{})
	human.SetArgs([]string{"--file", v8HotFixture, "--func", "nope"})
	humanErr := human.Execute()

	jsonCmd := newHotCmd()
	jsonCmd.SetOut(&bytes.Buffer{})
	jsonCmd.SetErr(&bytes.Buffer{})
	jsonCmd.SetArgs([]string{"--file", v8HotFixture, "--func", "nope", "--json"})
	jsonErr := jsonCmd.Execute()

	if humanErr == nil || jsonErr == nil {
		t.Fatalf("expected both modes to error for an unknown --func: human=%v json=%v", humanErr, jsonErr)
	}
}

// TestHotUnknownFuncDoesNotWriteExport is the CLI-level regression for the
// finding that --export wrote its file before the --func not-found check
// ran.
func TestHotUnknownFuncDoesNotWriteExport(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "hot.json")
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--func", "nope", "--export", out})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an unknown --func")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("--export must not write a file when --func doesn't match any function")
	}
}

func TestHumanMethodLabel(t *testing.T) {
	cases := map[profiler.HeatMethod]string{
		profiler.MethodV8PositionTicks: "v8 positionTicks",
		profiler.MethodPprofProto:      "pprof proto (inlining-aware; no go toolchain needed)",
	}
	for m, want := range cases {
		if got := humanMethodLabel(m); got != want {
			t.Errorf("humanMethodLabel(%q) = %q, want %q", m, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("(anonymous)"); got != "'(anonymous)'" {
		t.Errorf("shellQuote((anonymous)) = %q, want %q", got, "'(anonymous)'")
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote(it's) = %q, want %q", got, `'it'\''s'`)
	}
}

func TestFormatCallees(t *testing.T) {
	hm := &profiler.Heatmap{ActiveSamples: 200}
	f := profiler.HeatFunction{Callees: []profiler.HeatCallee{{Func: "stringify", Cum: 100}, {Func: "repeat", Cum: 50}}}
	got := formatCallees(hm, f)
	want := "calls-> stringify 50.0%   repeat 25.0%"
	if got != want {
		t.Errorf("formatCallees = %q, want %q", got, want)
	}
}

func TestHotActiveClauseOmittedForNonCPUProfileType(t *testing.T) {
	hm := &profiler.Heatmap{ProfileType: profiler.HeatHeapInuse, IdleMeasured: false}
	if got := hotActiveClause(hm); got != "" {
		t.Errorf("hotActiveClause(heap) = %q, want empty (idle is a CPU-only concept)", got)
	}
}

func TestHotActiveClauseSaysNotMeasuredInsteadOfFakingIdleZero(t *testing.T) {
	hm := &profiler.Heatmap{ProfileType: profiler.HeatCPU, IdleMeasured: false, Samples: 40, ActiveSamples: 40}
	got := hotActiveClause(hm)
	if !strings.Contains(got, "not measured") {
		t.Errorf("hotActiveClause(unmeasured cpu) = %q, want it to say idle was not measured, not \"idle 0%%\"", got)
	}
	if strings.Contains(got, "idle 0%") {
		t.Errorf("hotActiveClause(unmeasured cpu) = %q, must never claim idle 0%% when idle wasn't measured", got)
	}
}

func TestHotFileJSONOutputIsLineHeatmapV1(t *testing.T) {
	cmd := newHotCmd()
	cmd.SetArgs([]string{"--file", v8HotFixture, "--json"})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	runErr := cmd.Execute()
	_ = w.Close()
	raw, _ := io.ReadAll(r)
	os.Stdout = old
	if runErr != nil {
		t.Fatalf("Execute: %v", runErr)
	}

	var hm profiler.Heatmap
	if err := json.Unmarshal(raw, &hm); err != nil {
		t.Fatalf("--json output did not decode as a Heatmap: %v\n%s", err, raw)
	}
	if hm.Schema != profiler.HeatSchema {
		t.Errorf("schema = %q, want %q", hm.Schema, profiler.HeatSchema)
	}
	found := false
	for _, f := range hm.Functions {
		if f.Name == "heavyStringify" {
			found = true
			for _, l := range f.Lines {
				if l.Line == 5 && l.Self <= 0 {
					t.Errorf("line 5 has non-positive self: %+v", l)
				}
			}
		}
	}
	if !found {
		t.Error("heavyStringify not present in --json output")
	}
}

func TestHotRequiresFileFlag(t *testing.T) {
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error when --file is omitted")
	}
	if !strings.Contains(err.Error(), "--file is required") {
		t.Errorf("error = %q, want it to explain --file is required", err.Error())
	}
}

// TestHotNumericPositionalTargetIsPIDMode is the E3.2 regression: a numeric
// positional argument is now a real pid-mode request (leaf resolution, live
// capture), not a blanket "not implemented" refusal — so a pid nothing on
// this host actually has fails with an honest "no such process" shape
// instead of the old placeholder message.
func TestHotNumericPositionalTargetIsPIDMode(t *testing.T) {
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	// A pid essentially guaranteed not to be a live process on any test
	// host (PIDs wrap well below this on every real OS).
	cmd.SetArgs([]string{"999999999"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for a pid with no live process")
	}
	if strings.Contains(err.Error(), "not implemented yet") {
		t.Errorf("error = %q, a numeric pid must no longer hit the placeholder message", err.Error())
	}
}

// TestHotSymbolicPositionalTargetIsServiceMode is E3.2's own regression for
// the OTHER half of TestHotNumericPositionalTargetIsPIDMode above: a
// NON-numeric positional argument is now a symbolic <service> name
// (parsePID fails on it, exactly like the old "not implemented yet" branch
// checked), dispatched to runHotService (hot_service.go) instead of that
// placeholder. An unregistered service name's actual error path calls
// os.Exit(2) (see printUnknownService's own doc comment and
// resolve_test.go's identical convention for procbind's ambiguous-leaf
// exit-2 path), so it is deliberately NOT exercised by executing the
// cobra command in-process here -- see hot_service_test.go for
// printUnknownService/resolveHotServiceProject/etc. unit-tested directly,
// and specs/hot_service.yml for the real, out-of-process exit-code
// behavior end to end.
func TestHotSymbolicPositionalTargetIsServiceMode(t *testing.T) {
	if _, err := parsePID("workload"); err == nil {
		t.Fatal("test precondition: \"workload\" must not parse as a pid, or newHotCmd's dispatch would never reach runHotService for it")
	}
}

func TestHotUnknownFuncErrors(t *testing.T) {
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--func", "doesNotExist"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an unknown --func")
	}
}

func TestHotInvalidTypeErrors(t *testing.T) {
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--type", "bogus"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an invalid --type")
	}
}

func TestHotTypeMismatchAgainstCDPSourceErrors(t *testing.T) {
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--type", "heap"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error: --type heap against a .cpuprofile (always cpu)")
	}
	if !strings.Contains(err.Error(), "only applies to a pprof profile") {
		t.Errorf("error = %q, want it to explain --type only applies to pprof", err.Error())
	}
}

func TestHotMissingFileErrors(t *testing.T) {
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", filepath.Join(t.TempDir(), "nope.cpuprofile")})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for a missing --file")
	}
}

func TestHotExportJSON(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "hot.json")
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--export", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	var hm profiler.Heatmap
	if err := json.Unmarshal(data, &hm); err != nil {
		t.Fatalf("exported file is not valid line_heatmap JSON: %v", err)
	}
}

func TestHotExportCPUProfileCopiesRawBytes(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "copy.cpuprofile")
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--export", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want, _ := os.ReadFile(v8HotFixture)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("exported .cpuprofile bytes do not match the source file")
	}
}

func TestHotExportMismatchedFormatErrors(t *testing.T) {
	dir := t.TempDir()
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--export", filepath.Join(dir, "out.pb.gz")})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error exporting a CDP source to .pb.gz")
	}
}

func TestHotExportUnsupportedExtensionErrors(t *testing.T) {
	dir := t.TempDir()
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", v8HotFixture, "--export", filepath.Join(dir, "out.txt")})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an unsupported --export extension")
	}
}

func TestParseHotType(t *testing.T) {
	cases := map[string]profiler.HeatProfileType{
		"":           profiler.HeatCPU,
		"cpu":        profiler.HeatCPU,
		"heap":       profiler.HeatHeapInuse,
		"heap-alloc": profiler.HeatHeapAlloc,
		"goroutine":  profiler.HeatGoroutine,
	}
	for in, want := range cases {
		got, err := parseHotType(in)
		if err != nil || got != want {
			t.Errorf("parseHotType(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	if _, err := parseHotType("bogus"); err == nil {
		t.Error("expected an error for an invalid type")
	}
}

func TestHottestBySelfPrefersSelfOverCum(t *testing.T) {
	fns := []profiler.HeatFunction{
		{Name: "wrapper", SelfPct: 0.2, CumPct: 61.2},
		{Name: "heavyStringify", SelfPct: 60.8, CumPct: 61.0},
		{Name: "flakyParse", SelfPct: 4.1, CumPct: 4.1},
	}
	got := hottestBySelf(fns)
	if got.Name != "heavyStringify" {
		t.Errorf("hottestBySelf = %q, want heavyStringify (highest self, even though wrapper has higher cum)", got.Name)
	}
}

func TestNextFuncSuggestionSkipsAnonymous(t *testing.T) {
	fns := []profiler.HeatFunction{
		{Name: "(anonymous)"},
		{Name: "shown"},
		{Name: "cheap"},
	}
	if got := nextFuncSuggestion(fns, "shown"); got != "cheap" {
		t.Errorf("nextFuncSuggestion = %q, want cheap (skipping (anonymous))", got)
	}
	// Only itself and anonymous functions available: falls back to anonymous.
	only := []profiler.HeatFunction{{Name: "(anonymous)"}, {Name: "shown"}}
	if got := nextFuncSuggestion(only, "shown"); got != "(anonymous)" {
		t.Errorf("nextFuncSuggestion fallback = %q, want (anonymous)", got)
	}
	// Only the shown function itself: falls back to it.
	if got := nextFuncSuggestion([]profiler.HeatFunction{{Name: "shown"}}, "shown"); got != "shown" {
		t.Errorf("nextFuncSuggestion single-function = %q, want shown", got)
	}
}

func TestHeatLocation(t *testing.T) {
	cases := []struct {
		f    profiler.HeatFunction
		want string
	}{
		{profiler.HeatFunction{File: "a.js"}, "a.js"},
		{profiler.HeatFunction{File: "a.js", StartLine: 5}, "a.js:5"},
		{profiler.HeatFunction{File: "a.js", StartLine: 5, EndLine: 5}, "a.js:5"},
		{profiler.HeatFunction{File: "a.js", StartLine: 5, EndLine: 9}, "a.js:5-9"},
	}
	for _, c := range cases {
		if got := heatLocation(c.f); got != c.want {
			t.Errorf("heatLocation(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

func TestFormatHeatQuantity(t *testing.T) {
	if got := formatHeatQuantity(1750000000, "nanoseconds"); got != "1.75s" {
		t.Errorf("nanoseconds = %q, want 1.75s", got)
	}
	if got := formatHeatQuantity(1024, "bytes"); !strings.Contains(got, "1.0") {
		t.Errorf("bytes = %q, want a human byte size", got)
	}
	if got := formatHeatQuantity(930, "samples"); got != "930 samples" {
		t.Errorf("samples = %q, want %q", got, "930 samples")
	}
}

func TestHasHeatFunction(t *testing.T) {
	hm := &profiler.Heatmap{Functions: []profiler.HeatFunction{{Name: "foo"}}}
	if !hasHeatFunction(hm, "foo") {
		t.Error("hasHeatFunction(foo) = false, want true")
	}
	if hasHeatFunction(hm, "bar") {
		t.Error("hasHeatFunction(bar) = true, want false")
	}
}

func TestWantColorRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if wantColor(os.Stdout) {
		t.Error("wantColor must be false when NO_COLOR is set, regardless of the file")
	}
}

func TestWantColorNilFile(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if wantColor(nil) {
		t.Error("wantColor(nil) must be false")
	}
}

func TestCopyFileBytesCreatesDestinationDirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nested", "dst.txt")
	if err := copyFileBytes(src, dst); err != nil {
		t.Fatalf("copyFileBytes: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "hello" {
		t.Errorf("copied content = %q, err %v, want %q", got, err, "hello")
	}
}

// --- E3.2 (live pid capture) --------------------------------------------

func TestHeatTypeToProfileType(t *testing.T) {
	cases := map[profiler.HeatProfileType]profiler.ProfileType{
		profiler.HeatCPU:       profiler.ProfileCPU,
		profiler.HeatHeapInuse: profiler.ProfileHeap,
		profiler.HeatHeapAlloc: profiler.ProfileHeap,
		profiler.HeatGoroutine: profiler.ProfileGoroutine,
	}
	for in, want := range cases {
		if got := heatTypeToProfileType(in); got != want {
			t.Errorf("heatTypeToProfileType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProfilerSourceFromCaptureCDPText(t *testing.T) {
	raw, err := os.ReadFile(v8HotFixture)
	if err != nil {
		t.Fatal(err)
	}
	src, cleanup, err := profilerSourceFromCapture(profiler.Profile{Type: profiler.ProfileCPU, Method: "inspector_cpu", Text: string(raw)})
	if err != nil {
		t.Fatalf("profilerSourceFromCapture: %v", err)
	}
	defer cleanup()
	if src.Kind != profiler.SourceCDP {
		t.Errorf("Kind = %q, want cdp", src.Kind)
	}
}

// TestProfilerSourceFromCaptureCDPTextCleanupRunsAfterCallerIsDone is the
// --export regression: the staged temp file backing a Text-sourced (CDP)
// capture must still exist when the CALLER is done with src (e.g. having
// just used src.Path for --export), and only actually disappear once the
// caller itself invokes the returned cleanup func — not the instant
// profilerSourceFromCapture itself returns.
func TestProfilerSourceFromCaptureCDPTextCleanupRunsAfterCallerIsDone(t *testing.T) {
	raw, err := os.ReadFile(v8HotFixture)
	if err != nil {
		t.Fatal(err)
	}
	src, cleanup, err := profilerSourceFromCapture(profiler.Profile{Type: profiler.ProfileCPU, Method: "inspector_cpu", Text: string(raw)})
	if err != nil {
		t.Fatalf("profilerSourceFromCapture: %v", err)
	}
	if _, statErr := os.Stat(src.Path); statErr != nil {
		t.Fatalf("staged temp file must still exist before cleanup runs: %v", statErr)
	}
	cleanup()
	if _, statErr := os.Stat(src.Path); statErr == nil {
		t.Errorf("staged temp file %s should be removed once cleanup runs", src.Path)
	}
}

func TestProfilerSourceFromCapturePprofPath(t *testing.T) {
	src, cleanup, err := profilerSourceFromCapture(profiler.Profile{Type: profiler.ProfileHeap, Method: "pprof_heap", Path: writeFakePprofCPU(t)})
	if err != nil {
		t.Fatalf("profilerSourceFromCapture: %v", err)
	}
	defer cleanup()
	if src.Kind != profiler.SourcePprof {
		t.Errorf("Kind = %q, want pprof", src.Kind)
	}
	// The Path branch's cleanup is a no-op: that file belongs to the
	// caller's own discardTempProfilePath, not this function.
	if _, statErr := os.Stat(src.Path); statErr != nil {
		t.Fatalf("Path-sourced file should still exist: %v", statErr)
	}
	cleanup()
	if _, statErr := os.Stat(src.Path); statErr != nil {
		t.Errorf("Path-sourced file must survive profilerSourceFromCapture's own cleanup (a no-op): %v", statErr)
	}
}

func TestProfilerSourceFromCaptureNoEvidenceErrors(t *testing.T) {
	_, cleanup, err := profilerSourceFromCapture(profiler.Profile{Type: profiler.ProfileSample, Method: "sample"})
	cleanup()
	if err == nil {
		t.Fatal("expected an error for a capture with neither Path nor Text")
	}
	if !strings.Contains(err.Error(), "no file:line detail") && !strings.Contains(err.Error(), "carries no file:line detail") {
		t.Errorf("error = %q, want it to explain the missing file:line detail", err.Error())
	}
}

func TestHotLiveHeaderLineCPUSamplingClause(t *testing.T) {
	binding := procbind.Binding{PID: 123, Runtime: procbind.RuntimeNode, InspectAddr: "127.0.0.1:9229"}
	got := hotLiveHeaderLine(context.Background(), 123, binding, "inspector_cpu", "", profiler.HeatCPU, 5*time.Second)
	for _, want := range []string{"monitor > node pid 123", "inspector 127.0.0.1:9229", "sampling 5s"} {
		if !strings.Contains(got, want) {
			t.Errorf("header = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "child of") {
		t.Errorf("header = %q, must not print a child-of clause when the leaf pid equals the requested pid", got)
	}
}

func TestHotLiveHeaderLineHeapIsInstantSnapshot(t *testing.T) {
	binding := procbind.Binding{PID: 555, Runtime: procbind.RuntimeGo}
	got := hotLiveHeaderLine(context.Background(), 555, binding, "pprof_heap", "localhost:6060", profiler.HeatHeapInuse, 5*time.Second)
	if !strings.Contains(got, "pprof localhost:6060") {
		t.Errorf("header = %q, want the pprof addr clause", got)
	}
	if !strings.Contains(got, "instant snapshot") {
		t.Errorf("header = %q, want \"instant snapshot\" for a non-CPU type", got)
	}
	if strings.Contains(got, "sampling") {
		t.Errorf("header = %q, must not print a sampling-duration clause for heap", got)
	}
}

// TestHotLiveHeaderLineChildOfFallsBackWhenRootUnresolvable covers a
// resolved leaf that differs from the requested pid, but where the
// requested (wrapper) pid can no longer be inspected (e.g. it already
// exited) — the "(child of ...)" clause must still appear, honestly
// labeled "wrapper" rather than silently vanishing.
func TestHotLiveHeaderLineChildOfFallsBackWhenRootUnresolvable(t *testing.T) {
	binding := procbind.Binding{PID: 456, Runtime: procbind.RuntimeNode}
	got := hotLiveHeaderLine(context.Background(), 999999999, binding, "inspector_cpu", "", profiler.HeatCPU, 5*time.Second)
	if !strings.Contains(got, "(child of wrapper 999999999)") {
		t.Errorf("header = %q, want a wrapper fallback child-of clause", got)
	}
}

// --- E3.4 (errors × heat overlay) ---------------------------------------

func TestHeatFileMatchesCulprit(t *testing.T) {
	cases := []struct {
		heat, culprit string
		want          bool
	}{
		{"js/workload.js", "js/workload.js", true},
		{"/repo/js/workload.js", "js/workload.js", true},
		{"js/workload.js", "/repo/js/workload.js", true},
		{"js/other_workload.js", "workload.js", false},
		{"js/workload.js", "python/workload.js", false},
		{"", "js/workload.js", false},
		{"js/workload.js", "", false},
	}
	for _, tc := range cases {
		if got := heatFileMatchesCulprit(tc.heat, tc.culprit); got != tc.want {
			t.Errorf("heatFileMatchesCulprit(%q, %q) = %v, want %v", tc.heat, tc.culprit, got, tc.want)
		}
	}
}

func TestIssueShortID(t *testing.T) {
	if got := issueShortID("ISS-5C1D9A2B3E4F5061"); got != "5C1D" {
		t.Errorf("issueShortID = %q, want 5C1D", got)
	}
	if got := issueShortID("AB"); got != "AB" {
		t.Errorf("issueShortID(short) = %q, want AB unchanged", got)
	}
}

func TestIssuesColumnForFunction(t *testing.T) {
	if got := issuesColumnForFunction(profiler.HeatFunction{}); got != "-" {
		t.Errorf("issuesColumnForFunction(none) = %q, want -", got)
	}
	f := profiler.HeatFunction{Lines: []profiler.HeatLine{
		{Line: 31, Issues: []profiler.HeatLineIssue{{ShortID: "5C1D"}}},
	}}
	if got := issuesColumnForFunction(f); got != "5C1D" {
		t.Errorf("issuesColumnForFunction(one) = %q, want 5C1D", got)
	}
	f3 := profiler.HeatFunction{Lines: []profiler.HeatLine{
		{Line: 10, Issues: []profiler.HeatLineIssue{{ShortID: "AAAA"}, {ShortID: "BBBB"}}},
		{Line: 20, Issues: []profiler.HeatLineIssue{{ShortID: "CCCC"}}},
	}}
	if got := issuesColumnForFunction(f3); got != "AAAA,BBBB,+1 more" {
		t.Errorf("issuesColumnForFunction(three) = %q, want AAAA,BBBB,+1 more", got)
	}
}

// TestApplyIssueOverlayMarksMatchingLine is the E3.4 CLI-level regression:
// applyIssueOverlay reads a real (temp) issues store and fills
// HeatLine.Issues for a line whose file (root-relative) matches a heatmap
// function's own (absolute) File and whose line falls inside the
// function's range — the exact "root-relative vs absolute" comparison the
// task calls out.
func TestApplyIssueOverlayMarksMatchingLine(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)

	store, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.UpsertOccurrenceResult(issues.OccurrenceInput{
		ObservedAt: time.Now(), Project: "polyglot", Kind: issues.KindException,
		Title: "Error: flakyParse: malformed payload", ExceptionType: "Error",
		Culprit: &issues.Culprit{Function: "flakyParse", File: "js/workload.js", Line: 31, Source: "stack"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	hm := &profiler.Heatmap{Functions: []profiler.HeatFunction{
		{Name: "flakyParse", File: "/repo/examples/polyglot/js/workload.js", StartLine: 28, EndLine: 35,
			Lines: []profiler.HeatLine{{Line: 29}, {Line: 31}}},
		{Name: "processBatch", File: "/repo/examples/polyglot/js/workload.js", StartLine: 24, EndLine: 27,
			Lines: []profiler.HeatLine{{Line: 25}}},
	}}
	if warn := applyIssueOverlay(hm, "polyglot"); warn != "" {
		t.Fatalf("applyIssueOverlay returned an unexpected degradation: %q", warn)
	}

	flaky := hm.Functions[0]
	var line31 *profiler.HeatLine
	for i := range flaky.Lines {
		if flaky.Lines[i].Line == 31 {
			line31 = &flaky.Lines[i]
		}
	}
	if line31 == nil || len(line31.Issues) != 1 {
		t.Fatalf("expected exactly one overlaid issue on line 31, got %+v", flaky.Lines)
	}
	if line31.Issues[0].ShortID != issueShortID(res.Issue.ID) {
		t.Errorf("Issues[0].ShortID = %q, want %q", line31.Issues[0].ShortID, issueShortID(res.Issue.ID))
	}
	if line31.Issues[0].Status != string(issues.StatusOpen) {
		t.Errorf("Issues[0].Status = %q, want open", line31.Issues[0].Status)
	}
	// The neighboring line (29, same function) and the other function
	// entirely (processBatch) must be untouched.
	for _, l := range flaky.Lines {
		if l.Line == 29 && len(l.Issues) != 0 {
			t.Errorf("line 29 should carry no overlay, got %+v", l.Issues)
		}
	}
	if len(hm.Functions[1].Lines[0].Issues) != 0 {
		t.Errorf("processBatch should carry no overlay, got %+v", hm.Functions[1].Lines[0].Issues)
	}
}

// TestApplyIssueOverlayNoStoreYetIsNotADegradation asserts the common case
// -- no issues.veclite has ever been written on this host -- returns "" (no
// Warnings entry), not a fabricated degradation message.
func TestApplyIssueOverlayNoStoreYetIsNotADegradation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(issues.StorePathEnv, filepath.Join(dir, "does-not-exist.veclite"))
	hm := &profiler.Heatmap{Functions: []profiler.HeatFunction{{Name: "f", File: "a.go", Lines: []profiler.HeatLine{{Line: 1}}}}}
	if warn := applyIssueOverlay(hm, "local"); warn != "" {
		t.Errorf("applyIssueOverlay = %q, want \"\" when no store has ever been written", warn)
	}
}

// TestApplyIssueOverlayIgnoresOtherProjectsCulprit is the E3.4
// cross-project regression: a root-relative culprit path shape many
// projects share (here "ov2.js:5") must not be stamped onto a DIFFERENT
// project's identically-named file just because the issues store happens
// to hold both.
func TestApplyIssueOverlayIgnoresOtherProjectsCulprit(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)

	store, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertOccurrenceResult(issues.OccurrenceInput{
		ObservedAt: time.Now(), Project: "otherproj", Kind: issues.KindException,
		Title: "Error: boom", ExceptionType: "Error",
		Culprit: &issues.Culprit{Function: "work", File: "ov2.js", Line: 5, Source: "stack"},
	}); err != nil {
		t.Fatal(err)
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	hm := &profiler.Heatmap{Functions: []profiler.HeatFunction{
		{Name: "work", File: "/scratch/myproj/ov2.js", StartLine: 3, EndLine: 6,
			Lines: []profiler.HeatLine{{Line: 5}}},
	}}
	if warn := applyIssueOverlay(hm, "myproj"); warn != "" {
		t.Fatalf("applyIssueOverlay returned an unexpected degradation: %q", warn)
	}
	if got := hm.Functions[0].Lines[0].Issues; len(got) != 0 {
		t.Errorf("expected no overlay for a different project's issue, got %+v", got)
	}

	// Sanity: the SAME store, filtered by the issue's OWN project, does
	// overlay — proving the negative result above is the project filter at
	// work, not some other bug hiding a real match.
	hm2 := &profiler.Heatmap{Functions: []profiler.HeatFunction{
		{Name: "work", File: "/scratch/otherproj/ov2.js", StartLine: 3, EndLine: 6,
			Lines: []profiler.HeatLine{{Line: 5}}},
	}}
	if warn := applyIssueOverlay(hm2, "otherproj"); warn != "" {
		t.Fatalf("applyIssueOverlay returned an unexpected degradation: %q", warn)
	}
	if got := hm2.Functions[0].Lines[0].Issues; len(got) != 1 {
		t.Fatalf("expected exactly one overlay for the matching project, got %+v", got)
	}
}

// TestApplyIssueOverlayInsertsZeroWeightLineForUnsampledCulprit is the E3.4
// review regression: a throw/return culprit line inside a partially-sampled
// function (RangeSource "observed", whose range is only the min/max of
// whatever lines HAPPENED to be sampled) must still get an E marker even
// when that exact line carries zero samples of its own — the roadmap's own
// "6. Errores × calor" mockup shows exactly this (line 31, 0.4% self, still
// marked).
func TestApplyIssueOverlayInsertsZeroWeightLineForUnsampledCulprit(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)

	src := filepath.Join(dir, "ov.js")
	if err := os.WriteFile(src, []byte("function work(x) {\n  const y = x * 2;\n  loop(y);\n  return check(y) ? y : boom(y);\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.UpsertOccurrenceResult(issues.OccurrenceInput{
		ObservedAt: time.Now(), Project: "ovproj", Kind: issues.KindException,
		Title: "Error: boom", ExceptionType: "Error",
		Culprit: &issues.Culprit{Function: "work", File: "ov.js", Line: 4, Source: "stack"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	// Only line 3 was ever sampled -- the observed range collapses to
	// [3,3], well short of the real function body (lines 1-5) and short of
	// the culprit at line 4.
	hm := &profiler.Heatmap{Functions: []profiler.HeatFunction{
		{Name: "work", File: src, StartLine: 3, EndLine: 3, RangeSource: "observed",
			Lines: []profiler.HeatLine{{Line: 3, Self: 100, PctOfFunction: 100}}},
	}}
	if warn := applyIssueOverlay(hm, "ovproj"); warn != "" {
		t.Fatalf("applyIssueOverlay returned an unexpected degradation: %q", warn)
	}

	f := hm.Functions[0]
	var line4 *profiler.HeatLine
	for i := range f.Lines {
		if f.Lines[i].Line == 4 {
			line4 = &f.Lines[i]
		}
	}
	if line4 == nil {
		t.Fatalf("expected a zero-weight HeatLine inserted at line 4, got %+v", f.Lines)
	}
	if len(line4.Issues) != 1 || line4.Issues[0].ShortID != issueShortID(res.Issue.ID) {
		t.Errorf("line 4 Issues = %+v, want exactly the recorded issue", line4.Issues)
	}
	if line4.Self != 0 || line4.Cum != 0 {
		t.Errorf("inserted line must carry zero weight, got Self=%d Cum=%d", line4.Self, line4.Cum)
	}
	if line4.Code == "" {
		t.Errorf("inserted line should still carry its source snippet, got empty Code")
	}
	if f.EndLine < 4 {
		t.Errorf("EndLine should widen to include the culprit line, got %d", f.EndLine)
	}
	// Lines must stay sorted ascending -- every other Heatmap consumer
	// (CodeFrame, --json) assumes this.
	for i := 1; i < len(f.Lines); i++ {
		if f.Lines[i].Line < f.Lines[i-1].Line {
			t.Errorf("Lines out of order: %+v", f.Lines)
		}
	}
}

// TestRunHotPIDLiveNodeInspectorNamesHotLine is the E3.2 end-to-end CLI
// regression: `monitor hot <pid>` against a real `node --inspect` process
// resolves the leaf, captures live, and names
// examples/polyglot/js/workload.js's planted hot line (17).
func TestRunHotPIDLiveNodeInspectorNamesHotLine(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	workload, err := filepath.Abs(filepath.Join("..", "..", "examples", "polyglot", "js", "workload.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(workload); statErr != nil {
		t.Skipf("workload fixture not found at %s", workload)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cmd := exec.Command(nodeBin, "--jitless", "--inspect="+addr, workload)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start node: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	pid := cmd.Process.Pid

	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("node inspector never came up on %s", addr)
	}
	time.Sleep(300 * time.Millisecond)

	hotCmd := newHotCmd()
	var out bytes.Buffer
	hotCmd.SetOut(&out)
	hotCmd.SetErr(&bytes.Buffer{})
	hotCmd.SetArgs([]string{fmt.Sprintf("%d", pid), "--duration", "2s"})
	if err := hotCmd.Execute(); err != nil {
		t.Fatalf("monitor hot %d: %v", pid, err)
	}
	text := out.String()
	// The header may or may not carry the optional "<name> = " clause
	// (hotLiveTargetName, from binding.MainScript) ahead of "node pid" —
	// either way it must start with "monitor > " and name the runtime+pid.
	firstLine := strings.SplitN(text, "\n", 2)[0]
	if !strings.HasPrefix(firstLine, "monitor > ") || !strings.Contains(firstLine, "node pid") {
		t.Errorf("output missing the live header line:\n%s", text)
	}
	if !strings.Contains(text, "> 17 |") && !strings.Contains(text, ">  17 |") {
		t.Errorf("output missing the planted hot line 17:\n%s", text)
	}
}

// TestRunHotPIDLiveGoRunHeapNamesAllocationLine is E3.5's own end-to-end
// regression for a Go target: `monitor hot <pid> --type heap` against a
// REAL `go run .` launch of examples/polyglot/go-pprof resolves the pid
// given (the `go` toolchain process) down to its compiled child — via
// tree.go's looksLikeGoRunBinary reclassification, not this package's own
// code — captures a real pprof heap snapshot, and names main.buildIndex's
// planted retained allocation (line 96; see the fixture's own "HOT LINE
// (heap)" comment).
//
// go-pprof's pprof port (127.0.0.1:6069, hardcoded — matching every other
// spec/fixture that attaches to this same example: specs/hot_file.yml,
// specs/mcp_profile_capture.yml, specs/profile_go_pprof.yml) is a shared
// resource: on a dev machine also running other work against the same
// fixture, a launch can transiently lose the bind race (main.go discards
// http.ListenAndServe's error, by design, matching every other consumer of
// this fixture) and a later "is the port open" probe can succeed against
// THAT unrelated process instead of this test's own — which then goes away
// before the actual capture runs, surfacing as "connection refused" well
// after the readiness probe passed (verified live: reproduced once under
// -race with concurrent packages). attemptHotGoHeap retries the WHOLE
// spawn-probe-capture cycle, not just the capture step, so a transient
// external collision like that doesn't fail the test outright.
func TestRunHotPIDLiveGoRunHeapNamesAllocationLine(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "polyglot", "go-pprof"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "main.go")); statErr != nil {
		t.Skipf("go-pprof fixture not found: %v", statErr)
	}

	const maxAttempts = 3
	var text string
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		text, lastErr = attemptHotGoHeap(t, goBin, dir)
		if lastErr == nil {
			break
		}
		t.Logf("attempt %d/%d: %v", attempt, maxAttempts, lastErr)
	}
	if lastErr != nil {
		t.Fatalf("monitor hot --type heap against go-pprof failed after %d attempts: %v", maxAttempts, lastErr)
	}
	if !strings.HasPrefix(text, "monitor > go pid") {
		t.Errorf("output missing the live header line:\n%s", text)
	}
	if !strings.Contains(text, "(child of go") {
		t.Errorf("output should show the go-toolchain wrapper was resolved through:\n%s", text)
	}
	if !strings.Contains(text, "> 96 |") && !strings.Contains(text, ">  96 |") {
		t.Errorf("output missing the planted heap allocation line 96:\n%s", text)
	}
}

// attemptHotGoHeap is TestRunHotPIDLiveGoRunHeapNamesAllocationLine's one
// spawn-probe-capture cycle: launch a fresh `go run .`, wait for its pprof
// port, let buildIndex accumulate real retained allocations, then run
// `monitor hot <pid> --type heap`. The spawned process (and its process
// group) is always torn down before returning, successful or not, so a
// caller retrying this never accumulates leftover go-pprof processes.
func attemptHotGoHeap(t *testing.T, goBin, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command(goBin, "run", ".")
	cmd.Dir = dir
	// Setpgid so cleanup can kill the WHOLE process group: `go run` re-
	// execs a fresh compiled binary as a genuine child (not the same pid),
	// and killing only the `go` toolchain process leaves that child
	// orphaned and running — verified live (see the tree.go/tree_test.go
	// convention resolve_test.go's own "& wait" spawn already documents).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("go run .: %w", err)
	}
	root := cmd.Process.Pid
	defer func() {
		if killErr := syscall.Kill(-root, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			t.Logf("kill process group %d: %v", root, killErr)
		}
		_ = cmd.Wait()
	}()

	const pprofAddr = "127.0.0.1:6069"
	deadline := time.Now().Add(20 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if conn, dialErr := net.DialTimeout("tcp", pprofAddr, 200*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		return "", fmt.Errorf("go-pprof's pprof endpoint never came up on %s", pprofAddr)
	}
	// Let buildIndex's ticker (every 50ms) accumulate real retained
	// allocations before the heap snapshot is taken.
	time.Sleep(1 * time.Second)

	var out bytes.Buffer
	captureDeadline := time.Now().Add(10 * time.Second)
	var runErr error
	for {
		hotCmd := newHotCmd()
		out.Reset()
		hotCmd.SetOut(&out)
		hotCmd.SetErr(&bytes.Buffer{})
		hotCmd.SetArgs([]string{fmt.Sprintf("%d", root), "--type", "heap", "--pprof-addr", pprofAddr, "--func", "main.buildIndex"})
		runErr = hotCmd.Execute()
		if runErr == nil {
			return out.String(), nil
		}
		if time.Now().After(captureDeadline) {
			return "", fmt.Errorf("monitor hot %d --type heap: %w", root, runErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestRunHotPIDRefusesHeapForJSRuntime is the review regression for
// `monitor hot <node pid> --type heap`: it must refuse BEFORE ever
// attempting a CDP capture (never pausing the isolate for a heap snapshot
// `hot` can't render anyway), with an error naming a command that can
// actually work.
func TestRunHotPIDRefusesHeapForJSRuntime(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	workload, err := filepath.Abs(filepath.Join("..", "..", "examples", "polyglot", "js", "workload.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(workload); statErr != nil {
		t.Skipf("workload fixture not found at %s", workload)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cmd := exec.Command(nodeBin, "--jitless", "--inspect="+addr, workload)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start node: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	pid := cmd.Process.Pid

	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("node inspector never came up on %s", addr)
	}

	start := time.Now()
	hotCmd := newHotCmd()
	hotCmd.SetOut(&bytes.Buffer{})
	hotCmd.SetErr(&bytes.Buffer{})
	hotCmd.SetArgs([]string{fmt.Sprintf("%d", pid), "--type", "heap"})
	runErr := hotCmd.Execute()
	elapsed := time.Since(start)

	if runErr == nil {
		t.Fatal("expected monitor hot <node pid> --type heap to refuse, got nil error")
	}
	if !strings.Contains(runErr.Error(), "needs a Go pprof target") {
		t.Errorf("error = %q, want it to explain heap needs a Go pprof target", runErr.Error())
	}
	if !strings.Contains(runErr.Error(), "monitor profile") {
		t.Errorf("error = %q, want a recovery naming `monitor profile <pid> -t heap`", runErr.Error())
	}
	// The whole point of refusing BEFORE capture: this must return almost
	// immediately, never block for anywhere near defaultInspectorHeapTimeout
	// (20s) taking a CDP heap snapshot it would just throw away.
	if elapsed > 5*time.Second {
		t.Errorf("refusal took %s, want well under defaultInspectorHeapTimeout (20s) — it must never attempt the capture", elapsed)
	}
}

// testPython3Bin resolves a REAL python3 interpreter for a live test to
// spawn, skipping the test when none is found. `python3` resolved via
// exec.LookPath alone can land on an asdf shim that exits immediately with
// "No version is set" rather than a real interpreter (verified on this
// dev machine) — a version-manager quirk unrelated to anything this test
// exercises — so an asdf-installed interpreter is tried first when
// present, falling back to plain PATH resolution (the shape a provisioned
// CI runner, with no asdf shim in play, actually has).
func testPython3Bin(t *testing.T) string {
	t.Helper()
	if home, err := os.UserHomeDir(); err == nil {
		matches, _ := filepath.Glob(filepath.Join(home, ".asdf", "installs", "python", "*", "bin", "python3"))
		for _, m := range matches {
			if fi, statErr := os.Stat(m); statErr == nil && !fi.IsDir() {
				return m
			}
		}
	}
	bin, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	return bin
}

// TestRunHotPIDDarwinSampleFallbackForNonJSTarget is E3.2/AC-1's own
// "darwin sample fallback" regression: a target with no working CDP or
// pprof capture path at all — a plain Python process, exactly the "no
// probe, no pprof endpoint" case the review reproduced live — still gets a
// real, honestly function-level-only answer on macOS instead of the
// pprof-endpoint dead end. procbind.ResolveLeaf only ever resolves a
// RECOGNIZED runtime leaf (node/bun/deno/go/python/ruby — see
// isSupportedLeafRuntime), so this uses a real `python3` process rather
// than an arbitrary unrecognized binary, which ResolveLeaf would refuse
// before `hot` ever got a chance to fall back.
func TestRunHotPIDDarwinSampleFallbackForNonJSTarget(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin sample fallback only applies on macOS")
	}
	if _, err := exec.LookPath("sample"); err != nil {
		t.Skip("sample not on PATH")
	}
	pythonBin := testPython3Bin(t)
	// A tight busy loop, not time.sleep: `sample` only ever captures ACTIVE
	// (non-idle) frames from whatever the target is doing DURING its
	// roughly 1s window (see internal/profiler/sample_parse.go's own idle
	// bucketing) — a genuinely sleeping process legitimately produces zero
	// active samples, which would make BuildHeatmapFromSample refuse for a
	// real reason unrelated to what this test is actually checking.
	cmd := exec.Command(pythonBin, "-c", "x = 0\nwhile True:\n    x += 1\n")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start python3: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	pid := cmd.Process.Pid

	hotCmd := newHotCmd()
	hotCmd.SetErr(&bytes.Buffer{})
	hotCmd.SetArgs([]string{fmt.Sprintf("%d", pid), "--json"})

	// --json always writes via WriteJSON, straight to os.Stdout (not
	// cmd.OutOrStdout()) — the same convention TestHotFileJSONOutputIsLineHeatmapV1
	// already captures this way.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	runErr := hotCmd.Execute()
	_ = w.Close()
	raw, _ := io.ReadAll(r)
	os.Stdout = oldStdout
	if runErr != nil {
		t.Fatalf("monitor hot %d: %v", pid, runErr)
	}

	var hm profiler.Heatmap
	if err := json.Unmarshal(raw, &hm); err != nil {
		t.Fatalf("output is not valid line_heatmap JSON: %v\n%s", err, raw)
	}
	if hm.Method != profiler.MethodDarwinSample {
		t.Errorf("Method = %q, want %q", hm.Method, profiler.MethodDarwinSample)
	}
	found := false
	for _, w := range hm.Warnings {
		if strings.Contains(w, "function-level only") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want the function-level-only disclosure", hm.Warnings)
	}
}
