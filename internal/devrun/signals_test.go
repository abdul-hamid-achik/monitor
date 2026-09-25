//go:build unix

package devrun

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain keeps the test binary itself alive for the whole package run.
// The forwarding tests below deliberately signal their OWN process
// (syscall.Kill(os.Getpid(), ...)), and each forwardSignals under test
// only holds a signal.Notify registration inside its own window -- a
// SIGTERM or SIGHUP landing outside every window (a stray sender on a
// shared CI runner, a runtime delivery edge) otherwise kills the binary
// with Go's default disposition, surfacing as a bare "signal: terminated"
// with zero FAIL lines (observed on ubuntu CI). Go broadcasts notified
// signals to EVERY registered channel, so this package-level registration
// changes no test's semantics: each forwardSignals still receives its own
// copy and the assertions still observe the real forwarding behavior. The
// guard channel is never drained and never stopped: a full channel only
// drops that spare copy, and survival needs registration, not receipt.
func TestMain(m *testing.M) {
	signal.Notify(make(chan os.Signal, 64), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	os.Exit(m.Run())
}

// tempFileStdout gives cmd.Stdout a real *os.File (not an in-memory
// io.Writer): the test scripts below background a "sleep" job so their
// trap is promptly interruptible (see the doc comments on each test for
// why), which orphans that sleep as a grandchild still holding the SAME
// stdout fd for its own remaining lifetime once the shell itself exits. An
// io.Writer that is not an *os.File makes exec.Cmd spawn its own internal
// copy goroutine and makes Wait() block until that pipe's write end is
// closed by every holder -- including the orphan -- which is exactly the
// "exec: WaitDelay expired before I/O complete" hang this sidesteps. A
// plain *os.File is handed to the child directly with no such goroutine.
func tempFileStdout(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stdout.txt"))
	if err != nil {
		t.Fatalf("create temp stdout: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func readFile(t *testing.T, f *os.File) string {
	t.Helper()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read %s: %v", f.Name(), err)
	}
	return string(data)
}

// waitForPIDFile polls path until it contains a positive pid, so tests
// synchronize on the shell having spawned its background sleep (and,
// in the trap scripts below, installed its signal handlers) instead of
// sleeping a fixed "let the child get ready" window that a loaded CI
// runner can overrun.
func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(bytes.TrimSpace(data))); s != "" {
				if pid, perr := strconv.Atoi(s); perr == nil && pid > 0 {
					return pid
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pidfile %s never contained a pid", path)
	return 0
}

// trapScript builds the shell script the forwarding tests use: a shell
// that backgrounds a sleep and waits on it, with a TERM trap that reports
// and exits 7 -- proving the forwarded signal reached the shell itself,
// not just its descendants. The sleep runs with SIGTERM IGNORED (an
// ignored disposition survives across fork+exec, so emptying the trap
// before spawning it suffices): deliverSignal's ttyShared walk signals
// deepest-first, and a TERM-receptive sleep would die first and let the
// shell's bare `wait` return 0 and exit cleanly before its own pending
// TERM trap ever fires -- dash's argument-less `wait` reports 0 even
// when the reaped job was signal-killed, which is exactly the "exit
// code = 0, want 7" flake this guards against. The trap kills the sleep
// with SIGKILL (unkillable sleep, no orphan) before exiting 7. The
// pidfile write comes AFTER the handler installs, so a test that has
// observed the pidfile knows the shell is fully armed. Pass withINT to
// prepend the SIGINT trap the skip test asserts never fires.
func trapScript(pidFile string, withINT bool) string {
	prefix := ""
	if withINT {
		prefix = "trap 'echo caught-int; exit 8' INT; "
	}
	return prefix + "trap '' TERM; sleep 30 & SP=$!; " +
		"trap 'kill -KILL $SP 2>/dev/null; echo caught-term; exit 7' TERM; " +
		"echo $SP > " + pidFile + "; wait"
}

func TestConfigureProcessGroupSharedWhenTTY(t *testing.T) {
	cmd := exec.Command("true")
	configureProcessGroup(cmd, true)
	if cmd.SysProcAttr != nil {
		t.Errorf("SysProcAttr = %+v, want nil when stdin is a TTY (child shares monitor's process group)", cmd.SysProcAttr)
	}
}

func TestConfigureProcessGroupSetpgidWhenNotTTY(t *testing.T) {
	cmd := exec.Command("true")
	configureProcessGroup(cmd, false)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Errorf("SysProcAttr = %+v, want Setpgid:true when stdin is not a TTY", cmd.SysProcAttr)
	}
}

func TestExitCodeForNormalExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 3")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected exit 3 to produce a non-nil error from Run")
	}
	got := exitCodeFor(cmd.ProcessState)
	if got != 3 {
		t.Errorf("exitCodeFor = %d, want 3", got)
	}
}

