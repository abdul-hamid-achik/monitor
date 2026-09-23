//go:build unix

package devrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
			cmd := exec.CommandContext(context.Background(), "sh", "-c",
				`trap 'kill %1 2>/dev/null; echo caught-term; exit 7' TERM; sleep 10 & wait`)
			out := tempFileStdout(t)
			cmd.Stdout = out
			configureProcessGroup(cmd, ttyShared)
			if err := cmd.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}

			done := make(chan struct{})
			go forwardSignals(cmd, ttyShared, done)

			time.Sleep(300 * time.Millisecond) // let the child install its trap
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
// TTY rule: with ttyShared true (docs/contracts/local-sentry-naming.md's
// "the kernel already delivers SIGINT to both processes directly, monitor
// must not forward it a second time"), forwardSignals must NOT call
// cmd.Process.Signal for a SIGINT it receives -- the child must still be
// alive afterward. A later SIGTERM (always forwarded) is used to end the
// child cleanly and confirm forwardSignals itself is still working.
func TestForwardSignalsSkipsSigintWhenTTYShared(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c",
		`trap 'echo caught-int; exit 8' INT; trap 'kill %1 2>/dev/null; echo caught-term; exit 7' TERM; sleep 10 & wait`)
	out := tempFileStdout(t)
	cmd.Stdout = out
	configureProcessGroup(cmd, true) // ttyShared

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan struct{})
	go forwardSignals(cmd, true, done)
	defer close(done)

	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("Kill(self, SIGINT): %v", err)
	}
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

func boolLabel(name string, v bool) string {
	if v {
		return name + "=true"
	}
	return name + "=false"
}
