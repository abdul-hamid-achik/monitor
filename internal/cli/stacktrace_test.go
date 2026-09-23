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