func TestExitCodeForSuccess(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := exitCodeFor(cmd.ProcessState); got != 0 {
		t.Errorf("exitCodeFor = %d, want 0", got)
	}
}

func TestExitCodeForSignalKilled(t *testing.T) {
	// The child sends itself SIGTERM (15) with no trap installed, so the
	// default disposition kills it: exitCodeFor must report 128+15=143,
	// the same convention a shell uses.
	cmd := exec.Command("sh", "-c", "kill -TERM $$; sleep 5")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected the self-SIGTERM to produce a non-nil error from Run")
	}
	got := exitCodeFor(cmd.ProcessState)
	if got != 143 {
		t.Errorf("exitCodeFor = %d, want 143 (128+SIGTERM)", got)
	}
}

func TestExitCodeForNilState(t *testing.T) {
	if got := exitCodeFor(nil); got != -1 {
		t.Errorf("exitCodeFor(nil) = %d, want -1", got)
	}
}

// TestForwardSignalsAlwaysRelaysSigterm is the TTY/process-group unit test
// the roadmap's done-when list asks for: SIGTERM must reach the child
// regardless of ttyShared, since `kill <monitor-pid>` never reaches a
// separate process group on its own. It runs devrun's actual forwardSignals
// against a real child process (no monitor subprocess needed: the SIGTERM
// is sent to THIS test process's own pid, exactly what forwardSignals'
// signal.Notify listens for).
func TestForwardSignalsAlwaysRelaysSigterm(t *testing.T) {
	for _, ttyShared := range []bool{true, false} {
		t.Run(boolLabel("ttyShared", ttyShared), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "sleep.pid")
			cmd := exec.CommandContext(context.Background(), "sh", "-c", trapScript(pidFile, false))
			out := tempFileStdout(t)
			cmd.Stdout = out
			configureProcessGroup(cmd, ttyShared)
			if err := cmd.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}

			done := make(chan struct{})
			go forwardSignals(cmd, ttyShared, done)

			waitForPIDFile(t, pidFile) // the shell is armed once the pidfile lands
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				t.Fatalf("Kill(self, SIGTERM): %v", err)
			}

			waitDone := make(chan error, 1)
			go func() { waitDone <- cmd.Wait() }()
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				close(done)
				t.Fatal("child did not exit within 5s of SIGTERM -- forwardSignals likely failed to relay it")
			}
			close(done)

			if got := exitCodeFor(cmd.ProcessState); got != 7 {
				t.Errorf("exit code = %d, want 7 (the child's own TERM trap)", got)
			}
			if got := readFile(t, out); !strings.Contains(got, "caught-term") {
				t.Errorf("child stdout = %q, want it to show its TERM trap fired", got)
			}
		})
	}
}

// TestForwardSignalsSkipsSigintWhenTTYShared verifies the other half of the
// TTY rule: with ttyShared true (the naming ADR's
// "the kernel already delivers SIGINT to both processes directly, monitor
// must not forward it a second time"), forwardSignals must NOT call
// cmd.Process.Signal for a SIGINT it receives -- the child must still be
// alive afterward. A later SIGTERM (always forwarded) is used to end the
// child cleanly and confirm forwardSignals itself is still working.
func TestForwardSignalsSkipsSigintWhenTTYShared(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	cmd := exec.CommandContext(context.Background(), "sh", "-c", trapScript(pidFile, true))
	out := tempFileStdout(t)
	cmd.Stdout = out
	configureProcessGroup(cmd, true) // ttyShared

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan struct{})
	go forwardSignals(cmd, true, done)
	defer close(done)

	waitForPIDFile(t, pidFile) // the shell is armed once the pidfile lands
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("Kill(self, SIGINT): %v", err)
	}
	// Negative-observation window, not readiness: a (broken) forwarded
	// SIGINT needs time to arrive and kill the child, so absence can
	// only be asserted after waiting.
	time.Sleep(300 * time.Millisecond)

	if cmd.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("child exited after SIGINT under ttyShared=true; forwardSignals must not forward SIGINT in this case")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("Kill(self, SIGTERM): %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not exit after SIGTERM either -- forwardSignals appears broken, not just correctly skipping SIGINT")
	}
	if got := exitCodeFor(cmd.ProcessState); got != 7 {
		t.Errorf("exit code = %d, want 7 (TERM trap, proving INT was never delivered)", got)
	}
}

