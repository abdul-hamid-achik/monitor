package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
)

func TestNormalizeRequiredTools(t *testing.T) {
	got, err := normalizeRequiredTools([]string{"codemap,tvault", "glyph", "codemap"}, false)
	if err != nil {
		t.Fatalf("normalizeRequiredTools: %v", err)
	}
	want := []string{"codemap", "glyphrun", "tinyvault"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("required = %v, want %v", got, want)
	}

	strict, err := normalizeRequiredTools(nil, true)
	if err != nil || !reflect.DeepEqual(strict, doctorToolNames) {
		t.Fatalf("strict = %v, err %v; want %v", strict, err, doctorToolNames)
	}
	if _, err := normalizeRequiredTools([]string{"docker"}, false); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown tool error = %v", err)
	}
}

func TestMissingRequiredTools(t *testing.T) {
	status := ecosystem.Status{
		Codemap: ecosystem.ToolStatus{Available: true},
		Tmux:    ecosystem.ToolStatus{Available: true},
	}
	got := missingRequiredTools(status, []string{"tmux", "fcheap", "codemap", "veclite"})
	want := []string{"fcheap", "veclite"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
}

func TestDoctorHasDependencyGateFlags(t *testing.T) {
	cmd := newDoctorCmd()
	if cmd.Flags().Lookup("require") == nil || cmd.Flags().Lookup("strict") == nil || cmd.Flags().Lookup("json") == nil {
		t.Fatal("doctor should expose --require, --strict, and --json")
	}
}

// TestBuildDoctorReportIncludesCodeIntelAndBinaries isolates PATH to a fake
// toolset so the report is deterministic and fast, and asserts the new
// code_intel/binaries sections are populated and shadow detection works
// end-to-end through buildDoctorReport (E1.3's "hecho cuando": doctor shows
// ~/go/bin/glyph (dev) shadowing brew's).
func TestBuildDoctorReportIncludesCodeIntelAndBinaries(t *testing.T) {
	devDir := t.TempDir()
	brewDir := t.TempDir()
	writeFakeTool(t, devDir, "glyph", "dev")
	writeFakeTool(t, brewDir, "glyph", "v0.20.0")
	writeFakeTool(t, brewDir, "codemap", "v1.0.0")
	t.Setenv("PATH", devDir+string(os.PathListSeparator)+brewDir)

	report := buildDoctorReport(context.Background())

	if report.CodeIntel.Codemap.Tool != "codemap" || report.CodeIntel.Vecgrep.Tool != "vecgrep" {
		t.Fatalf("code_intel = %+v", report.CodeIntel)
	}
	if report.CodeIntel.Vecgrep.State != ecosystem.HealthUnavailable {
		t.Errorf("vecgrep state = %q, want %q (not on the fake PATH)", report.CodeIntel.Vecgrep.State, ecosystem.HealthUnavailable)
	}

	if report.Binaries.Self.Version != Version {
		t.Errorf("self.version = %q, want %q", report.Binaries.Self.Version, Version)
	}
	if len(report.Binaries.Other) != len(doctorBinaryNames) {
		t.Fatalf("other_binaries = %d entries, want %d", len(report.Binaries.Other), len(doctorBinaryNames))
	}
	var glyphInfo *ecosystem.BinaryInfo
	for i := range report.Binaries.Other {
		if report.Binaries.Other[i].Name == "glyph" {
			glyphInfo = &report.Binaries.Other[i]
		}
	}
	if glyphInfo == nil {
		t.Fatal("other_binaries missing a glyph entry")
	}
	if !glyphInfo.Shadowed || glyphInfo.Path != filepath.Join(devDir, "glyph") {
		t.Fatalf("glyph = %+v, want it shadowed and resolved to the dev dir first", glyphInfo)
	}
}

// TestDoctorJSONIsAdditive locks every pre-existing top-level key (the
// stable presence contract other consumers, e.g. minerva/cairntrace/chalupa,
// may already parse) while confirming the new code_intel/binaries sections
// are also present.
func TestDoctorJSONIsAdditive(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // deterministic + fast: nothing is on PATH

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"--json"})
	runErr := cmd.Execute()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = old
	if runErr != nil {
		t.Fatalf("doctor --json: %v", runErr)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("doctor --json produced invalid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{
		"codemap", "fcheap", "vecgrep", "tinyvault", "vidtrace",
		"glyphrun", "cairntrace", "veclite", "tmux",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("doctor --json dropped pre-existing key %q", key)
		}
	}
	for _, key := range []string{"code_intel", "binaries"} {
		if _, ok := got[key]; !ok {
			t.Errorf("doctor --json missing new key %q", key)
		}
	}
}

func TestFormatToolStatus(t *testing.T) {
	if got := formatToolStatus(ecosystem.ToolStatus{}); got != "unavailable" {
		t.Errorf("empty status = %q, want unavailable", got)
	}
	if got := formatToolStatus(ecosystem.ToolStatus{Note: "codemap not on PATH"}); !strings.Contains(got, "codemap not on PATH") {
		t.Errorf("status with note = %q, want it to carry the note", got)
	}
	got := formatToolStatus(ecosystem.ToolStatus{Available: true, Version: "v1.2.3", Path: "/bin/codemap"})
	if !strings.Contains(got, "v1.2.3") || !strings.Contains(got, "/bin/codemap") {
		t.Errorf("available status = %q, want version and path", got)
	}
}

func TestFormatHealth(t *testing.T) {
	ok := formatHealth(ecosystem.Health{State: ecosystem.HealthOK})
	if ok != ecosystem.HealthOK {
		t.Errorf("ok health = %q, want a bare state with no noise", ok)
	}
	degraded := formatHealth(ecosystem.Health{State: ecosystem.HealthSchemaSkew, Detail: "boom", Recovery: "fix it"})
	if !strings.Contains(degraded, "schema_skew") || !strings.Contains(degraded, "boom") || !strings.Contains(degraded, "fix it") {
		t.Errorf("degraded health = %q, want state, detail, and recovery", degraded)
	}
}

func TestFormatBinary(t *testing.T) {
	if got := formatBinary(ecosystem.BinaryInfo{}); got != "unavailable" {
		t.Errorf("unavailable binary = %q, want unavailable", got)
	}
	shadowed := formatBinary(ecosystem.BinaryInfo{Available: true, Path: "/a/glyph", Version: "dev", Shadowed: true, Warning: "shadow!"})
	if !strings.Contains(shadowed, "/a/glyph") || !strings.Contains(shadowed, "shadow!") {
		t.Errorf("shadowed binary = %q, want path and warning", shadowed)
	}
}

func TestPadName(t *testing.T) {
	if got := padName("cairn"); len(got) != 12 {
		t.Errorf("padName(%q) = %q (len %d), want len 12", "cairn", got, len(got))
	}
	if got := padName("a-very-long-binary-name"); got != "a-very-long-binary-name" {
		t.Errorf("padName should not truncate a name longer than the pad width: %q", got)
	}
}

// writeFakeTool drops an executable script named name in dir that answers
// `--version` with version, matching the fake-PATH harness pattern used by
// internal/ecosystem's registry/health tests.
func writeFakeTool(t *testing.T, dir, name, version string) {
	t.Helper()
	script := "#!/bin/sh\nprintf '" + name + " " + version + "'\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}
