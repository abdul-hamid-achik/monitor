// Package devrun implements `monitor run -- <cmd>` (E2.4): launching a
// development process with its stdin inherited, copying its stdout/stderr
// to the terminal untouched, and -- for whichever stream(s) --scan names --
// feeding a copy of that output to internal/stacktrace's detector so an
// uncaught exception becomes a durable issue occurrence, without slowing
// the monitored process down or touching it beyond the environment
// variables it is launched with.
//
// The one rule every other rule in this package serves: monitoring must
// never be able to make the monitored process slower, let alone block it.
// See pump.go's non-blocking channel send and docs/contracts/
// local-sentry-naming.md, which this package implements section by
// section.
package devrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
)

// Scan stream selectors for Options.Scan (docs/contracts/
// local-sentry-naming.md §3).
const (
	ScanStderr = "stderr"
	ScanStdout = "stdout"
	ScanBoth   = "both"
)

// devrunProjectPIDHint is a nonzero sentinel passed as project.Hints.PID
// when resolving the launch's identity: Resolve treats PID<=0 as "a
// host-wide event with no process attached" and skips the git-root/marker
// walk entirely (see project.Hints.PID's doc comment). `run --` resolves
// identity BEFORE the child exists (it needs project/service to compute
// MONITOR_LAUNCH_ROOT/SERVICE before exec), so there is no real PID yet --
// but the launch always has a real working directory to walk. The value is
// never stored anywhere Identity itself carries no PID field.
const devrunProjectPIDHint int32 = 1

// Options configures one `monitor run -- <cmd>` launch.
type Options struct {
	// Argv is the child's argv; Argv[0] is the command.
	Argv []string
	// Scan selects which stream(s) feed the detector: "stderr" (default),
	// "stdout", or "both".
	Scan string
	// Name is --name: overrides MONITOR_LAUNCH_SERVICE and
	// project.Hints.ExplicitService.
	Name string
	// Project is --project: overrides project.Hints.ExplicitProject.
	Project string
	// Quiet suppresses the start/NEW/again banners; the exit summary still
	// prints (it reports dropped lines, which must never be silent).
	Quiet bool
	// NoIssues launches and passes the child's output through untouched,
	// but never runs the detector or writes to the issues store.
	NoIssues bool
	// RedactEnvNames names additional environment variables (beyond
	// scrub's own secret-shaped-name heuristic) whose values get redacted
	// before persisting or printing detected exception text.
	RedactEnvNames []string
	// NoSourceMaps skips appending --enable-source-maps to NODE_OPTIONS.
	NoSourceMaps bool
	// StorePath overrides issues.ResolvePath's default issue store.
	StorePath string

	// Stdin/Stdout/Stderr are monitor's own terminal streams, overridable
	// in tests; nil defaults to os.Stdin/os.Stdout/os.Stderr. Stdin is
	// always inherited by the child unchanged, regardless of --scan.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Banner is where devrun writes its own "monitor > ..." lines,
	// distinct from Stderr above (the CHILD's raw stderr passthrough) so
	// tests can tell them apart; nil defaults to Stderr, matching the
	// roadmap's "banners go to stderr" rule.
	Banner io.Writer

	// environOverride and ttyOverride exist only for tests (see
	// devrun_test.go): production callers never set them, and Run falls
	// back to os.Environ() / StdinIsTTY().
	environOverride []string
	ttyOverride     *bool
}

// Result is what Run reports once the child has exited.
type Result struct {
	ExitCode    int
	Dropped     int64
	NewIssueIDs []string
	// FirstNewIssueFullID is the first NEW issue's full "ISS-..." id
	// recorded this run (empty when NewIssueIDs is empty) -- see
	// detector.firstNewIssueFullID's doc comment for why the exit
	// summary's "next:" hint needs this instead of NewIssueIDs[0].
	FirstNewIssueFullID string
	Occurrences         int64
	Pid                 int
	// FailedWrites is how many detected exceptions could not be recorded
	// into the issues store (almost always a contended writer lock at
	// shutdown -- see detector.flushAllPending's bounded budget): counted,
	// never silently lost, and surfaced in the exit summary.
	FailedWrites int64
}

// childIOGrace bounds how long Run waits, once the child process ITSELF has
// exited, for its stdout/stderr pipes to also close before forcing them
// shut (exec.Cmd.WaitDelay). Without this, a grandchild that inherited a
// scanned pipe's write end and outlives the direct child -- `sh -c 'sleep 6
// & echo done'`, or the compiled binary `go run .` leaves running after `go
// run` itself exits -- pins Run (and therefore the whole monitor process)
// behind that orphan indefinitely, even though the process the user asked
// to run is long gone. Bounding it here means cmd.Wait() below can be
// called CONCURRENTLY with the pump goroutines still draining those pipes,
// instead of this package's previous order (wait for pipe EOF, THEN reap
// the child), which was itself the direct cause of that hang.
const childIOGrace = 2 * time.Second

// drainFloor is the minimum time drainPipes waits for EOF after the child
// exits, even when cmd.Wait already used up childIOGrace: bytes the child
// wrote before exiting sit in the kernel pipe buffer and are lost if the
// read end is closed before the pumps read them.
const drainFloor = 250 * time.Millisecond

