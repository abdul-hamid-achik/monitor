// Package capture runs an external command (or tails an existing
// process's log files) and ingests its stdout/stderr lines into the
// veclite-backed log store. It is the BACKLOG "log capture pipeline"
// item, completing the loop that `monitor logs search` already reads
// from.
//
// Two entry points:
//
//   - Capture(ctx, Command) — spawns a new process and captures its
//     stdout/stderr from start.
//   - TailPID(ctx, pid) — uses `lsof` to discover log files for the
//     running process and tails each one from EOF, following rotation.
//
// Both write to the same log store; search via `monitor logs search`.
// Each line is parsed for a leading level token (INFO / WARN / ERROR
// etc.) so the UI can color-code; the full line is also kept in Raw
// for exact replay.
package capture

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/logger"
)

// Source describes a Capture input. Exactly one of Command or PID is set.
type Source struct {
	// Command spawns a new process. Args is the full argv (Args[0] is
	// the program name); Dir is the working directory ("" = inherit).
	Command string
	Args    []string
	Dir     string
	// Env is appended to the parent's os.Environ. Nil = inherit only.
	Env []string
	// PID tails an already-running process. On macOS, monitor uses
	// `lsof` to find open files and tails the log-shaped ones.
	PID int32
	// Name is a human-readable label stored on each captured Entry.
	// Defaults to the command name or "pid:<n>".
	Name string
	// Level is the level stamped on each Entry. Defaults to "info"
	// unless the line itself starts with a level token.
	Level string
}

// Result is the summary returned by Run.
type Result struct {
	// Source is the resolved Source (Name defaulted if empty).
	Source Source
	// Lines is the number of lines ingested.
	Lines int
	// Bytes is the total bytes captured (stdout+stderr).
	Bytes int64
	// Duration is wall-clock time the capture ran.
	Duration time.Duration
	// Err is non-nil if the command exited non-zero or the pipeline
	// failed. The Result is still populated.
	Err error
}

// Appender is the smallest log-store contract the capture pipeline needs.
// Keeping the boundary narrow makes write failures testable and ensures a
// capture never reports a line as ingested before the store accepted it.
type Appender interface {
	Append(logger.Entry) error
}

// Runner owns the log store and orchestrates a single capture.
type Runner struct {
	store Appender

	// MaxLines caps the total number of ingested lines; 0 = unlimited.
	// When the cap is hit, the runner stops reading and returns
	// (Result.Err is nil; the cap is normal completion, not failure).
	MaxLines int
	// MaxBytes caps total bytes; 0 = unlimited. Same semantics as MaxLines.
	MaxBytes int64

	// mu protects the running counter and result fields so a caller
	// reading Result concurrently with a running capture gets a
	// consistent snapshot.
	mu    sync.Mutex
	lines int
	bytes int64
}

// NewRunner returns a Runner that writes to store. The caller must
// keep store open for the lifetime of the Runner.
func NewRunner(store Appender) *Runner {
	return &Runner{store: store}
}

// Run dispatches on src.Command vs src.PID. It blocks until ctx is
// canceled, the captured process exits, or the MaxLines / MaxBytes cap
// is reached.
func (r *Runner) Run(ctx context.Context, src Source) Result {
	start := time.Now()
	r.mu.Lock()
	r.lines = 0
	r.bytes = 0
	r.mu.Unlock()
	if src.Name == "" {
		if src.Command != "" {
			src.Name = filepath.Base(src.Command)
		} else {
			src.Name = fmt.Sprintf("pid:%d", src.PID)
		}
	}
	if src.Level == "" {
		src.Level = "info"
	}
	var res Result
	switch {
	case src.Command != "":
		res = r.runCommand(ctx, src)
	case src.PID > 0:
		res = r.runTailPID(ctx, src)
	default:
		res = Result{Source: src, Err: fmt.Errorf("capture: neither Command nor PID set")}
	}
	res.Source = src
	res.Duration = time.Since(start)
	return res
}

