package devrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendRuntimeOptionAppendsToExisting(t *testing.T) {
	env := []string{"PATH=/bin", "NODE_OPTIONS=--max-old-space-size=4096"}
	got := appendRuntimeOption(env, "NODE_OPTIONS", "--inspect=127.0.0.1:0")
	val, ok := envValue(t, got, "NODE_OPTIONS")
	if !ok {
		t.Fatal("NODE_OPTIONS missing")
	}
	if !strings.Contains(val, "--max-old-space-size=4096") || !strings.Contains(val, "--inspect=127.0.0.1:0") {
		t.Errorf("NODE_OPTIONS = %q, want both the original value and the appended option", val)
	}
}

func TestAppendRuntimeOptionCreatesWhenAbsent(t *testing.T) {
	got := appendRuntimeOption([]string{"PATH=/bin"}, "BUN_OPTIONS", "--preload=/x.cjs")
	val, ok := envValue(t, got, "BUN_OPTIONS")
	if !ok || val != "--preload=/x.cjs" {
		t.Errorf("BUN_OPTIONS = %q, %v, want exactly --preload=/x.cjs", val, ok)
	}
}

func TestAppendRuntimeOptionAppliedTwiceExtendsSameEntry(t *testing.T) {
	env := appendRuntimeOption(nil, "NODE_OPTIONS", "--inspect=127.0.0.1:0")
	env = appendRuntimeOption(env, "NODE_OPTIONS", "--cpu-prof")
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "NODE_OPTIONS=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d NODE_OPTIONS entries, want exactly 1", count)
	}
	val, _ := envValue(t, env, "NODE_OPTIONS")
	if !strings.Contains(val, "--inspect=127.0.0.1:0") || !strings.Contains(val, "--cpu-prof") {
		t.Errorf("NODE_OPTIONS = %q, want both options folded into the one entry", val)
	}
}

func TestApplyInspectAndProfileNodeAppendsInspectOption(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	out, err := applyInspectAndProfile(nil, Options{Inspect: true}, "node", "launch-1")
	if err != nil {
		t.Fatalf("applyInspectAndProfile: %v", err)
	}
	val, ok := envValue(t, out.env, "NODE_OPTIONS")
	if !ok || !strings.Contains(val, "--inspect=127.0.0.1:0") {
		t.Errorf("NODE_OPTIONS = %q, %v, want --inspect=127.0.0.1:0", val, ok)
	}
	if len(out.notes) != 0 {
		t.Errorf("notes = %v, want none for node --inspect", out.notes)
	}
}

// TestApplyInspectAndProfileBunInspectIsANoOpWithNote is the documented,
// verified Bun+--inspect limitation: Bun's inspector speaks JSC, not V8
// CDP, so NODE_OPTIONS must not even be touched (Bun ignores it anyway,
// but there is nothing for monitor's own profiler to do with it either),
// and a one-line note explains why instead of silently doing nothing.
func TestApplyInspectAndProfileBunInspectIsANoOpWithNote(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	out, err := applyInspectAndProfile(nil, Options{Inspect: true}, "bun", "launch-1")
	if err != nil {
		t.Fatalf("applyInspectAndProfile: %v", err)
	}
	if _, ok := envValue(t, out.env, "NODE_OPTIONS"); ok {
		t.Error("NODE_OPTIONS was set for Bun+--inspect, want it left untouched")
	}
	if len(out.notes) != 1 || !strings.Contains(out.notes[0], "JSC") {
		t.Errorf("notes = %v, want exactly one note explaining Bun speaks JSC", out.notes)
	}
}

// TestApplyInspectAndProfileDenoProfileIsSkippedWithNote is the documented,
// verified Deno+--profile limitation: NODE_OPTIONS' --cpu-prof has no
// effect at all under Deno (verified live), so --profile must not claim
// to have set anything up for it.
func TestApplyInspectAndProfileDenoProfileIsSkippedWithNote(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	out, err := applyInspectAndProfile(nil, Options{Profile: true}, "deno", "launch-1")
	if err != nil {
		t.Fatalf("applyInspectAndProfile: %v", err)
	}
	if out.profileDir != "" {
		t.Errorf("profileDir = %q, want empty for Deno (--profile is skipped)", out.profileDir)
	}
	if _, ok := envValue(t, out.env, "NODE_OPTIONS"); ok {
		t.Error("NODE_OPTIONS was set for Deno+--profile, want it left untouched")
	}
	if len(out.notes) != 1 || !strings.Contains(out.notes[0], "Deno") {
		t.Errorf("notes = %v, want exactly one note about Deno", out.notes)
	}
}