// TestForwardSignalsReachesWholeProcessGroupWhenNotTTYShared is the
// process-group signal-forwarding fix: when Setpgid is on (stdin is not a
// TTY), a signal forwarded to the child must reach the WHOLE new process
// group, not just cmd.Process's own pid -- a wrapper child (`go run .`,
// `sh -c '...'`) commonly forks or execs a real leaf process that inherits
// that same group, and `kill -TERM <monitor-pid>` can never target a
// separate group on its own. This proves it against a real grandchild
// (`sleep`, backgrounded by a shell with no signal trap of its own): with
// only the direct child (the shell) signaled, the grandchild would survive
// for its full 100s sleep.
func TestForwardSignalsReachesWholeProcessGroupWhenNotTTYShared(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.CommandContext(context.Background(), "sh", "-c",
		"sleep 100 & echo $! > "+pidFile+"; wait")
	configureProcessGroup(cmd, false) // Setpgid: the shell AND its background sleep share a NEW group.
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go forwardSignals(cmd, false, done)

	grandchildPID := waitForPIDFile(t, pidFile)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("Kill(self, SIGTERM): %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		close(done)
		t.Fatal("the direct child did not exit within 5s of the forwarded SIGTERM")
	}
	close(done)

	// The grandchild shares the new process group; only a group-wide kill
	// (not a direct-pid signal to the shell) reaches it. Poll briefly for
	// the kernel to finish reaping it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(grandchildPID, 0); err != nil {
			return // ESRCH: gone. Success.
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(grandchildPID, syscall.SIGKILL) // don't leak a sleep(100) past this test
	t.Errorf("grandchild pid %d is still alive 2s after the forwarded SIGTERM -- it must reach the whole process group, not just the direct child", grandchildPID)
}

// TestForwardSignalsTtySharedReachesDescendants is CC-9's ttyShared fix:
// when monitor's stdin is a terminal the child stays in monitor's OWN
// process group, so a forwarded SIGTERM/SIGHUP cannot be a group kill
// (Kill(0) would hit monitor itself, pipeline siblings and a non-job-
// control parent shell). It must instead reach cmd.Process AND every
// DESCENDANT of it, taken from a process-tree snapshot, leaves first --
// otherwise a wrapper child (`sh -c 'node srv.js'`) dies without passing
// the signal on and leaves the real server orphaned, still holding its
// port. This proves it with a real grandchild (`sleep`, backgrounded by
// the shell with no trap of its own, exactly the repro's shape): the shell
// is signaled last, and only the descendant walk can reach the sleep.
func TestForwardSignalsTtySharedReachesDescendants(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{"sigterm", syscall.SIGTERM},
		{"sighup", syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
			// No trap on the shell: it must die on the forwarded signal
			// like an ordinary wrapper would, not cooperate with us.
			cmd := exec.CommandContext(context.Background(), "sh", "-c",
				"sleep 100 & echo $! > "+pidFile+"; wait")
			out := tempFileStdout(t)
			cmd.Stdout = out
			configureProcessGroup(cmd, true) // ttyShared: SysProcAttr stays nil, the child shares OUR group.
			if err := cmd.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}

			done := make(chan struct{})
			go forwardSignals(cmd, true, done)

			grandchildPID := waitForPIDFile(t, pidFile)

			// Exactly what a user's `kill <monitor-pid>` does: only
			// monitor's own pid, never the shared group.
			if err := syscall.Kill(os.Getpid(), tc.sig); err != nil {
				t.Fatalf("Kill(self, %v): %v", tc.sig, err)
			}

			waitDone := make(chan error, 1)
			go func() { waitDone <- cmd.Wait() }()
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				close(done)
				t.Fatalf("the direct child did not exit within 5s of the forwarded %v", tc.sig)
			}
			close(done)

			// The grandchild shares monitor's process group, so only the
			// descendant walk (not the self-pid kill above) can have
			// reached it. Poll for the kernel to finish reaping it.
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if err := syscall.Kill(grandchildPID, 0); err != nil {
					return // ESRCH: gone. Success.
				}
				time.Sleep(20 * time.Millisecond)
			}
			_ = syscall.Kill(grandchildPID, syscall.SIGKILL) // don't leak a sleep(100) past this test
			t.Fatalf("grandchild pid %d is still alive 5s after the forwarded %v -- ttyShared delivery must walk the process tree, not just the direct child (CC-9)", grandchildPID, tc.sig)
		})
	}
}

func boolLabel(name string, v bool) string {
	if v {
		return name + "=true"
	}
	return name + "=false"
}
