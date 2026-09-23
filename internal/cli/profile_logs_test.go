package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/logger"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func TestBuildCaptureSourcePreservesExactArgvAfterDash(t *testing.T) {
	argv := []string{"/usr/bin/printf", "%s|%s\n", "hello world", "$(not-executed)"}
	src, err := buildCaptureSource(argv, 0, 0, "", "", "info")
	if err != nil {
		t.Fatalf("buildCaptureSource: %v", err)
	}
	if src.Command != argv[0] {
		t.Fatalf("Command = %q, want %q", src.Command, argv[0])
	}
	if !reflect.DeepEqual(src.Args, argv) {
		t.Fatalf("Args = %#v, want exact %#v", src.Args, argv)
	}
	if src.Name != "printf" {
		t.Fatalf("Name = %q, want printf", src.Name)
	}
}

func TestBuildCaptureSourceRejectsAmbiguousUnseparatedArgv(t *testing.T) {
	_, err := buildCaptureSource([]string{"printf", "hello world"}, -1, 0, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "after --") {
		t.Fatalf("error = %v, want exact-argv guidance", err)
	}
}

func TestBuildCaptureSourceSetsTailPID(t *testing.T) {
	src, err := buildCaptureSource(nil, -1, 123, "", "", "warn")
	if err != nil {
		t.Fatalf("buildCaptureSource: %v", err)
	}
	if src.PID != 123 || src.Name != "pid:123" {
		t.Fatalf("source = %+v, want PID and default name", src)
	}
}

func TestLogsCaptureExactArgvAndExplicitStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested-meaning.veclite")
	literal := "space $HOME ; && $(never-run)"
	cmd := newLogsCaptureCmd()
	cmd.SetArgs([]string{"--store", path, "--", "printf", "%s\n", literal})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("logs capture: %v", err)
	}

	store, err := logger.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	closeLogStoreOnCleanup(t, store)
	entries, err := store.Search(literal, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(entries) != 1 || entries[0].Raw != literal {
		t.Fatalf("captured entries = %+v, want exact literal argv payload", entries)
	}
}

func TestLogsCaptureReturnsChildFailureEvenAfterWritingLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.veclite")
	cmd := newLogsCaptureCmd()
	cmd.SetArgs([]string{"--store", path, "--", "sh", "-c", "echo INFO: before-failure; exit 7"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("logs capture returned nil for a non-zero child exit")
	}

	store, err := logger.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	closeLogStoreOnCleanup(t, store)
	entries, err := store.Search("before-failure", 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("partial successful capture = (%+v, %v), want one stored line", entries, err)
	}
}

func TestWriteLogEntriesExportFormats(t *testing.T) {
	entries := []logger.Entry{{
		Timestamp: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
		PID:       42,
		Process:   "api",
		Level:     "error",
		Message:   "request failed",
		Raw:       "ERROR: request failed",
	}}
	for _, tc := range []struct {
		format string
		want   string
	}{
		{format: "json", want: `"process": "api"`},
		{format: "ndjson", want: `"process":"api"`},
		{format: "raw", want: "ERROR: request failed\n"},
		{format: "text", want: "error   42 api request failed"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			var out bytes.Buffer
			if err := writeLogEntries(&out, entries, tc.format); err != nil {
				t.Fatalf("writeLogEntries: %v", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output = %q, want substring %q", out.String(), tc.want)
			}
		})
	}
}

// TestHelperLogsCaptureUntilSignal is not a real test; it's re-executed as a
// subprocess by TestLogsCaptureSyncsStoreOnSIGTERM to prove SIGTERM really
// reaches `monitor logs capture` end to end (Context()'s signal.NotifyContext
// -> ctx cancellation -> capture.go's process-group kill -> store.Close's
// flush), not just Store.Close in isolation. Run directly, it is a no-op
// because the env var below is unset.
func TestHelperLogsCaptureUntilSignal(t *testing.T) {
	path := os.Getenv("MONITOR_LOGS_SIGTEST_PATH")
	if path == "" {
		return
	}
	cmd := newLogsCaptureCmd()
	cmd.SetArgs([]string{"--store", path, "--", "sh", "-c", `echo INFO: sigterm_needle; touch "$MONITOR_LOGS_SIGTEST_READY"; sleep 30`})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "helper execute:", err)
		os.Exit(1)
	}
}

