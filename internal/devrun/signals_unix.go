//go:build unix

package devrun

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/x/term"
)

// StdinIsTTY reports whether monitor's own stdin is a terminal -- the
// signal `monitor run --` uses to decide its process-group strategy
// (docs/contracts/local-sentry-naming.md's E2.4 "process group" rule).
func StdinIsTTY() bool {
	return term.IsTerminal(os.Stdin.Fd())
}

// configureProcessGroup sets cmd's SysProcAttr per the TTY rule: when
// monitor's stdin is a terminal, the child stays in monitor's own process
// group (SysProcAttr left nil, exec's default) so the kernel delivers a
// terminal-generated SIGINT (Ctrl-C) to both processes directly -- no
// forwarding from monitor required, and monitor must NOT forward it a
// second time (see forwardSignals). Otherwise the child is given its own
// process group (Setpgid), since a signal sent to only monitor's PID would
// otherwise never reach it automatically; forwardSignals then relays SIGINT
// explicitly in that case.
func configureProcessGroup(cmd *exec.Cmd, ttyShared bool) {
	if ttyShared {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// forwardSignals relays signals received by monitor itself to the child
// process for as long as done is open (docs/contracts/
// local-sentry-naming.md's E2.4 "process group" rule):
//
//   - SIGTERM and SIGHUP are ALWAYS forwarded, regardless of ttyShared:
//     `kill <monitor-pid>` targets only monitor's own PID, never the child,
//     so without this the child would be orphaned instead of shut down
//     alongside monitor.
//   - SIGINT is forwarded only when ttyShared is false. When stdin is a
//     terminal and the child shares monitor's process group, the kernel
//     already delivers a Ctrl-C-generated SIGINT to BOTH processes
//     directly; forwarding it again here would be redundant at best.
//
// This function still calls signal.Notify for SIGINT in the ttyShared case
// too, so monitor itself does not fall to Go's default fatal-terminate-on-
// SIGINT disposition before it has had a chance to wait for the child and
// print the exit summary -- it just does nothing with that particular
// signal beyond staying alive.
//
// Returns once done is closed.
func forwardSignals(cmd *exec.Cmd, ttyShared bool, done <-chan struct{}) {
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	for {
		select {
		case <-done:
			return
		case sig := <-sigCh:
			if sig == syscall.SIGINT && ttyShared {
				continue
			}
			deliverSignal(cmd, ttyShared, sig)
		}
	}
}

// deliverSignal relays sig to cmd's child. When the child has its own
// process group (configureProcessGroup's Setpgid case, i.e. ttyShared is
// false), the signal is sent to the WHOLE group -- syscall.Kill(-pid,
// sig) -- not just cmd.Process's own pid: a wrapper child (`go run .`,
// `yarn start`, `sh -c '...'`) commonly execs or forks a real leaf process
// that inherits that same new group, and `kill -TERM <monitor-pid>` (which
// can only ever target monitor's own pid, never a separate group it does
// not belong to) or a piped Ctrl-C (a non-TTY stdin, so --scan turned it
// into an ordinary SIGINT delivered only to monitor) must still reach that
// leaf, or it is silently orphaned while monitor waits on it (see
// devrun.go's WaitDelay-bounded reap for the rest of that fix -- this half
// is what makes the signal arrive at all instead of merely bounding the
// hang if it doesn't). In the ttyShared case the child stays in monitor's
// OWN process group (configureProcessGroup leaves SysProcAttr nil), so a
// group kill here would also hit monitor itself; that case keeps signaling
// only cmd.Process directly, unchanged.
func deliverSignal(cmd *exec.Cmd, ttyShared bool, sig os.Signal) {
	if cmd.Process == nil {
		return
	}
	if !ttyShared {
		if sysSig, ok := sig.(syscall.Signal); ok {
			_ = syscall.Kill(-cmd.Process.Pid, sysSig)
			return
		}
	}
	_ = cmd.Process.Signal(sig)
}

// exitCodeFor derives the process exit code from state, matching a shell's
// 128+signal convention for a process terminated by a signal
// (ProcessState.ExitCode() reports -1 in that case on Unix, since there is
// no ordinary exit status to report).
func exitCodeFor(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return -1
}