// runCommand spawns src.Command, captures stdout+stderr, and ingests
// line-by-line. The process is killed when ctx is canceled.
func (r *Runner) runCommand(ctx context.Context, src Source) Result {
	// Derive a cancelable context so reaching the MaxLines/MaxBytes cap
	// actively kills the child. Otherwise the ingest goroutines stop
	// reading, the OS pipe fills, the child blocks on write(), and the
	// cmd.Wait() below hangs forever.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// src.Args[0] is the program name (argv convention); the rest are real
	// args. Guard the slice so a caller that sets Command but leaves Args
	// empty gets a clean error instead of a slice-bounds panic.
	var rest []string
	if len(src.Args) > 1 {
		rest = src.Args[1:]
	}
	cmd := exec.CommandContext(ctx, src.Command, rest...)
	cmd.Dir = src.Dir
	if len(src.Env) > 0 {
		cmd.Env = append(os.Environ(), src.Env...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{Err: fmt.Errorf("stdout pipe: %w", err)}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{Err: fmt.Errorf("stderr pipe: %w", err)}
	}
	if err := cmd.Start(); err != nil {
		return Result{Err: fmt.Errorf("start: %w", err)}
	}
	// Stamp wrapped-command entries with the real child PID so later
	// `logs search --pid` filters are useful in both capture modes.
	src.PID = int32(cmd.Process.Pid)

	var wg sync.WaitGroup
	ingestErrs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		err := r.ingest(ctx, stdout, src, false)
		ingestErrs <- err
		if err != nil || r.capReached() {
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		err := r.ingest(ctx, stderr, src, true)
		ingestErrs <- err
		if err != nil || r.capReached() {
			cancel()
		}
	}()
	wg.Wait()
	close(ingestErrs)
	runErr := cmd.Wait()
	if r.capReached() {
		// Hitting the cap is a clean stop; the kill we triggered above is
		// expected, not a failure. An ingestion error cannot be hidden by
		// this branch: failed appends do not advance the counters to a cap.
		runErr = nil
	}
	for err := range ingestErrs {
		runErr = errors.Join(runErr, err)
	}

	res := Result{Err: runErr}
	r.mu.Lock()
	res.Lines = r.lines
	res.Bytes = r.bytes
	r.mu.Unlock()
	return res
}

// runTailPID discovers log files for src.PID via `lsof` and tails each
// from EOF. macOS doesn't expose /proc/<pid>/fd the way Linux does;
// lsof is the portable equivalent.
func (r *Runner) runTailPID(ctx context.Context, src Source) Result {
	files, err := discoverLogFiles(src.PID)
	if err != nil {
		return Result{Err: fmt.Errorf("discover log files: %w", err)}
	}
	if len(files) == 0 {
		return Result{Err: fmt.Errorf("no log-shaped files found for pid %d", src.PID)}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, len(files))
	for _, f := range files {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			if err := r.tailFile(ctx, src, path); err != nil {
				errCh <- err
				cancel()
			}
		}(f)
	}
	wg.Wait()
	close(errCh)
	res := Result{}
	for err := range errCh {
		res.Err = errors.Join(res.Err, err)
	}
	r.mu.Lock()
	res.Lines = r.lines
	res.Bytes = r.bytes
	r.mu.Unlock()
	return res
}