// TestLogsCaptureSyncsStoreOnSIGTERM is the regression for the minor finding
// that the SIGTERM/SIGINT durability path (profile_logs.go's `logs capture`
// relying on store.Close(), reached promptly by capture.go's pipe-close/
// process-group fix, rather than a separate pre-Close Sync goroutine) was
// untested end to end. A real SIGTERM to a real subprocess must still leave
// the just-captured line on disk.
func TestLogsCaptureSyncsStoreOnSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM semantics differ on windows")
	}
	path := filepath.Join(t.TempDir(), "logs.veclite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLogsCaptureUntilSignal$", "-test.v")
	ready := filepath.Join(t.TempDir(), "ready")
	cmd.Env = append(os.Environ(), "MONITOR_LOGS_SIGTEST_PATH="+path, "MONITOR_LOGS_SIGTEST_READY="+ready)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper subprocess: %v", err)
	}
	// Wait until the captured `sh -c` grandchild has printed its line (it
	// touches the ready file right after), instead of a fixed sleep that a
	// slow CI runner can outlast; then give capture a moment to ingest it.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("captured child never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM helper subprocess: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logs capture did not exit within 5s of SIGTERM")
	}

	reader, err := logger.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly after SIGTERM: %v", err)
	}
	closeLogStoreOnCleanup(t, reader)
	got, err := reader.Search("sigterm_needle", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("lines visible after SIGTERM = %d, want 1 (store.Close's flush must have persisted it)", len(got))
	}
}

func closeLogStoreOnCleanup(t *testing.T, store *logger.Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close log store: %v", err)
		}
	})
}

// TestDiscardTempProfilePathClearsOnlyPath verifies the CLI's own
// temp-file-cleanup helper removes the on-disk file and clears Path, while
// leaving Text and Symbols untouched — unlike profiler.Profile.DiscardRawArtifact,
// which also clears Text. `monitor profile --json` must keep glyphrun
// procmon's promised text/symbols fields unchanged by E1.7 cleanup.
func TestDiscardTempProfilePathClearsOnlyPath(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "monitor-heap-*.pb.gz")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	if _, err := f.WriteString("raw bytes"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	prof := &profiler.Profile{Path: path, Text: "heap profile: 1", Symbols: []profiler.Symbol{{Func: "main.f"}}}

	discardTempProfilePath(prof)

	if prof.Path != "" {
		t.Errorf("Path = %q, want empty", prof.Path)
	}
	if prof.Text != "heap profile: 1" {
		t.Errorf("Text = %q, want unchanged", prof.Text)
	}
	if len(prof.Symbols) != 1 {
		t.Errorf("Symbols = %v, want unchanged", prof.Symbols)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("temp file at %s still exists after discard: %v", path, statErr)
	}

	// Safe to call again (no Path left) and on a Profile that never had one.
	discardTempProfilePath(prof)
	discardTempProfilePath(&profiler.Profile{})
}

