package system

import (
	"testing"
)

func TestProtectedProcessNames(t *testing.T) {
	// Test that critical processes are in the protected list
	criticalProcesses := []string{
		"launchd",
		"kernel_task",
		"init",
		"System",
	}

	for _, proc := range criticalProcesses {
		if !ProtectedProcessNames[proc] {
			t.Errorf("Expected %s to be in ProtectedProcessNames", proc)
		}
	}
}

func TestCriticalProcessNames(t *testing.T) {
	// Test that only the most critical processes are in critical list
	if !CriticalProcessNames["launchd"] {
		t.Error("launchd should be in CriticalProcessNames")
	}
	if !CriticalProcessNames["kernel_task"] {
		t.Error("kernel_task should be in CriticalProcessNames")
	}
	if !CriticalProcessNames["init"] {
		t.Error("init should be in CriticalProcessNames")
	}
}

func TestKillProcess_InvalidPID(t *testing.T) {
	// Test killing invalid PID returns error
	err := KillProcess(0, false)
	if err == nil {
		t.Error("Expected error when killing PID 0")
	}

	err = KillProcess(-1, false)
	if err == nil {
		t.Error("Expected error when killing negative PID")
	}
}

func TestCheckKillSafety(t *testing.T) {
	// Test with a protected PID (PID 1 is always launchd/init)
	result := CheckKillSafety([]int32{1})

	if !result.HasProtected {
		t.Error("Expected PID 1 to be flagged as protected")
	}

	if len(result.SafetyWarnings) == 0 {
		t.Error("Expected safety warnings for PID 1")
	}
}
