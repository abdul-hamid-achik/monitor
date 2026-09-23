package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestHotRejectsPositionalTarget(t *testing.T) {
	cmd := newHotCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"12345"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for a positional pid/service target")
	}
	if !strings.Contains(err.Error(), "not implemented yet") {
		t.Errorf("error = %q, want it to say pid/service is not implemented yet", err.Error())
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
		"":          profiler.HeatCPU,
		"cpu":       profiler.HeatCPU,
		"heap":      profiler.HeatHeapInuse,
		"goroutine": profiler.HeatGoroutine,
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
