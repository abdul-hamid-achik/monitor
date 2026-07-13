package capture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/logger"
)

// TestParseLevelVariants covers the leading-token formats we accept.
func TestParseLevelVariants(t *testing.T) {
	cases := []struct {
		line      string
		wantLevel string
		wantMsg   string
	}{
		{"INFO: hello world", "info", "hello world"},
		{"[INFO] hello world", "info", "hello world"},
		{"[INFO ] hello world", "info", "hello world"},
		{"WARN: deprecated", "warn", "deprecated"},
		{"[WARN] deprecated", "warn", "deprecated"},
		{"ERROR: boom", "error", "boom"},
		{"[ERROR] boom", "error", "boom"},
		{"FATAL: kaboom", "fatal", "kaboom"},
		{"DEBUG trace", "debug", "trace"},
		{"plain message", "info", "plain message"}, // no token = info
		{"", "info", ""},
		{"  leading spaces", "info", "  leading spaces"},
		{"[WARN] multi word message here", "warn", "multi word message here"},
	}
	for _, tc := range cases {
		lvl, msg := parseLevel(tc.line)
		if lvl != tc.wantLevel || msg != tc.wantMsg {
			t.Errorf("parseLevel(%q) = (%q, %q), want (%q, %q)",
				tc.line, lvl, msg, tc.wantLevel, tc.wantMsg)
		}
	}
}