// discoverLogFiles shells out to `lsof -p <pid> -F n` to list open
// files, then filters for paths that look like log files. A log file
// is one whose name ends in .log, contains a /log/ path component, or
// lives in /var/log.
func discoverLogFiles(pid int32) ([]string, error) {
	out, err := exec.Command("lsof", "-p", strconv.Itoa(int(pid)), "-F", "n").Output()
	if err != nil {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		// lsof -F n output lines start with "n" followed by the path.
		if !strings.HasPrefix(line, "n") {
			continue
		}
		path := strings.TrimPrefix(line, "n")
		if path == "" || !looksLikeLogPath(path) {
			continue
		}
		if !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	return files, nil
}

var logSuffixes = []string{".log", ".out", ".err"}

func looksLikeLogPath(p string) bool {
	low := strings.ToLower(p)
	for _, s := range logSuffixes {
		if strings.HasSuffix(low, s) {
			return true
		}
	}
	if strings.Contains(low, "/log/") || strings.HasPrefix(low, "/var/log/") {
		return true
	}
	return false
}

// tailFile opens path at EOF and reads new lines as they're appended.
// It honors ctx for shutdown and stops when r.capReached() returns true.
func (r *Runner) tailFile(ctx context.Context, src Source, path string) (err error) {
	f, err := os.Open(path)
	if err != nil {
		// Silently skip; the file may have been rotated away between
		// lsof and open. Other files in the set may still be valid.
		return nil
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek %s: %w", path, err)
	}
	reader := bufio.NewReader(f)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if r.capReached() {
			return nil
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// No new data; sleep briefly and retry. Production
				// tailers use inotify; this is the simple polling
				// variant that works portably on macOS.
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(500 * time.Millisecond):
				}
				continue
			}
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := r.appendLine(src, line, false); err != nil {
			return fmt.Errorf("append %s: %w", path, err)
		}
	}
}

// ingest reads from r line-by-line, parses the level token, and
// appends to the log store. Honors MaxLines / MaxBytes caps and the
// context for shutdown.
func (r *Runner) ingest(ctx context.Context, reader io.Reader, src Source, isStderr bool) error {
	scanner := bufio.NewScanner(reader)
	// Allow long log lines; 1 MiB matches the log scanner convention.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if r.capReached() {
			return nil
		}
		if err := r.appendLine(src, scanner.Text(), isStderr); err != nil {
			return fmt.Errorf("append log line: %w", err)
		}
	}
	// Command-context cancellation closes the pipe underneath Scanner. That
	// read error is expected when a cap, sibling ingestion error, or caller
	// cancellation deliberately stops the child; the initiating condition is
	// reported separately.
	if ctx.Err() != nil {
		return nil
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan log stream: %w", err)
	}
	return nil
}

func (r *Runner) appendLine(src Source, raw string, isStderr bool) error {
	line := strings.TrimRight(raw, "\r\n")
	level, message := parseLevel(line)
	if level == "info" && message == line && src.Level != "" {
		level = strings.ToLower(src.Level)
	}
	if isStderr && level == "info" {
		// Stderr lines without a level token are usually errors.
		level = "error"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if (r.MaxLines > 0 && r.lines >= r.MaxLines) ||
		(r.MaxBytes > 0 && r.bytes >= r.MaxBytes) {
		return nil
	}
	if r.store == nil {
		return errors.New("log store is not configured")
	}
	if err := r.store.Append(logger.Entry{
		PID:     pidFromSource(src),
		Process: src.Name,
		Level:   level,
		Message: message,
		Raw:     line,
	}); err != nil {
		return err
	}
	r.lines++
	r.bytes += int64(len(line)) + 1 // +1 for the newline we trimmed
	return nil
}

func (r *Runner) capReached() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.MaxLines > 0 && r.lines >= r.MaxLines {
		return true
	}
	if r.MaxBytes > 0 && r.bytes >= r.MaxBytes {
		return true
	}
	return false
}

func pidFromSource(src Source) int32 { return src.PID }

// levelRe matches a leading "[LEVEL]" or "LEVEL:" token (with the
// level being one of DEBUG/INFO/WARN/WARNING/ERROR/FATAL/TRACE, case
// insensitive). The rest of the line is the message, with optional
// leading whitespace trimmed.
var levelRe = regexp.MustCompile(`^\s*(?:\[\s*)?(DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL|TRACE)(?:\s*\]|\s*[:\-]|\s)\s*(.*)$`)

func parseLevel(line string) (level, message string) {
	m := levelRe.FindStringSubmatch(line)
	if m == nil {
		return "info", line
	}
	return strings.ToLower(m[1]), m[2]
}
