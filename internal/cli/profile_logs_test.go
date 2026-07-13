package cli

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
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

func closeLogStoreOnCleanup(t *testing.T, store *logger.Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close log store: %v", err)
		}
	})
}
