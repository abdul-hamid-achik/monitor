package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolateRunStore points the issues store and parse-checkpoint state at
// fresh temp dirs, so a `monitor run --` test never touches a real
// developer's local store.
func isolateRunStore(t *testing.T) {
	t.Helper()
	t.Setenv("MONITOR_ISSUES_STORE", filepath.Join(t.TempDir(), "issues.veclite"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestNewRunCmdLegacyModeRequiresExactlyOneArg(t *testing.T) {
	cmd := newRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for `monitor run` with no spec argument and no --")
	}
}

func TestNewRunCmdDashRequiresACommand(t *testing.T) {
	cmd := newRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires a command after --") {
		t.Fatalf("Execute() = %v, want an error naming the missing command after --", err)
	}
}

func TestNewRunCmdDashRejectsFlagsAfterTheCommand(t *testing.T) {
	// `monitor run -- <cmd> --flag-looking-like-runs-own-flag` must NOT be
	// reinterpreted as a run flag -- everything after -- belongs to the
	// child. This is exercised implicitly by cobra's own -- handling
	// (DisableFlagParsing is not set, so flags before -- are run's own and
	// everything from -- onward is untouched), asserted here by confirming
	// a bogus "flag" after -- does not make run itself error out before
	// ever reaching devrun.
	isolateRunStore(t)
	cmd := newRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--quiet", "--", "true", "--not-a-run-flag"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want the trailing --not-a-run-flag passed to the child untouched", err)
	}
}

func TestNewRunCmdLegacyModeStillDispatchesToGlyphrun(t *testing.T) {
	// A spec path that does not exist: ecosystem.RunGlyphrun (unchanged
	// legacy behavior) must still be the code path reached -- proven by
	// getting glyphrun's own "spec not found"-shaped failure rather than
	// devrun's "Argv is required" or any dash-mode error text.
	cmd := newRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"/nonexistent/spec/path.yml"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for a nonexistent glyphrun spec path")
	}
	if strings.Contains(err.Error(), "requires a command after") || strings.Contains(err.Error(), "Argv is required") {
		t.Fatalf("Execute() = %v, dispatched to the -- mode instead of the legacy glyphrun runner", err)
	}
}

// TestNewRunCmdDashModeLaunchesAndSucceedsOnExitZero only covers the exit-0
// path: RunE returns nil straight through without ever reaching its
// os.Exit(result.ExitCode) branch (see newRunCmd), so it cannot prove that
// branch propagates a NON-zero code correctly. See
// TestNewRunCmdDashModePropagatesNonZeroExitCode below for that, via the
// standard re-exec-the-test-binary pattern (os.Exit inside the same process
// would kill the test binary itself).
func TestNewRunCmdDashModeLaunchesAndSucceedsOnExitZero(t *testing.T) {
	isolateRunStore(t)
	cmd := newRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--quiet", "--", "true"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want `run -- true` to succeed", err)
	}
}

// monitorRunSubprocessEnv, when set to "1", tells this test binary (re-
// invoked as a subprocess below) to run `monitor run -- sh -c "exit 3"`
// and let its os.Exit(3) actually terminate the subprocess, instead of
// running the whole test suite.
const monitorRunSubprocessEnv = "MONITOR_RUN_TEST_DASH_MODE_SUBPROCESS"

// TestNewRunCmdDashModePropagatesNonZeroExitCode proves newRunCmd's
// os.Exit(result.ExitCode) branch (the naming ADR's
// "se propaga el exit code" rule) actually fires for a non-zero code, not
// just the (already covered) implicit `return nil` on success. os.Exit
// cannot be called from an ordinary in-process test without killing the
// whole `go test` binary, so this re-execs the test binary itself with a
// marker env var, the same pattern os/exec's own tests use for
// TestHelperProcess-style subprocess assertions.
func TestNewRunCmdDashModePropagatesNonZeroExitCode(t *testing.T) {
	if os.Getenv(monitorRunSubprocessEnv) == "1" {
		isolateRunStore(t)
		cmd := newRunCmd()
		cmd.SetOut(os.Stdout)
		cmd.SetErr(os.Stderr)
		cmd.SetArgs([]string{"--quiet", "--", "sh", "-c", "exit 3"})
		_ = cmd.Execute()
		// If os.Exit(3) did not already terminate the process above,
		// RunE's non-zero branch did not fire -- exit a code the parent
		// below does not expect (3), so a regression is a definite
		// failure rather than an accidental pass.
		os.Exit(9)
	}

	execCmd := exec.Command(os.Args[0], "-test.run=^TestNewRunCmdDashModePropagatesNonZeroExitCode$", "-test.v")
	execCmd.Env = append(os.Environ(), monitorRunSubprocessEnv+"=1")
	var out bytes.Buffer
	execCmd.Stdout, execCmd.Stderr = &out, &out
	err := execCmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("subprocess did not exit with an *exec.ExitError (got %v); output:\n%s", err, out.String())
	}
	if exitErr.ExitCode() != 3 {
		t.Errorf("subprocess exit code = %d, want 3 (monitor run -- must propagate the child's own exit code); output:\n%s", exitErr.ExitCode(), out.String())
	}
}

func TestNewRunCmdRegistersAllDevrunFlags(t *testing.T) {
	cmd := newRunCmd()
	for _, name := range []string{"name", "project", "scan", "quiet", "no-issues", "redact-env", "no-source-maps", "store"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s is not registered on `monitor run`", name)
		}
	}
}

func TestNewRunCmdHelpDescribesBothModes(t *testing.T) {
	cmd := newRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--help: %v", err)
	}
	help := out.String()
	for _, want := range []string{"glyphrun", "MONITOR_LAUNCH", "run [flags] -- <cmd>"} {
		if !strings.Contains(help, want) {
			t.Errorf("run --help output missing %q:\n%s", want, help)
		}
	}
}
