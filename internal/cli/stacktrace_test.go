package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
)

const cliPyTrace = "Traceback (most recent call last):\n" +
	"  File \"/repo/app/main.py\", line 3, in <module>\n    run()\n" +
	"ValueError: bad\n"

func decodeEvents(t *testing.T, out string) []parsedEvent {
	t.Helper()
	var evs []parsedEvent
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var ev parsedEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("unmarshal %q: %v", l, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestStacktraceParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.stderr.txt")
	content := "Error: flakyParse: boom\n    at flakyParse (/repo/app/workload.js:31:11)\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cmd := newStacktraceParseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--file", path, "--project", "acme", "--service", "widget-api"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := decodeEvents(t, out.String())
	if len(evs) != 1 {
		t.Fatalf("got %d NDJSON lines, want 1: %q", len(evs), out.String())
	}
	got := evs[0]
	if got.Project != "acme" || got.Service != "widget-api" {
		t.Errorf("project/service = %q/%q, want acme/widget-api", got.Project, got.Service)
	}
	if got.Exception == nil || got.Type != "Error" || got.Value != "flakyParse: boom" {
		t.Fatalf("exception = %+v", got.Exception)
	}
	if len(got.Frames) != 1 || got.Frames[0].Function != "flakyParse" {
		t.Errorf("frames = %+v", got.Frames)
	}
	// R0-17: no timestamp in the text means no observed_at key at all.
	if strings.Contains(out.String(), "observed_at") {
		t.Errorf("zero observed_at serialized: %s", out.String())
	}
}

func TestStacktraceParseStdin(t *testing.T) {
	cmd := newStacktraceParseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/repo/app/main.go:10 +0x1\n"))
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := decodeEvents(t, out.String())
	if len(evs) != 1 || evs[0].Parser != "gopanic" || evs[0].Project != "" || evs[0].Service != "" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestStacktraceParseCleanInputProducesNoOutput(t *testing.T) {
	cmd := newStacktraceParseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("just a normal log line\nError: unknown flag\nno errors here\n"))
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for clean input, got %q", out.String())
	}
}

func TestStacktraceParseMissingFile(t *testing.T) {
	cmd := newStacktraceParseCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", "/nonexistent/does-not-exist.txt"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for a missing --file")
	}
}

func TestStacktraceCmdHidden(t *testing.T) {
	cmd := newStacktraceCmd()
	if !cmd.Hidden {
		t.Error("stacktrace command should be Hidden")
	}
}

func TestFindGitRoot(t *testing.T) {
	if _, ok := findGitRoot(t.TempDir()); ok {
		t.Error("a fresh temp dir outside any repo should not resolve a git root")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root, ok := findGitRoot(sub)
	if want, _ := filepath.Abs(dir); !ok || root != want {
		t.Errorf("findGitRoot(%s) = %q, %v; want %q", sub, root, ok, want)
	}
}

// R0-13/R1-35: a line longer than any scanner buffer is truncated, not
// fatal, and the exceptions on both sides of it are still reported.
func TestStacktraceParseLongLineIsNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	content := cliPyTrace + strings.Repeat("x", 2_000_000) + "\n" + cliPyTrace
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newStacktraceParseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--file", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if evs := decodeEvents(t, out.String()); len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (one each side of the 2 MB line)", len(evs))
	}
}

