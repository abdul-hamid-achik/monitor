//go:build unix

package devrun

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeSignalExitScript is a minimal, self-authored stand-in for the real
// signal-exit@4.x npm package (verified live separately against the actual
// package, node_modules and all -- see profile.go's own doc comment on
// exitShimScript for that evidence), used here so this regression test
// stays fully offline/portable. It reproduces the TWO mechanisms this
// package's exit shim has to interoperate with, faithfully enough to
// exercise the real interop bug:
//
//  1. it registers its own process-level signal listener and marks its
//     presence via the exact same
//     globalThis[Symbol.for('signal-exit emitter')] convention the real
//     package uses (that symbol, and the {count} shape read off it, IS
//     exitShimScript's own detection mechanism, so this fixture's
//     fidelity to it is what makes the test meaningful) -- when it
//     decides it is the sole listener left for a signal, it runs its own
//     cleanup and re-raises the raw signal itself;
//  2. separately (and this is the part a naive fixture misses, and the
//     part that actually matters here): it patches process.exit itself
//     to run that same cleanup first -- the real package's actual
//     mechanism for guaranteeing its onExit callbacks (e.g.
//     restore-cursor's terminal cleanup) still run when something ELSE
//     (this package's own shim) is the one that ends up calling
//     process.exit(), which is exactly what happens once there are 2+
//     listeners and signal-exit's OWN signal listener (mechanism 1
//     above) declines to act.
const fakeSignalExitScript = `'use strict';
var fs = require('fs');
var marker = process.env.MONITOR_TEST_MARKER;
var se = globalThis[Symbol.for('signal-exit emitter')] || { count: 0 };
se.count += 1;
globalThis[Symbol.for('signal-exit emitter')] = se;
var cleaned = false;
function cleanup() {
  if (cleaned) return;
  cleaned = true;
  fs.writeFileSync(marker, 'cleanup-ran\n');
}
var origExit = process.exit.bind(process);
process.exit = function (code) {
  cleanup();
  return origExit(code);
};
function install(sig) {
  process.on(sig, function () {
    if (process.listeners(sig).length === se.count) {
      cleanup();
      process.kill(process.pid, sig);
    }
  });
}
install('SIGINT');
install('SIGTERM');
`

// fakeSignalExitAppScript is the "application" under test: it loads
// fakeSignalExitScript (mimicking an app that pulled in signal-exit
// transitively via execa/ora/foreground-child/write-file-atomic, none of
// which it ever references directly) and otherwise registers no signal
// handler of its OWN at all -- the common case the exit shim exists to
// finish for --profile.
const fakeSignalExitAppScript = `require(process.env.MONITOR_TEST_SIGEXIT_FIXTURE);
console.error('ready pid=' + process.pid);
setInterval(function () {}, 1000);
`

func writeTestScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// runShimAgainstSignalExitFixture launches runtime (node or bun) with
// exitShimScript loaded via requireFlag alongside fakeSignalExitScript,
// sends SIGINT once "ready" is observed, and reports whether the process
// exited on its own within the deadline (vs. having to be SIGKILLed) and
// whether the fixture's own marker file (standing in for a real
// onExit-style cleanup callback) was written.
func runShimAgainstSignalExitFixture(t *testing.T, runtime, optionsEnvName, requireFlag string) (exited bool, exitErr error, cleanupRan bool) {
	t.Helper()
	if _, err := exec.LookPath(runtime); err != nil {
		t.Skipf("%s not found on PATH: %v", runtime, err)
	}

	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	shim, err := exitShimPath()
	if err != nil {
		t.Fatalf("exitShimPath: %v", err)
	}
	fixture := writeTestScript(t, dir, "fake-signal-exit.cjs", fakeSignalExitScript)
	app := writeTestScript(t, dir, "app.cjs", fakeSignalExitAppScript)
	marker := filepath.Join(dir, "marker.txt")

	cmd := exec.Command(runtime, app)
	cmd.Env = append(os.Environ(),
		optionsEnvName+"="+requireFlag+" "+shim,
		"MONITOR_TEST_SIGEXIT_FIXTURE="+fixture,
		"MONITOR_TEST_MARKER="+marker,
	)
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	cmd.Stdout = stderrW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", runtime, err)
	}
	_ = stderrW.Close()

	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		var acc strings.Builder
		for {
			n, rerr := stderrR.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
				if strings.Contains(acc.String(), "ready pid=") {
					close(ready)
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("%s never printed its ready line", runtime)
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case waitErr := <-done:
		exited = true
		exitErr = waitErr
	case <-time.After(5 * time.Second):
		exited = false
		_ = cmd.Process.Kill()
		<-done
	}

	if _, statErr := os.Stat(marker); statErr == nil {
		cleanupRan = true
	}
	return exited, exitErr, cleanupRan
}

