package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/devrun"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func withIsolatedHotServiceRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestResolveHotServiceProjectPrefersExplicitFlag(t *testing.T) {
	if got := resolveHotServiceProject("  acme  "); got != "acme" {
		t.Errorf("resolveHotServiceProject(explicit) = %q, want acme (trimmed)", got)
	}
}

// TestResolveHotServiceProjectFallsBackToWorkingDirNotHost is the PID:1
// regression: this test runs from inside a real git checkout (the monitor
// repo itself), so resolveHotServiceProject("") must resolve a REAL
// project slug from the working directory's git root, never fall through
// to project.Resolve's PID<=0 "host" shortcut -- which is exactly what
// happened before PID:1 was added (verified live: `monitor hot <service>`
// run from the very directory a service was registered from still failed
// to find it).
func TestResolveHotServiceProjectFallsBackToWorkingDirNotHost(t *testing.T) {
	got := resolveHotServiceProject("")
	if got == "host" {
		t.Error(`resolveHotServiceProject("") = "host", want the working directory's real git-root-derived slug (PID<=0 must not short-circuit this)`)
	}
	if got == "" {
		t.Error(`resolveHotServiceProject("") = "", want a non-empty slug when run from inside a real git checkout`)
	}
}

func TestPidIsAliveRejectsNonPositive(t *testing.T) {
	if pidIsAlive(0) {
		t.Error("pidIsAlive(0) = true, want false")
	}
	if pidIsAlive(-1) {
		t.Error("pidIsAlive(-1) = true, want false")
	}
}

func TestPidIsAliveTrueForSelf(t *testing.T) {
	if !pidIsAlive(os.Getpid()) {
		t.Error("pidIsAlive(os.Getpid()) = false, want true (this very test process is alive)")
	}
}

func TestPidIsAliveFalseForAnAlmostCertainlyDeadPID(t *testing.T) {
	if pidIsAlive(999999999) {
		t.Error("pidIsAlive(999999999) = true, want false")
	}
}

func TestLiveRegisteredServiceNamesFiltersStaleEntries(t *testing.T) {
	withIsolatedHotServiceRegistry(t)
	if _, err := devrun.WriteRegistryEntry(devrun.RegistryEntry{
		Name: "alive", Project: "acme", PID: os.Getpid(),
	}); err != nil {
		t.Fatalf("WriteRegistryEntry(alive): %v", err)
	}
	if _, err := devrun.WriteRegistryEntry(devrun.RegistryEntry{
		Name: "dead", Project: "acme", PID: 999999999,
	}); err != nil {
		t.Fatalf("WriteRegistryEntry(dead): %v", err)
	}
	got := liveRegisteredServiceNames("acme")
	if len(got) != 1 || got[0] != "alive" {
		t.Errorf("liveRegisteredServiceNames = %v, want exactly [alive] (the dead-pid entry must be filtered out)", got)
	}
}

func TestLiveRegisteredServiceNamesEmptyForUnknownProject(t *testing.T) {
	withIsolatedHotServiceRegistry(t)
	if got := liveRegisteredServiceNames("no-such-project"); len(got) != 0 {
		t.Errorf("liveRegisteredServiceNames(unknown project) = %v, want empty", got)
	}
}

func TestPrintUnknownServiceListsLiveServicesOnly(t *testing.T) {
	withIsolatedHotServiceRegistry(t)
	if _, err := devrun.WriteRegistryEntry(devrun.RegistryEntry{
		Name: "web-api", Project: "acme", PID: os.Getpid(),
	}); err != nil {
		t.Fatalf("WriteRegistryEntry: %v", err)
	}
	if _, err := devrun.WriteRegistryEntry(devrun.RegistryEntry{
		Name: "stale-worker", Project: "acme", PID: 999999999,
	}); err != nil {
		t.Fatalf("WriteRegistryEntry: %v", err)
	}

	cmd := newHotCmd()
	var buf strings.Builder
	cmd.SetErr(&buf)
	printUnknownService(cmd, "acme", "bogus")

	out := buf.String()
	if !strings.Contains(out, `no service named "bogus"`) {
		t.Errorf("output = %q, want the requested name quoted", out)
	}
	if !strings.Contains(out, "web-api") {
		t.Errorf("output = %q, want the live service listed", out)
	}
	if strings.Contains(out, "stale-worker") {
		t.Errorf("output = %q, must not list the dead-pid (stale) entry", out)
	}
}

