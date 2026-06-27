package kill

import "testing"

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