type failingReader struct {
	data []byte
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// A read error flushes what the joiner holds before it is returned.
func TestStacktraceParseFlushesBeforeReadError(t *testing.T) {
	boom := errors.New("disk on fire")
	for _, live := range []bool{false, true} {
		var out bytes.Buffer
		err := runStacktraceParse(&failingReader{data: []byte(cliPyTrace), err: boom}, &out,
			stacktraceParseOptions{live: live, tick: 10 * time.Millisecond})
		if !errors.Is(err, boom) {
			t.Fatalf("live=%v: err = %v, want the read error", live, err)
		}
		if evs := decodeEvents(t, out.String()); len(evs) != 1 || evs[0].Type != "ValueError" {
			t.Errorf("live=%v: events = %+v, want the buffered traceback flushed first", live, evs)
		}
	}
}

func TestLineReaderTruncatesAndKeepsLastLine(t *testing.T) {
	lr := newLineReader(strings.NewReader("short\r\n"+strings.Repeat("y", 100)+"\nlast"), 10)
	var got []string
	for {
		l, err := lr.next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			break
		}
		got = append(got, l)
	}
	want := []string{"short", strings.Repeat("y", 10), "last"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// R0-12/R1-37: frames under the git root get a root-relative filename, keep
// abs_path, and are in_app; frames outside it are not.
func TestStacktraceParseRelativizesToGitRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.Abs(root)
	appFile := filepath.Join(root, "src", "ev.js")
	log := "TypeError: cannot read x\n" +
		"    at handler (" + appFile + ":3:9)\n" +
		"    at listOnTimeout (node:internal/timers:685:17)\n" +
		"    at load (" + filepath.Join(root, "node_modules", "lib", "i.js") + ":1:1)\n"
	logPath := filepath.Join(root, "logs", "app.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"--file", logPath}, {"--file", logPath, "--root", root}} {
		cmd := newStacktraceParseCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute(%v): %v", args, err)
		}
		evs := decodeEvents(t, out.String())
		if len(evs) != 1 || len(evs[0].Frames) != 3 {
			t.Fatalf("events = %+v", evs)
		}
		crash := evs[0].Frames[2]
		if crash.Filename != "src/ev.js" || crash.AbsPath != appFile || !crash.InApp {
			t.Errorf("%v: crash frame = %+v, want in-app src/ev.js with abs_path %s", args, crash, appFile)
		}
		if f := evs[0].Frames[1]; f.InApp || f.Filename != "node:internal/timers" {
			t.Errorf("%v: node internal frame = %+v, want not in-app", args, f)
		}
		if f := evs[0].Frames[0]; f.InApp || f.Filename != "node_modules/lib/i.js" {
			t.Errorf("%v: node_modules frame = %+v, want relative and not in-app", args, f)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for the command's writer goroutine and
// the test's polling.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// R1-34: on a live stdin the event is printed once the stream goes idle,
// without waiting for EOF.
func TestStacktraceParseLiveStdinEmitsOnIdle(t *testing.T) {
	pr, pw := io.Pipe()
	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- runStacktraceParse(pr, &out, stacktraceParseOptions{live: true, tick: 10 * time.Millisecond})
	}()
	if _, err := io.WriteString(pw, "panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/repo/app/main.go:10 +0x1\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), `"gopanic"`) {
		if time.Now().After(deadline) {
			t.Fatal("no event within 5s of the stream going idle; the pipe is still open")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("runStacktraceParse: %v", err)
	}
	if evs := decodeEvents(t, out.String()); len(evs) != 1 {
		t.Fatalf("got %d events, want exactly 1 (no duplicate at EOF)", len(evs))
	}
}

// recordEnv isolates one --record test: a fresh $XDG_STATE_HOME (so the
// checkpoint never touches the real developer machine's state dir) and a
// fresh MONITOR_ISSUES_STORE (so it never touches a real local issue
// store), returning the store path for direct inspection with
// issues.OpenReadOnly.
func recordEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv("MONITOR_ISSUES_STORE", store)
	return store
}

func runRecord(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newStacktraceParseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(%v): %v", args, err)
	}
	return out.String()
}

// writeSettledFixture writes content to path and backdates its mtime well
// outside stacktrace.SettleWindow, simulating a log a writer is genuinely
// done with (as opposed to one caught mid-write) -- runStacktraceRecord
// only force-closes and records a still-open trailing block, and
// checkpoints past it, once the file looks settled this way. Every
// --record test below that expects the file's LAST block to be recorded
// immediately (not held back for a follow-up run) uses this instead of a
// plain os.WriteFile.
func writeSettledFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

func openIssuesForTest(t *testing.T, storePath string) *issues.Store {
	t.Helper()
	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStacktraceRecordRequiresFile(t *testing.T) {
	recordEnv(t)
	cmd := newStacktraceParseCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--record"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--record requires --file") {
		t.Fatalf("Execute: got %v, want an error naming --file", err)
	}
}

func TestStacktraceRecordWritesOccurrenceAndSummary(t *testing.T) {
	store := recordEnv(t)
	path := filepath.Join(t.TempDir(), "app.log")
	content := "Error: flakyParse: boom\n    at flakyParse (/repo/app/workload.js:31:11)\n"
	writeSettledFixture(t, path, content)

	out := runRecord(t, "--record", "--file", path, "--project", "acme", "--root", "/repo")
	if !strings.Contains(out, "1 new blocks") || !strings.Contains(out, "1 occurrences written") {
		t.Fatalf("summary = %q, want 1 new block and 1 occurrence written", out)
	}

	db := openIssuesForTest(t, store)
	list, err := db.List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(list), list)
	}
	if list[0].OccurrenceCount != 1 {
		t.Errorf("occurrence_count = %d, want 1", list[0].OccurrenceCount)
	}
	if list[0].Project != "acme" {
		t.Errorf("project = %q, want acme", list[0].Project)
	}
	if list[0].Culprit == nil || list[0].Culprit.File != "app/workload.js" || list[0].Culprit.Line != 31 {
		t.Errorf("culprit = %+v, want app/workload.js:31", list[0].Culprit)
	}
}