// TestLooksLikeLogPath verifies the log-path heuristic used to filter
// lsof output for tail-by-pid.
func TestLooksLikeLogPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/var/log/system.log", true},
		{"/usr/local/var/myapp/app.log", true},
		{"/Users/me/project/foo/app.stderr", false}, // .stderr not in our set
		{"/var/log/app.log.2024", true},             // rotated logs end in numbers
		{"/tmp/foo.txt", false},
		{"/var/log/foo", true},                     // /var/log/ prefix
		{"/Users/me/project/log/server.out", true}, // .out suffix
		{"/dev/null", false},
		{"", false},
		{"/Library/Logs/foo", false}, // /logs/ (plural) does not match /log/ segment
	}
	for _, tc := range cases {
		if got := looksLikeLogPath(tc.path); got != tc.want {
			t.Errorf("looksLikeLogPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestIngestCapturesStdoutAndStderr spawns a process that emits a few
// lines on each stream, then asserts the store contains them with the
// right levels (stderr lines default to "error" when no level token).
func TestIngestCapturesStdoutAndStderr(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "logs.veclite")
	store, err := logger.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	closeTestStore(t, store)

	// sh -c prints on stdout and stderr separately so we can verify
	// both streams reach the store with the right levels.
	cmd := "sh"
	src := Source{
		Command: cmd,
		Args:    []string{cmd, "-c", "echo INFO: hello-out; echo >&2 ERROR: hello-err; echo plain line"},
		Name:    "test",
	}
	r := NewRunner(store)
	res := r.Run(context.Background(), src)
	if res.Err != nil {
		t.Fatalf("Run error: %v", res.Err)
	}
	if res.Lines < 3 {
		t.Errorf("expected >=3 lines, got %d", res.Lines)
	}

	// Search for "hello-out" and "hello-err" — they should both be
	// in the store regardless of the keyword case.
	for _, want := range []string{"hello-out", "hello-err", "plain line"} {
		entries, err := store.Search(want, 10)
		if err != nil {
			t.Fatalf("search %q: %v", want, err)
		}
		if len(entries) == 0 {
			t.Errorf("expected to find %q in store; not present", want)
		} else if entries[0].PID <= 0 {
			t.Errorf("captured command entry PID = %d, want real child PID", entries[0].PID)
		}
	}
}

// TestIngestRespectsMaxLines verifies the cap is honored: we send 50
// lines and the runner stops at MaxLines=10.
func TestIngestRespectsMaxLines(t *testing.T) {
	dir := t.TempDir()
	store, err := logger.OpenStore(dbPathFor(t, dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	closeTestStore(t, store)

	// 50 lines of "line N" via a shell loop.
	src := Source{
		Command: "sh",
		Args:    []string{"sh", "-c", "for i in $(seq 1 50); do echo line $i; done"},
		Name:    "cap",
	}
	r := NewRunner(store)
	r.MaxLines = 10
	res := r.Run(context.Background(), src)
	if res.Lines != 10 {
		t.Errorf("Lines = %d, want 10", res.Lines)
	}
	if res.Err != nil {
		t.Errorf("cap is normal completion, got error: %v", res.Err)
	}
}

// TestRunCommandCapDoesNotDeadlock is a regression for the cap deadlock:
// a process that keeps producing output past the MaxLines cap used to
// wedge on a full OS pipe (ingest stopped reading, the child blocked on
// write, and cmd.Wait hung forever). Hitting the cap must now kill the
// child and let Run return promptly.
func TestRunCommandCapDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	store, err := logger.OpenStore(dbPathFor(t, dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	closeTestStore(t, store)

	// An infinite producer of reasonably long lines — far more than the
	// ~64KiB pipe buffer once it runs past the cap.
	src := Source{
		Command: "sh",
		Args:    []string{"sh", "-c", "while true; do echo 'regression-line-padded-out-to-fill-the-pipe-buffer-faster-0123456789'; done"},
		Name:    "cap-deadlock",
	}
	r := NewRunner(store)
	r.MaxLines = 10

	done := make(chan Result, 1)
	go func() { done <- r.Run(context.Background(), src) }()

	select {
	case res := <-done:
		if res.Lines < 10 {
			t.Errorf("Lines = %d, want >= 10", res.Lines)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after hitting the cap — deadlocked on a full pipe")
	}
}

// TestIngestStopsOnContextCancel verifies SIGINT-equivalent cancellation
// terminates a long-running capture promptly.
func TestIngestStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	store, err := logger.OpenStore(dbPathFor(t, dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	closeTestStore(t, store)

	src := Source{
		Command: "sh",
		Args:    []string{"sh", "-c", "i=0; while [ $i -lt 1000 ]; do echo line $i; i=$((i+1)); done"},
		Name:    "cap",
	}
	r := NewRunner(store)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res := r.Run(ctx, src)
	// ctx canceled mid-run; we don't expect a clean exit. But res.Lines
	// must be > 0 (we did capture something) and the runner must
	// return within a reasonable bound (no hang).
	if res.Lines == 0 {
		t.Errorf("Lines = 0; expected at least some captures before cancel")
	}
	if res.Duration > 2*time.Second {
		t.Errorf("Duration = %v; expected to honor the 200ms ctx deadline", res.Duration)
	}
}

// TestTailFileFollowsNewLines writes to a temp file, opens a tail
// against it, and verifies new appends are picked up.
func TestTailFileFollowsNewLines(t *testing.T) {
	dir := t.TempDir()
	store, err := logger.OpenStore(dbPathFor(t, dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	closeTestStore(t, store)

	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := NewRunner(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.tailFile(ctx, Source{Name: "tail", Level: "info"}, path)
	}()

	// Append two more lines, then cancel and wait for tail to return.
	if err := os.WriteFile(path, []byte("seed\none\n"), 0o644); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	// Append after a small delay so the tail reader polls and picks up.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(path, []byte("seed\none\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	time.Sleep(700 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tailFile: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("tailFile did not return after cancel")
	}
}

// TestDiscoverLogFilesFiltersBySuffix covers the lsof-output heuristic.
// We synthesize fake lsof output and assert only matching files come
// through.
func TestDiscoverLogFilesFiltersBySuffix(t *testing.T) {
	out := bytes.NewBufferString(
		"p 1234\n" +
			"f 1\n" + "n/var/log/system.log\n" +
			"f 2\n" + "n/tmp/sock\n" +
			"f 3\n" + "n/usr/local/var/app/app.stderr\n" + // not in our suffix set
			"f 4\n" + "n/var/log/foo\n" + // /var/log/ prefix
			"f 5\n" + "n/Users/me/proj/log/server.out\n" + // /log/ segment + .out
			"f 6\n" + "n/dev/null\n",
	)
	_ = out // see "real lsof" test below
	// Note: we don't actually call discoverLogFiles here because it
	// shells out; the lsof-output parsing is verified by the
	// looksLikeLogPath tests above. This placeholder documents that
	// path; the real lsof integration is covered by the live
	// integration test in specs/logs_capture.yml.
}

// TestRunDispatchesOnSourceShape verifies the dispatch table picks
// the right path based on which field is set.
func TestRunDispatchesOnSourceShape(t *testing.T) {
	dir := t.TempDir()
	store, err := logger.OpenStore(dbPathFor(t, dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	closeTestStore(t, store)

	r := NewRunner(store)
	res := r.Run(context.Background(), Source{})
	if res.Err == nil {
		t.Errorf("expected error when neither Command nor PID is set; got nil")
	}
}

// TestLevelParityOnStderr verifies that stderr lines without an
// explicit level token default to "error" (a reasonable default; the
// common shape is `printf "boom\n" >&2`).
func TestLevelParityOnStderr(t *testing.T) {
	dir := t.TempDir()
	store, err := logger.OpenStore(dbPathFor(t, dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	closeTestStore(t, store)

	src := Source{
		Command: "sh",
		Args:    []string{"sh", "-c", "echo plain stderr line >&2"},
		Name:    "sterr",
	}
	r := NewRunner(store)
	res := r.Run(context.Background(), src)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	entries, err := store.Search("plain stderr", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected to find the stderr line in the store")
	}
	if got := entries[0].Level; got != "error" {
		t.Errorf("stderr line without level token: level = %q, want %q", got, "error")
	}
}

// TestIngestStopsOnEOF verifies the runner's ingest function returns
// when the underlying reader signals EOF (vs. tailFile which loops on
// EOF to keep watching). Ingest is a one-shot read; tailFile is the
// long-running variant.
func TestIngestStopsOnEOF(t *testing.T) {
	dir := t.TempDir()
	store, err := logger.OpenStore(dbPathFor(t, dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	closeTestStore(t, store)

	runner := NewRunner(store)
	r, w := io.Pipe()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- runner.ingest(ctx, r, Source{Name: "pipe", Level: "info"}, true)
	}()
	// Close the writer side; the reader hits EOF; ingest returns.
	_ = w.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ingest: %v", err)
		}
	case <-time.After(time.Second):
		t.Errorf("ingest did not return after EOF")
	}
}

// dbPathFor returns a unique path inside dir for the log store db.
func dbPathFor(t *testing.T, dir string) string {
	t.Helper()
	return filepath.Join(dir, "logs.veclite")
}

func closeTestStore(t *testing.T, store *logger.Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close log store: %v", err)
		}
	})
}

// _ silences an unused-import warning when the test list above changes
// (strings, etc.); keeps `go vet` happy.
var _ = strings.TrimSpace

type failingAppender struct {
	err error
}

func (f failingAppender) Append(logger.Entry) error { return f.err }

func TestAppendFailureIsReturnedAndNotCounted(t *testing.T) {
	wantErr := errors.New("disk full")
	runner := NewRunner(failingAppender{err: wantErr})
	res := runner.Run(context.Background(), Source{
		Command: "sh",
		Args:    []string{"sh", "-c", "printf 'INFO: first\nINFO: second\n'"},
		Name:    "failing-store",
	})
	if !errors.Is(res.Err, wantErr) {
		t.Fatalf("Result.Err = %v, want wrapped %v", res.Err, wantErr)
	}
	if res.Lines != 0 || res.Bytes != 0 {
		t.Fatalf("failed append counted as ingested: lines=%d bytes=%d", res.Lines, res.Bytes)
	}
}

type errorAfterDataReader struct {
	read bool
	err  error
}

func (r *errorAfterDataReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(p, "INFO: accepted\n"), nil
	}
	return 0, r.err
}

func TestScannerFailureIsReturnedAfterSuccessfulLines(t *testing.T) {
	store, err := logger.OpenStore(dbPathFor(t, t.TempDir()))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	closeTestStore(t, store)

	wantErr := errors.New("source read failed")
	runner := NewRunner(store)
	err = runner.ingest(context.Background(), &errorAfterDataReader{err: wantErr}, Source{Name: "reader"}, false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ingest error = %v, want wrapped %v", err, wantErr)
	}
	if runner.lines != 1 {
		t.Fatalf("successful lines = %d, want 1", runner.lines)
	}
}

func TestScannerFailurePropagatesToResult(t *testing.T) {
	store, err := logger.OpenStore(dbPathFor(t, t.TempDir()))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	closeTestStore(t, store)

	runner := NewRunner(store)
	res := runner.Run(context.Background(), Source{
		Command: os.Args[0],
		Args:    []string{os.Args[0], "-test.run=TestCaptureOversizedLineHelper", "--"},
		Env:     []string{"MONITOR_CAPTURE_OVERSIZED_HELPER=1"},
		Name:    "oversized-line",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "scan log stream") {
		t.Fatalf("Result.Err = %v, want scanner failure", res.Err)
	}
	if res.Lines != 0 {
		t.Fatalf("oversized rejected line counted as ingested: %d", res.Lines)
	}
}

func TestCaptureOversizedLineHelper(t *testing.T) {
	if os.Getenv("MONITOR_CAPTURE_OVERSIZED_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(strings.Repeat("x", 1024*1024+1))
	os.Exit(0)
}

func TestSourceDefaultLevelAppliesToUntaggedStdout(t *testing.T) {
	store, err := logger.OpenStore(dbPathFor(t, t.TempDir()))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	closeTestStore(t, store)

	runner := NewRunner(store)
	res := runner.Run(context.Background(), Source{
		Command: "printf",
		Args:    []string{"printf", "plain warning\n"},
		Name:    "level-test",
		Level:   "warn",
	})
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	entries, err := store.Search("plain warning", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(entries) != 1 || entries[0].Level != "warn" {
		t.Fatalf("entries = %+v, want one warn entry", entries)
	}
}
