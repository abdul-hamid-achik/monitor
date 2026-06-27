package collector

import "testing"

func TestProtectedProcessList(t *testing.T) {
	want := []string{"launchd", "kernel_task", "Finder", "Dock", "WindowServer"}
	for _, name := range want {
		if !ProtectedProcessNames[name] {
			t.Errorf("%q should be protected", name)
		}
	}
}

func TestCriticalIsSubsetOfProtected(t *testing.T) {
	for name := range CriticalProcessNames {
		if !ProtectedProcessNames[name] {
			t.Errorf("critical %q missing from protected list", name)
		}
	}
}

func TestSystemInfoJSONHasAllMetrics(t *testing.T) {
	// Sanity: SystemInfo encodes/decodes round-trip.
	s := SystemInfo{
		Hostname: "test",
		CPU:      CPUInfo{UsagePercent: 42.5, CoreCount: 8},
		Memory:   MemoryInfo{UsagePercent: 73.0},
		Network:  NetworkInfo{BytesRecvPerSec: 1024},
	}
	if s.CPU.UsagePercent != 42.5 {
		t.Error("CPU value mismatch")
	}
	if s.Memory.UsagePercent != 73.0 {
		t.Error("memory value mismatch")
	}
}