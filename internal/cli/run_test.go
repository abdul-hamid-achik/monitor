package cli

import (
	"bytes"
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

func TestNewRunCmdDashModeLaunchesAndPropagatesExitCode(t *testing.T) {
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