// installFakeCodemap writes a fake `codemap` executable to a fresh PATH
// entry (the same PATH-fake-binary harness pattern as
// ecosystem/registry_test.go's TestCodemapAdaptersBindProjectAndPreserveMachineArguments)
// and returns the path to the file every invocation's argv gets appended to
// (one line per arg, records separated by invocations appearing in order).
func installFakeCodemap(t *testing.T, script string) (argsFile string) {
	t.Helper()
	binDir := t.TempDir()
	argsFile = filepath.Join(binDir, "codemap-args")
	if err := os.WriteFile(filepath.Join(binDir, "codemap"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEMAP_ARGS_FILE", argsFile)
	return argsFile
}

const fakeCodemapBatchAvailableScript = `#!/bin/sh
printf '%s\n' "$@" >> "$CODEMAP_ARGS_FILE"
case " $* " in
  *" symbol-at "*) printf '%s' '{"file":"internal/a.go","line":7,"fqn":"pkg.Run","kind":"function","resolution":"enclosing"}' ;;
  *" --batch "*) printf '%s' '{"results":[{"position":{"file":"internal/a.go","line":7},"found":true,"direct_callers":[],"blast_radius":[{}],"tests":[],"untested":false,"call_graph":"resolved"}]}' ;;
  *" impact "*) printf '%s' '{"symbol":"pkg.Run","found":true,"direct_callers":[{}],"blast_radius":[{},{}],"tests":[],"untested":false,"call_graph":"resolved"}' ;;
  *) exit 9 ;;
esac
`

// TestCodemapImpactBatchParsesEnvelope verifies codemapImpactBatch parses
// codemap's --batch envelope (ImpactBatchReport, in codemap's own
// internal/app/service_impact_batch.go) and indexes results by "file:line"
// so correlateProfile can look one up per frame.
func TestCodemapImpactBatchParsesEnvelope(t *testing.T) {
	installFakeCodemap(t, fakeCodemapBatchAvailableScript)
	byKey, ok := codemapImpactBatch(context.Background(), ecosystem.CodemapOpts{},
		[]profiler.Symbol{{File: "internal/a.go", Line: 7}}, 0)
	if !ok {
		t.Fatal("codemapImpactBatch ok = false, want true")
	}
	item, found := byKey["internal/a.go:7"]
	if !found {
		t.Fatalf("byKey = %v, want an entry for internal/a.go:7", byKey)
	}
	if !item.Found || item.CallGraph != "resolved" || len(item.BlastRadius) != 1 {
		t.Fatalf("item = %+v, want Found=true CallGraph=resolved BlastRadius len 1", item)
	}
}

// TestCorrelateProfileUsesBatchImpactInsteadOfPerFrame verifies
// correlateProfile spends ONE `codemap impact --batch` subprocess instead
// of a separate `codemap impact --at ...` call per resolved frame, when the
// installed codemap supports --batch: the batch response's own
// blast_radius/direct_callers counts (distinct from what the per-frame
// stub would return) must be the ones that land in the correlation entry,
// proving the batch path — not a coincidental fallback — was actually used.
func TestCorrelateProfileUsesBatchImpactInsteadOfPerFrame(t *testing.T) {
	argsFile := installFakeCodemap(t, fakeCodemapBatchAvailableScript)
	syms := []profiler.Symbol{{Func: "pkg.Run", File: "internal/a.go", Line: 7, Cum: 50}}

	out := correlateProfile(context.Background(), syms, "")
	if len(out) != 1 {
		t.Fatalf("correlateProfile returned %d entries, want 1: %+v", len(out), out)
	}
	entry := out[0]
	// The batch stub's blast_radius has 1 element and direct_callers 0; the
	// per-frame stub's has 2 and 1 respectively — these must match the
	// BATCH numbers, or correlateProfile silently fell back per frame.
	if blast, _ := entry["blast"].(int); blast != 1 {
		t.Errorf("blast = %v, want 1 (the --batch response's count, proving the batch path was used)", entry["blast"])
	}
	if callers, _ := entry["callers"].(int); callers != 0 {
		t.Errorf("callers = %v, want 0 (the --batch response's count)", entry["callers"])
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	invocations := strings.Count(string(raw), "impact\n")
	if invocations != 1 {
		t.Fatalf("codemap was invoked with \"impact\" %d time(s), want exactly 1 (the single --batch call, no per-frame fallback): %s", invocations, raw)
	}
	if !strings.Contains(string(raw), "--batch\n") {
		t.Errorf("codemap args never included --batch: %s", raw)
	}
}

const fakeCodemapNoBatchSupportScript = `#!/bin/sh
printf '%s\n' "$@" >> "$CODEMAP_ARGS_FILE"
case " $* " in
  *" --batch "*) exit 9 ;;
  *" symbol-at "*) printf '%s' '{"file":"internal/a.go","line":7,"fqn":"pkg.Run","kind":"function","resolution":"enclosing"}' ;;
  *" impact "*) printf '%s' '{"symbol":"pkg.Run","found":true,"direct_callers":[{}],"blast_radius":[{},{}],"tests":[],"untested":false,"call_graph":"resolved"}' ;;
  *) exit 9 ;;
esac
`

// TestCorrelateProfileFallsBackPerFrameWithoutBatchSupport verifies an
// older codemap that rejects --batch (or any --batch failure) does not
// break correlation: correlateProfile falls back to its original per-frame
// `codemap impact --at ...` call and still produces a correct entry.
func TestCorrelateProfileFallsBackPerFrameWithoutBatchSupport(t *testing.T) {
	installFakeCodemap(t, fakeCodemapNoBatchSupportScript)
	syms := []profiler.Symbol{{Func: "pkg.Run", File: "internal/a.go", Line: 7, Cum: 50}}

	out := correlateProfile(context.Background(), syms, "")
	if len(out) != 1 {
		t.Fatalf("correlateProfile returned %d entries, want 1: %+v", len(out), out)
	}
	entry := out[0]
	// The per-frame stub's blast_radius has 2 elements and direct_callers 1.
	if blast, _ := entry["blast"].(int); blast != 2 {
		t.Errorf("blast = %v, want 2 (the per-frame fallback response's count)", entry["blast"])
	}
	if callers, _ := entry["callers"].(int); callers != 1 {
		t.Errorf("callers = %v, want 1 (the per-frame fallback response's count)", entry["callers"])
	}
}
