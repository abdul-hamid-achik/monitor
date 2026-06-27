package kill

import (
	"os/exec"
	"testing"
	"time"
)

// TestKillTerminatesChild spawns a real child and verifies Kill actually
// delivers the signal (the happy path was previously untested).
func TestKillTerminatesChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	pid := int32(cmd.Process.Pid)

	if err := Kill(pid, true); err != nil {
		t.Fatalf("Kill(%d) returned error: %v", pid, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done: // process exited (killed) — success
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not exit after Kill")
	}
}

func TestKillInvalidPID(t *testing.T) {
	if err := Kill(0, false); err == nil {
		t.Error("Kill(0) should error")
	}
	if err := Kill(-1, false); err == nil {
		t.Error("Kill(-1) should error")
	}
}

func TestCheckSafetyFlagsProtectedPID(t *testing.T) {
	// PID 1 is launchd/init on every Unix.
	conf := CheckSafety([]int32{1})
	if !conf.HasProtected {
		t.Error("PID 1 should be flagged protected")
	}
	if len(conf.SafetyWarnings) == 0 {
		t.Error("PID 1 should produce safety warnings")
	}
}

func TestCheckSafetyNoProcesses(t *testing.T) {
	conf := CheckSafety(nil)
	if conf.HasProtected || conf.HasSystem {
		t.Error("empty input should not be flagged")
	}
	if len(conf.Processes) != 0 {
		t.Errorf("Processes len = %d, want 0", len(conf.Processes))
	}
}