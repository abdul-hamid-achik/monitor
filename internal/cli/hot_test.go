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
	if !strings.Contains(text, "method: v8_position_ticks") {
		t.Errorf("output missing method line:\n%s", text)
	}
	if !strings.Contains(text, "next  monitor hot --file") {
		t.Errorf("output missing a next line:\n%s", text)
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