func TestStacktraceRecordSecondRunIsIdempotent(t *testing.T) {
	store := recordEnv(t)
	path := filepath.Join(t.TempDir(), "app.log")
	content := "Error: flakyParse: boom\n    at flakyParse (/repo/app/workload.js:31:11)\n"
	writeSettledFixture(t, path, content)

	first := runRecord(t, "--record", "--file", path, "--project", "acme")
	if !strings.Contains(first, "1 occurrences written") {
		t.Fatalf("first run summary = %q, want 1 occurrence written", first)
	}

	second := runRecord(t, "--record", "--file", path, "--project", "acme")
	if !strings.Contains(second, "0 new blocks") || !strings.Contains(second, "0 occurrences written") {
		t.Fatalf("second run summary = %q, want 0 new blocks and 0 occurrences written (checkpoint should skip already-read bytes)", second)
	}

	db := openIssuesForTest(t, store)
	list, err := db.List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].OccurrenceCount != 1 {
		t.Fatalf("issues = %+v, want exactly 1 issue with occurrence_count 1 (replay must not inflate counts)", list)
	}
}

func TestStacktraceRecordFromStartDoesNotDuplicateViaDedupeKey(t *testing.T) {
	store := recordEnv(t)
	path := filepath.Join(t.TempDir(), "app.log")
	content := "Error: flakyParse: boom\n    at flakyParse (/repo/app/workload.js:31:11)\n"
	writeSettledFixture(t, path, content)

	runRecord(t, "--record", "--file", path, "--project", "acme")
	// --from-start ignores the checkpoint and re-reads the identical bytes
	// from 0; the reprocess DedupeKey (inode+path+offset+block hash) must
	// still catch that this is the exact same raw event and dedupe it,
	// rather than the checkpoint being the only thing keeping counts
	// stable.
	replay := runRecord(t, "--record", "--file", path, "--project", "acme", "--from-start")
	if !strings.Contains(replay, "0 occurrences written") {
		t.Errorf("--from-start replay summary = %q, want 0 occurrences written (the DedupeKey caught it)", replay)
	}
	if !strings.Contains(replay, "1 deduped") {
		t.Errorf("--from-start replay summary = %q, want it to report the deduped block, not silently omit it", replay)
	}

	db := openIssuesForTest(t, store)
	list, err := db.List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].OccurrenceCount != 1 {
		t.Fatalf("issues = %+v, want exactly 1 issue with occurrence_count 1 (DedupeKey must catch the --from-start replay)", list)
	}
}