// drainPipes waits for the pump goroutines to reach EOF on the scanned
// pipes after the child has exited. EOF normally arrives at once (the
// kernel closes an exiting process's descriptors), but a grandchild that
// inherited a write end and outlives the child would hold it open forever;
// after grace the read ends are closed to unblock the pumps, so monitor
// never hangs behind an orphan while still copying everything the child
// itself wrote.
func drainPipes(pumps *sync.WaitGroup, grace time.Duration, readEnds ...*os.File) {
	done := make(chan struct{})
	go func() {
		pumps.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		for _, f := range readEnds {
			if f != nil {
				_ = f.Close()
			}
		}
		<-done
	}
	for _, f := range readEnds {
		if f != nil {
			_ = f.Close()
		}
	}
}

// Run launches Options.Argv, wires the copy/detect pipeline per Options.Scan,
// waits for it to exit, and returns its outcome. The returned error is only
// ever a launch/setup failure (e.g. the command could not be found); an
// ordinary non-zero child exit is reported through Result.ExitCode, not an
// error, so a caller (internal/cli/run.go) can propagate it as monitor's
// own exit code without treating it as failure to run devrun itself.
func Run(ctx context.Context, opts Options) (Result, error) {
	if len(opts.Argv) == 0 {
		return Result{}, errors.New("devrun: Argv is required")
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	banner := opts.Banner
	if banner == nil {
		banner = stderr
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	environ := opts.environOverride
	if environ == nil {
		environ = os.Environ()
	}

	scan := strings.ToLower(strings.TrimSpace(opts.Scan))
	if scan == "" {
		scan = ScanStderr
	}
	if scan != ScanStderr && scan != ScanStdout && scan != ScanBoth {
		return Result{}, fmt.Errorf("devrun: invalid --scan %q (want stderr, stdout, or both)", opts.Scan)
	}
	scanStderr := scan == ScanStderr || scan == ScanBoth
	scanStdout := scan == ScanStdout || scan == ScanBoth

	storePath, err := issues.ResolvePath(opts.StorePath)
	if err != nil {
		return Result{}, fmt.Errorf("devrun: resolve issues store: %w", err)
	}

	cwd, _ := os.Getwd()
	argv0Base := filepath.Base(opts.Argv[0])
	id := project.Resolve(project.Hints{
		ExplicitProject: opts.Project,
		ExplicitService: opts.Name,
		Dir:             cwd,
		ProcessName:     argv0Base,
		PID:             devrunProjectPIDHint,
	})

	effectiveService := strings.TrimSpace(opts.Name)
	if effectiveService == "" {
		effectiveService = id.Service
	}
	if effectiveService == "" {
		effectiveService = argv0Base
	}
	launchRoot := id.GitRoot
	if launchRoot == "" {
		launchRoot = cwd
	}

	// contextids.FromEnv must be computed BEFORE BuildEnv exports
	// MONITOR_LAUNCH_* (docs/contracts/local-sentry-naming.md's "Occurrence
	// run context = contextids.FromEnv() computed BEFORE exporting" rule;
	// see watch.go:378's same ordering) -- it never reads MONITOR_LAUNCH_*
	// itself, but computing it from THIS process's inbound environment
	// keeps run correlation anchored to whatever launched monitor, not to
	// what monitor is about to export to its own child.
	run := contextids.FromEnv(contextids.IDs{})

	launch := ResolveLaunchIDs(environ, effectiveService, launchRoot)
	env := BuildEnv(environ, launch, !opts.NoSourceMaps, scanStdout)

	cmd := exec.CommandContext(ctx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Env = env
	cmd.Stdin = stdin
	// See childIOGrace's doc comment: bounds the pipe-drain wait inside
	// cmd.Wait() once the child itself has exited, instead of blocking on
	// EOF forever behind an orphaned grandchild.
	cmd.WaitDelay = childIOGrace

	ttyShared := StdinIsTTY()
	if opts.ttyOverride != nil {
		ttyShared = *opts.ttyOverride
	}
	configureProcessGroup(cmd, ttyShared)

	// Scanned streams use pipes this package owns (os.Pipe), never
	// cmd.StdoutPipe/StderrPipe: exec closes those read ends as soon as
	// cmd.Wait observes the child's exit ("it is incorrect to call Wait
	// before all reads from the pipe have completed"), so a child that
	// prints and exits immediately lost its tail — and the crash it
	// printed — on a fast runner. With our own *os.File write ends, exec
	// hands them straight to the child, Wait never touches the read ends,
	// and the pumps read until real EOF (bounded by drainPipes below).
	var stdoutPipe, stderrPipe *os.File
	var childWriteEnds []*os.File
	closeAll := func() {
		for _, f := range []*os.File{stdoutPipe, stderrPipe} {
			if f != nil {
				_ = f.Close()
			}
		}
		for _, f := range childWriteEnds {
			_ = f.Close()
		}
	}
	if scanStdout {
		r, w, perr := os.Pipe()
		if perr != nil {
			return Result{}, fmt.Errorf("devrun: stdout pipe: %w", perr)
		}
		stdoutPipe, cmd.Stdout = r, w
		childWriteEnds = append(childWriteEnds, w)
	} else {
		cmd.Stdout = stdout
	}
	if scanStderr {
		r, w, perr := os.Pipe()
		if perr != nil {
			closeAll()
			return Result{}, fmt.Errorf("devrun: stderr pipe: %w", perr)
		}
		stderrPipe, cmd.Stderr = r, w
		childWriteEnds = append(childWriteEnds, w)
	} else {
		cmd.Stderr = stderr
	}

	if err := cmd.Start(); err != nil {
		closeAll()
		return Result{}, fmt.Errorf("devrun: start %s: %w", opts.Argv[0], err)
	}
	// The child holds its own copies of the write ends now; drop ours so
	// the pumps see EOF once the child (and any grandchild that inherited
	// them) is done writing.
	for _, f := range childWriteEnds {
		_ = f.Close()
	}
	start := time.Now()

	if !opts.Quiet {
		// The start banner's rendered command text goes through the same
		// secret-shaped-value scrubbing as detected exception text (see
		// newDetector): an argument on the command line (a bearer token
		// passed via --header, say) must not land in terminal scrollback
		// or a cairntrace pane capture unredacted just because it happened
		// to be part of argv rather than the child's own output.
		bannerScrubber := scrub.New(scrub.WithValues(scrub.SecretEnvValues(environ, opts.RedactEnvNames)))
		cmdText := bannerScrubber.String(renderCommandText(opts.Argv))
		fmt.Fprintln(banner, StartBanner(cmdText, cmd.Process.Pid, id.Slug, effectiveService, scan))
	}

	var dropped int64
	lines := make(chan streamLine, linesChanCap)
	var pumpWG sync.WaitGroup
	if scanStderr {
		pumpWG.Add(1)
		go func() {
			defer pumpWG.Done()
			_ = copyStream(stderr, stderrPipe, streamStderr, lines, &dropped)
		}()
	}
	if scanStdout {
		pumpWG.Add(1)
		go func() {
			defer pumpWG.Done()
			_ = copyStream(stdout, stdoutPipe, streamStdout, lines, &dropped)
		}()
	}

	sigDone := make(chan struct{})
	go forwardSignals(cmd, ttyShared, sigDone)

	detDone := make(chan struct{})
	var det *detector
	if opts.NoIssues {
		go func() {
			defer close(detDone)
			for range lines {
			}
		}()
	} else {
		det = newDetector(detectorOptions{
			storePath:      storePath,
			launch:         launch,
			id:             id,
			run:            run,
			redactEnvNames: opts.RedactEnvNames,
			onNewIssue: func(ev newIssueEvent) {
				if !opts.Quiet {
					fmt.Fprintln(banner, NewIssueBanner(ev))
				}
			},
			onRepeat: func(shortID string, count int64) {
				if !opts.Quiet {
					fmt.Fprintln(banner, AgainBanner(shortID, count))
				}
			},
		})
		go func() {
			defer close(detDone)
			det.run(ctx, lines)
		}()
	}

	// Reap the child first, then drain: Wait never touches the scanned
	// pipes (they are ours, not exec's), so everything the child wrote is
	// still readable after it exits. drainPipes then waits for EOF, bounded
	// by childIOGrace in case an orphaned grandchild still holds a write
	// end open (the case that used to hang monitor indefinitely).
	waitStart := time.Now()
	waitErr := cmd.Wait()
	close(sigDone)
	// One shared grace window: cmd.Wait may already have spent up to
	// childIOGrace (via WaitDelay) on an exec-managed passthrough pipe that
	// an orphan holds, so only the remainder is spent here -- but never less
	// than drainFloor, enough to read what the child already wrote into the
	// kernel pipe buffer before its exit.
	drainPipes(&pumpWG, max(childIOGrace-time.Since(waitStart), drainFloor), stdoutPipe, stderrPipe)
	close(lines)
	<-detDone

	exitCode := exitCodeFor(cmd.ProcessState)
	if exitCode < 0 && waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
			return Result{}, fmt.Errorf("devrun: wait for %s: %w", opts.Argv[0], waitErr)
		}
	}

	result := Result{ExitCode: exitCode, Dropped: dropped, Pid: cmd.Process.Pid}
	if det != nil {
		result.NewIssueIDs = det.newIssueIDs
		result.FirstNewIssueFullID = det.firstNewIssueFullID
		result.Occurrences = det.occurrences
		result.FailedWrites = det.failedWrites
	}

	if !opts.Quiet || dropped > 0 || result.FailedWrites > 0 {
		fmt.Fprintln(banner, ExitSummary(ExitSummaryInfo{
			CmdName:             argv0Base,
			ExitCode:            exitCode,
			Duration:            time.Since(start),
			NewIssueIDs:         result.NewIssueIDs,
			FirstNewIssueFullID: result.FirstNewIssueFullID,
			Dropped:             dropped,
			FailedWrites:        result.FailedWrites,
		}))
	}

	return result, nil
}
