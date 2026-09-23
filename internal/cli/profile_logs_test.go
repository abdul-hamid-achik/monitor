package cli

import (
	"bytes"
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

	"github.com/abdul-hamid-achik/monitor/internal/logger"
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
	cmd.SetArgs([]string{"--store", path, "--", "sh", "-c", "echo INFO: sigterm_needle; sleep 30"})
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
	cmd.Env = append(os.Environ(), "MONITOR_LOGS_SIGTEST_PATH="+path)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper subprocess: %v", err)
	}
	// Give the helper (and the `sh -c` grandchild it captures) time to print
	// its line and reach the sleep before signaling.
	time.Sleep(500 * time.Millisecond)
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