func TestStacktraceRecordObservedAtFallsBackToFileMtimeNeverNow(t *testing.T) {
	store := recordEnv(t)
	path := filepath.Join(t.TempDir(), "app.log")
	content := "Error: flakyParse: boom\n    at flakyParse (/repo/app/workload.js:31:11)\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Backdate the fixture's mtime well away from "now" so a bug that
	// falls back to time.Now() instead of the file's mtime is caught by a
	// wide margin rather than a flaky few-millisecond race.
	old := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	runRecord(t, "--record", "--file", path, "--project", "acme")

	db := openIssuesForTest(t, store)
	list, err := db.List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d issues, want 1", len(list))
	}
	if diff := list[0].LastSeen.Sub(old); diff < -2*time.Second || diff > 2*time.Second {
		t.Errorf("last_seen = %v, want close to the file mtime %v (not time.Now())", list[0].LastSeen, old)
	}
}

func TestStacktraceRecordScrubsSecretEnvValues(t *testing.T) {
	store := recordEnv(t)
	t.Setenv("FAKE_API_TOKEN", "sekrit-value-1234")
	path := filepath.Join(t.TempDir(), "app.log")
	content := "Error: upstream rejected sekrit-value-1234 for /widgets\n    at flakyParse (/repo/app/workload.js:31:11)\n"
	writeSettledFixture(t, path, content)

	runRecord(t, "--record", "--file", path, "--project", "acme")

	db := openIssuesForTest(t, store)
	list, err := db.List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d issues, want 1", len(list))
	}
	if strings.Contains(list[0].Message, "sekrit-value-1234") {
		t.Errorf("message = %q, secret env value must be redacted before persisting", list[0].Message)
	}
}

// TestStacktraceRecordDoesNotFabricateAPartialTraceCaughtMidWrite is the
// EOF-is-not-a-block-boundary fix: --record catching a log mid-write (a
// truncated, still-open trace at EOF, with a FRESH mtime -- nothing yet
// signals the writer is done) must record NOTHING from that fragment and
// must NOT advance the checkpoint past it, so a later pass -- once the
// writer has actually finished and the file has settled -- sees the
// complete trace and records it correctly instead of starting mid-trace
// (which previously could never re-open the earlier, already-skipped
// lines).
func TestStacktraceRecordDoesNotFabricateAPartialTraceCaughtMidWrite(t *testing.T) {
	store := recordEnv(t)
	path := filepath.Join(t.TempDir(), "app.log")

	partial := "Traceback (most recent call last):\n" +
		"  File \"/repo/app/main.py\", line 3, in <module>\n" +
		"    run()\n"
	if err := os.WriteFile(path, []byte(partial), 0o644); err != nil {
		t.Fatalf("write partial fixture: %v", err)
	}
	// Deliberately NOT backdated: a fresh mtime simulates --record catching
	// a writer still mid-trace.

	first := runRecord(t, "--record", "--file", path, "--project", "acme", "--root", "/repo")
	if !strings.Contains(first, "0 occurrences written") {
		t.Fatalf("mid-write pass summary = %q, want 0 occurrences written", first)
	}
	if list, err := openIssuesForTest(t, store).List(issues.ListOptions{}); err != nil {
		t.Fatalf("List: %v", err)
	} else if len(list) != 0 {
		t.Fatalf("issues after the mid-write pass = %+v, want none (no bogus fragment issue)", list)
	}

	// The writer finishes the trace; the file is now genuinely done.
	writeSettledFixture(t, path, cliPyTrace)

	second := runRecord(t, "--record", "--file", path, "--project", "acme", "--root", "/repo")
	if !strings.Contains(second, "1 occurrences written") {
		t.Fatalf("completed pass summary = %q, want 1 occurrences written", second)
	}

	list, err := openIssuesForTest(t, store).List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("issues = %+v, want exactly 1 (the completed trace, not the earlier discarded fragment)", list)
	}
	if list[0].ExceptionType != "ValueError" {
		t.Errorf("exception_type = %q, want ValueError (the fragment's own bogus, empty type must never have been recorded)", list[0].ExceptionType)
	}
	if list[0].Culprit == nil || list[0].Culprit.File != "app/main.py" || list[0].Culprit.Line != 3 {
		t.Errorf("culprit = %+v, want app/main.py:3", list[0].Culprit)
	}
}
