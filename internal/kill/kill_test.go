package kill

import (
	"encoding/json"
	"os/exec"
	"strings"
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

// TestKillVerifiedTerminatesChild spawns a real child, lets KillVerified
// SIGTERM it, and asserts the outcome is verified terminated (not just
// "signal sent").
func TestKillVerifiedTerminatesChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	pid := int32(cmd.Process.Pid)
	// Reap the child in the background so it doesn't linger as a zombie
	// and confuse the poll loop's process.Status() read.
	go func() { _ = cmd.Wait() }()

	res, err := KillVerified(pid, false)
	if err != nil {
		t.Fatalf("KillVerified(%d) returned error: %v", pid, err)
	}
	if res.Outcome != OutcomeTerminated {
		t.Errorf("Outcome = %v, want %v", res.Outcome, OutcomeTerminated)
	}
	if res.Signal != "SIGTERM" {
		t.Errorf("Signal = %q, want SIGTERM", res.Signal)
	}
	if res.WaitedMs < 0 || res.WaitedMs > 5000 {
		t.Errorf("WaitedMs = %d, want in [0, 5000]", res.WaitedMs)
	}
}

// TestKillVerifiedStillRunningSuggestsForce spawns a child that ignores
// SIGTERM and verifies KillVerified reports still_running with a next_action
// suggesting force, WITHOUT escalating to SIGKILL itself.
func TestKillVerifiedStillRunningSuggestsForce(t *testing.T) {
	origTimeout, origInterval := verifyTimeout, pollInterval
	verifyTimeout = 400 * time.Millisecond
	pollInterval = 50 * time.Millisecond
	defer func() { verifyTimeout, pollInterval = origTimeout, origInterval }()

	cmd := exec.Command("sh", "-c", `trap "" TERM; while :; do sleep 1; done`)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	pid := int32(cmd.Process.Pid)
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	time.Sleep(200 * time.Millisecond) // let the trap install

	res, err := KillVerified(pid, false)
	if err != nil {
		t.Fatalf("KillVerified(%d) returned error: %v", pid, err)
	}
	if res.Outcome != OutcomeStillRunning {
		t.Errorf("Outcome = %v, want %v", res.Outcome, OutcomeStillRunning)
	}
	if !strings.Contains(res.NextAction, "force") {
		t.Errorf("NextAction = %q, want it to mention force", res.NextAction)
	}
}

func TestKillVerifiedInvalidPID(t *testing.T) {
	for _, pid := range []int32{0, -1} {
		res, err := KillVerified(pid, false)
		if err == nil {
			t.Errorf("KillVerified(%d) should error", pid)
		}
		if res.Outcome != OutcomeUnknown {
			t.Errorf("KillVerified(%d).Outcome = %v, want %v", pid, res.Outcome, OutcomeUnknown)
		}
	}
}

// TestResultJSONFieldNames locks in the snake_case JSON contract for Result.
func TestResultJSONFieldNames(t *testing.T) {
	res := Result{PID: 1, Signal: "SIGTERM", Outcome: OutcomeStillRunning, WaitedMs: 5, NextAction: "x"}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"pid"`, `"signal"`, `"outcome"`, `"waited_ms"`, `"next_action"`} {
		if !strings.Contains(s, want) {
			t.Errorf("json %s missing field %s", s, want)
		}
	}
}