func TestPrintUnknownServiceNoServicesRegisteredAtAll(t *testing.T) {
	withIsolatedHotServiceRegistry(t)
	cmd := newHotCmd()
	var buf strings.Builder
	cmd.SetErr(&buf)
	printUnknownService(cmd, "acme", "bogus")
	out := buf.String()
	if !strings.Contains(out, "nothing is currently registered") {
		t.Errorf("output = %q, want an honest \"nothing registered\" message", out)
	}
	if !strings.Contains(out, "monitor run --name bogus -- <cmd>") {
		t.Errorf("output = %q, want a next-step suggestion naming the requested service", out)
	}
}

func TestRegisteredInspectAddrMatchesByPID(t *testing.T) {
	entry := devrun.RegistryEntry{
		Inspectors: []devrun.RegistryInspector{
			{PID: 111, Port: 9229, WS: "ws://127.0.0.1:9229/a"},
			{PID: 222, Port: 9230, WS: "ws://127.0.0.1:9230/b"},
		},
	}
	addr, ok := registeredInspectAddr(entry, 222)
	if !ok || addr != "127.0.0.1:9230" {
		t.Errorf("registeredInspectAddr = (%q, %v), want (127.0.0.1:9230, true)", addr, ok)
	}
}

func TestRegisteredInspectAddrNoMatch(t *testing.T) {
	entry := devrun.RegistryEntry{
		Inspectors: []devrun.RegistryInspector{{PID: 111, Port: 9229}},
	}
	if _, ok := registeredInspectAddr(entry, 999); ok {
		t.Error("registeredInspectAddr matched a pid with no corresponding inspector entry")
	}
}

func TestRegisteredInspectAddrIgnoresUnresolvedPID(t *testing.T) {
	// PID: 0 means the banner's port owner could not be resolved
	// (InspectorBanner.PIDKnown was false) -- it must never be treated as
	// a match for pid 0 itself, which is never a real leaf pid.
	entry := devrun.RegistryEntry{
		Inspectors: []devrun.RegistryInspector{{PID: 0, Port: 9229}},
	}
	if _, ok := registeredInspectAddr(entry, 0); ok {
		t.Error("registeredInspectAddr matched an unresolved-pid inspector entry")
	}
}

func TestHotServiceHeaderLineDirectLaunchOmitsChildClause(t *testing.T) {
	binding := procbind.Binding{PID: 100, Runtime: procbind.RuntimeNode}
	line := hotServiceHeaderLine(context.Background(), "workload", 100, binding, "inspector_cpu", "", "", profiler.HeatCPU, 5*time.Second)
	if strings.Contains(line, "child of") {
		t.Errorf("header = %q, must not print a child-of clause when the launched pid IS the leaf", line)
	}
	if !strings.HasPrefix(line, "monitor > workload = node pid 100") {
		t.Errorf("header = %q, want it to start with the service name and runtime/pid", line)
	}
	if !strings.Contains(line, "sampling 5s") {
		t.Errorf("header = %q, missing the sampling duration clause", line)
	}
}

func TestHotServiceHeaderLineWrapperWithRegisteredInspector(t *testing.T) {
	binding := procbind.Binding{PID: 200, Runtime: procbind.RuntimeNode}
	// launchedPID (100, the registered/launched pid) differs from
	// binding.PID (200, the resolved leaf) -- a wrapper (e.g. yarn) was
	// skipped by procbind.ResolveLeaf.
	line := hotServiceHeaderLine(context.Background(), "workload", 100, binding, "inspector_cpu", "", "127.0.0.1:53817", profiler.HeatCPU, 5*time.Second)
	if !strings.Contains(line, "child of") || !strings.Contains(line, "100") {
		t.Errorf("header = %q, want a child-of clause naming the launched pid 100", line)
	}
	if !strings.Contains(line, "monitor run --inspect") {
		t.Errorf("header = %q, want the \"; monitor run --inspect\" annotation for a registry-sourced inspector", line)
	}
	if !strings.Contains(line, "inspector 127.0.0.1:53817") {
		t.Errorf("header = %q, want the registered inspector address shown", line)
	}
}