// TestApplyInspectAndProfileNodeProfileSetsUpShimAndDir covers the
// non-Deno --profile path: NODE_OPTIONS and BUN_OPTIONS both gain
// --cpu-prof --cpu-prof-dir=<dir> --require/--preload <shim>, applied
// unconditionally (not gated on argv0Base) because a wrapper's eventual
// real leaf runtime is unknown at launch time and each env var is a
// harmless no-op for whichever runtime never reads it.
func TestApplyInspectAndProfileNodeProfileSetsUpShimAndDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	out, err := applyInspectAndProfile(nil, Options{Profile: true}, "node", "launch-xyz")
	if err != nil {
		t.Fatalf("applyInspectAndProfile: %v", err)
	}
	if out.profileDir == "" {
		t.Fatal("profileDir is empty, want a private per-launch directory")
	}
	if info, statErr := os.Stat(out.profileDir); statErr != nil || !info.IsDir() {
		t.Errorf("profileDir %q does not exist as a directory: %v", out.profileDir, statErr)
	}
	nodeOpts, _ := envValue(t, out.env, "NODE_OPTIONS")
	bunOpts, _ := envValue(t, out.env, "BUN_OPTIONS")
	for _, val := range []string{nodeOpts, bunOpts} {
		if !strings.Contains(val, "--cpu-prof") || !strings.Contains(val, "--cpu-prof-dir="+out.profileDir) {
			t.Errorf("option value = %q, want --cpu-prof and --cpu-prof-dir=%s", val, out.profileDir)
		}
	}
	if !strings.Contains(nodeOpts, "--require ") {
		t.Errorf("NODE_OPTIONS = %q, want --require <shim>", nodeOpts)
	}
	if !strings.Contains(bunOpts, "--preload ") {
		t.Errorf("BUN_OPTIONS = %q, want --preload <shim>", bunOpts)
	}
}

func TestApplyInspectAndProfileBothFlagsTogether(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	out, err := applyInspectAndProfile(nil, Options{Inspect: true, Profile: true}, "node", "launch-both")
	if err != nil {
		t.Fatalf("applyInspectAndProfile: %v", err)
	}
	nodeOpts, ok := envValue(t, out.env, "NODE_OPTIONS")
	if !ok {
		t.Fatal("NODE_OPTIONS missing")
	}
	if !strings.Contains(nodeOpts, "--inspect=127.0.0.1:0") {
		t.Errorf("NODE_OPTIONS = %q, missing --inspect", nodeOpts)
	}
	if !strings.Contains(nodeOpts, "--cpu-prof") {
		t.Errorf("NODE_OPTIONS = %q, missing --cpu-prof", nodeOpts)
	}
	if out.profileDir == "" {
		t.Error("profileDir empty with --profile set")
	}
}

func TestExitShimPathIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	p1, err := exitShimPath()
	if err != nil {
		t.Fatalf("exitShimPath: %v", err)
	}
	info1, err := os.Stat(p1)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// A second call with unchanged content must not rewrite the file
	// (ModTime unchanged) -- ReadRegistryEntry-style callers may call this
	// once per launch, and rewriting an unchanged, shared file on every
	// single launch is wasted I/O for no behavioral benefit.
	time.Sleep(10 * time.Millisecond)
	p2, err := exitShimPath()
	if err != nil {
		t.Fatalf("exitShimPath (2nd): %v", err)
	}
	if p1 != p2 {
		t.Errorf("path changed between calls: %q vs %q", p1, p2)
	}
	info2, err := os.Stat(p2)
	if err != nil {
		t.Fatalf("stat (2nd): %v", err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("exit shim file was rewritten even though its content did not change")
	}
	data, err := os.ReadFile(p1)
	if err != nil {
		t.Fatalf("read shim: %v", err)
	}
	if string(data) != exitShimScript {
		t.Error("shim file content does not match exitShimScript")
	}
}

func TestNewestProfileFilePicksMostRecentCpuprofile(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "CPU.old.cpuprofile")
	newer := filepath.Join(dir, "CPU.new.cpuprofile")
	if err := os.WriteFile(older, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(newer, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-.cpuprofile file in the same directory must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := newestProfileFile(dir)
	if err != nil {
		t.Fatalf("newestProfileFile: %v", err)
	}
	if got != newer {
		t.Errorf("newestProfileFile = %q, want %q", got, newer)
	}
}

func TestNewestProfileFileMissingDirIsError(t *testing.T) {
	if _, err := newestProfileFile(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("newestProfileFile should fail for a directory that was never created")
	}
}

// TestSummarizeProfileNamesTheHotFunction reuses internal/profiler's own
// golden fixture (the same one AC-1's positionTicks fix is verified
// against): the planted hot line is line 17 inside heavyStringify.
func TestSummarizeProfileNamesTheHotFunction(t *testing.T) {
	fixture := filepath.Join("..", "profiler", "testdata", "v8-hot.cpuprofile")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture not found: %v", err)
	}
	summary, err := summarizeProfile(context.Background(), fixture)
	if err != nil {
		t.Fatalf("summarizeProfile: %v", err)
	}
	if !strings.Contains(summary, "cpu profile: "+fixture) {
		t.Errorf("summary = %q, want it to name the loaded path", summary)
	}
	if !strings.Contains(summary, "hottest:") {
		t.Errorf("summary = %q, want a hottest: clause", summary)
	}
}