// TestExitShimResolvesSignalExitInteropNode is the BLOCKER regression test
// (node): without this fix, a shim that only exits when it is the SOLE
// SIGINT listener deadlocks against a signal-exit-style dependency, which
// itself only re-raises the signal when there are no OTHER listeners --
// each defers to the other and the process never terminates on SIGINT at
// all (verified live against the real signal-exit@4.1.0 package; see
// exitShimScript's own doc comment). With the fix, both this shim's
// process.exit() call AND the fixture's own "cleanup" (marker file) must
// still happen.
func TestExitShimResolvesSignalExitInteropNode(t *testing.T) {
	exited, _, cleanupRan := runShimAgainstSignalExitFixture(t, "node", "NODE_OPTIONS", "--require")
	if !exited {
		t.Fatal("process did not exit on its own within the deadline (deadlock reproduced) -- required SIGKILL")
	}
	if !cleanupRan {
		t.Error("the signal-exit-style fixture's own cleanup never ran -- process.exit() must still let it flush")
	}
}

// TestExitShimResolvesSignalExitInteropBun is the same regression, under
// Bun (BUN_OPTIONS/--preload instead of NODE_OPTIONS/--require) -- verified
// live to deadlock identically to Node without this fix.
func TestExitShimResolvesSignalExitInteropBun(t *testing.T) {
	exited, _, cleanupRan := runShimAgainstSignalExitFixture(t, "bun", "BUN_OPTIONS", "--preload")
	if !exited {
		t.Fatal("process did not exit on its own within the deadline (deadlock reproduced) -- required SIGKILL")
	}
	if !cleanupRan {
		t.Error("the signal-exit-style fixture's own cleanup never ran -- process.exit() must still let it flush")
	}
}

// TestExitShimStillDefersToARealApplicationHandler is the companion
// safety test: an application-level handler ADDITIONAL to the
// signal-exit-style listener must still run to completion, unmolested --
// proving the fix does not simply always exit once 2+ listeners are seen.
func TestExitShimStillDefersToARealApplicationHandler(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node not found on PATH: %v", err)
	}
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	shim, err := exitShimPath()
	if err != nil {
		t.Fatalf("exitShimPath: %v", err)
	}
	fixture := writeTestScript(t, dir, "fake-signal-exit.cjs", fakeSignalExitScript)
	marker := filepath.Join(dir, "marker.txt")
	ownMarker := filepath.Join(dir, "own-handler-ran.txt")
	app := writeTestScript(t, dir, "app_own_handler.cjs", `require(process.env.MONITOR_TEST_SIGEXIT_FIXTURE);
var fs = require('fs');
process.on('SIGINT', function () {
  fs.writeFileSync(process.env.MONITOR_TEST_OWN_MARKER, 'own-handler-ran\n');
  process.exitCode = 7;
  process.exit(7);
});
console.error('ready pid=' + process.pid);
setInterval(function () {}, 1000);
`)

	cmd := exec.Command("node", app)
	cmd.Env = append(os.Environ(),
		"NODE_OPTIONS=--require "+shim,
		"MONITOR_TEST_SIGEXIT_FIXTURE="+fixture,
		"MONITOR_TEST_MARKER="+marker,
		"MONITOR_TEST_OWN_MARKER="+ownMarker,
	)
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	cmd.Stdout = stderrW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start node: %v", err)
	}
	_ = stderrW.Close()

	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		var acc strings.Builder
		for {
			n, rerr := stderrR.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
				if strings.Contains(acc.String(), "ready pid=") {
					close(ready)
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("node never printed its ready line")
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("process did not exit within the deadline")
	}

	if _, err := os.Stat(ownMarker); err != nil {
		t.Error("the application's OWN SIGINT handler never ran -- the shim must defer to it, not override it")
	}
	exitCode := cmd.ProcessState.ExitCode()
	if exitCode != 7 {
		t.Errorf("exit code = %d, want 7 (the application's own exit code, preserved)", exitCode)
	}
}