func TestHotServiceNextHintIncludesNonDefaultFlags(t *testing.T) {
	got := hotServiceNextHint("workload", "heap", 10*time.Second, true)
	if !strings.Contains(got, "monitor hot workload") {
		t.Errorf("next hint = %q, want the service name as the base command", got)
	}
	if !strings.Contains(got, "--type heap") {
		t.Errorf("next hint = %q, missing --type heap", got)
	}
	if !strings.Contains(got, "--duration 10s") {
		t.Errorf("next hint = %q, missing --duration 10s", got)
	}
}

func TestHotServiceNextHintDefaultsOmitFlags(t *testing.T) {
	got := hotServiceNextHint("workload", "cpu", 5*time.Second, false)
	if got != "monitor hot workload" {
		t.Errorf("next hint = %q, want exactly the bare service command for every-default invocation", got)
	}
}

// hotServiceExitTwoSubprocessEnv, when set to "1", tells this test binary
// (re-invoked as a subprocess below) to run `monitor hot bogus-service`
// against an isolated, empty registry and let its os.Exit(2) actually
// terminate the subprocess, instead of running the whole test suite --
// the exact re-exec pattern run_test.go's
// TestNewRunCmdDashModePropagatesNonZeroExitCode uses for its own
// os.Exit(result.ExitCode) branch, applied here to hot_service.go's
// os.Exit(2) unknown-service path (resolve_test.go's convention: unit
// tests above prove the pieces; this ONE subprocess test proves the
// wiring, since os.Exit would otherwise kill the whole `go test` binary).
const hotServiceExitTwoSubprocessEnv = "MONITOR_HOT_SERVICE_TEST_SUBPROCESS"

func TestHotUnknownServiceExitsTwo(t *testing.T) {
	if os.Getenv(hotServiceExitTwoSubprocessEnv) == "1" {
		cmd := newHotCmd()
		cmd.SetOut(os.Stdout)
		cmd.SetErr(os.Stderr)
		cmd.SetArgs([]string{"bogus-service"})
		_ = cmd.Execute()
		// If os.Exit(2) did not already terminate the process above, the
		// unknown-service branch did not fire -- exit a code the parent
		// below does not expect (9), so a regression is a definite
		// failure rather than an accidental pass.
		os.Exit(9)
	}

	execCmd := exec.Command(os.Args[0], "-test.run=^TestHotUnknownServiceExitsTwo$", "-test.v")
	execCmd.Env = append(os.Environ(),
		hotServiceExitTwoSubprocessEnv+"=1",
		"XDG_STATE_HOME="+t.TempDir(),
	)
	var out bytes.Buffer
	execCmd.Stdout, execCmd.Stderr = &out, &out
	err := execCmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("subprocess did not exit with an *exec.ExitError (got %v); output:\n%s", err, out.String())
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2; output:\n%s", exitErr.ExitCode(), out.String())
	}
	if !strings.Contains(out.String(), `no service named "bogus-service"`) {
		t.Errorf("output = %s, want the unknown-service message", out.String())
	}
}

// parsePIDRoundTrips is a tiny sanity check that this file's dispatch
// assumption (parsePID rejects a symbolic name) holds for the exact
// strings these tests use.
func TestParsePIDRejectsServiceNames(t *testing.T) {
	for _, name := range []string{"bogus-service", "workload", "web-api"} {
		if _, err := parsePID(name); err == nil {
			t.Errorf("parsePID(%q) succeeded, want an error (it must dispatch to service-name lookup)", name)
		}
	}
	if _, err := parsePID(strconv.Itoa(os.Getpid())); err != nil {
		t.Errorf("parsePID(a real pid string) failed: %v", err)
	}
}
