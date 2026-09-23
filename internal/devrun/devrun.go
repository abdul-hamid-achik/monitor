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
	Occurrences int64
	Pid         int
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

	ttyShared := StdinIsTTY()
	if opts.ttyOverride != nil {
		ttyShared = *opts.ttyOverride
	}
	configureProcessGroup(cmd, ttyShared)

	var stdoutPipe, stderrPipe io.ReadCloser
	if scanStdout {
		stdoutPipe, err = cmd.StdoutPipe()
		if err != nil {
			return Result{}, fmt.Errorf("devrun: stdout pipe: %w", err)
		}
	} else {
		cmd.Stdout = stdout
	}
	if scanStderr {
		stderrPipe, err = cmd.StderrPipe()
		if err != nil {
			return Result{}, fmt.Errorf("devrun: stderr pipe: %w", err)
		}
	} else {
		cmd.Stderr = stderr
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("devrun: start %s: %w", opts.Argv[0], err)
	}
	start := time.Now()

	if !opts.Quiet {
		fmt.Fprintln(banner, StartBanner(opts.Argv, cmd.Process.Pid, id.Slug, effectiveService, scan))
	}

	var dropped int64
	lines := make(chan string, linesChanCap)
	var pumpWG sync.WaitGroup
	if scanStderr {
		pumpWG.Add(1)
		go func() {
			defer pumpWG.Done()
			_ = copyStream(stderr, stderrPipe, lines, &dropped)
		}()
	}
	if scanStdout {
		pumpWG.Add(1)
		go func() {
			defer pumpWG.Done()
			_ = copyStream(stdout, stdoutPipe, lines, &dropped)
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

	// os/exec requires every read from a StdoutPipe/StderrPipe to finish
	// before Wait is called; pumpWG.Wait() below is exactly that. Only
	// after the pipes are fully drained do we close `lines`, so the
	// detector (or the plain drain loop above) sees a clean EOF and can
	// run its own final flush before detDone closes.
	pumpWG.Wait()
	close(lines)
	<-detDone

	waitErr := cmd.Wait()
	close(sigDone)

	exitCode := exitCodeFor(cmd.ProcessState)
	if exitCode < 0 && waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return Result{}, fmt.Errorf("devrun: wait for %s: %w", opts.Argv[0], waitErr)
		}
	}

	result := Result{ExitCode: exitCode, Dropped: dropped, Pid: cmd.Process.Pid}
	if det != nil {
		result.NewIssueIDs = det.newIssueIDs
		result.Occurrences = det.occurrences
	}

	if !opts.Quiet || dropped > 0 {
		fmt.Fprintln(banner, ExitSummary(ExitSummaryInfo{
			CmdName:     argv0Base,
			ExitCode:    exitCode,
			Duration:    time.Since(start),
			NewIssueIDs: result.NewIssueIDs,
			Dropped:     dropped,
		}))
	}

	return result, nil
}
