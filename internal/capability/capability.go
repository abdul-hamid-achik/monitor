// Package capability centralizes platform and tool capability detection.
package capability

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// State describes whether an operation can be attempted on this host.
type State string

const (
	Supported   State = "supported"
	Unsupported State = "unsupported"
	Unavailable State = "unavailable"
)

// Name identifies a capability checked before collection or capture.
type Name string

const (
	SystemMetrics  Name = "system_metrics"
	ProcessMetrics Name = "process_metrics"
	CPULoadAverage Name = "cpu_load_average"
	CgroupV2       Name = "cgroup_v2"
	ProfilePprof   Name = "profile_pprof"
	ProfileSample  Name = "profile_sample"
)

// Support is the JSON-ready result of one capability check.
type Support struct {
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Set is an immutable-by-convention capability snapshot. Callers may inject a
// Set in tests; production callers should use Detect or Current.
type Set struct {
	OS    string           `json:"os"`
	Items map[Name]Support `json:"items"`
}

// Detector contains the environment seams needed for deterministic tests.
type Detector struct {
	GOOS     string
	LookPath func(string) (string, error)
}

// Current detects capabilities for the running host.
func Current() Set {
	return Detect(Detector{GOOS: runtime.GOOS, LookPath: exec.LookPath})
}

// Detect builds one centralized capability set. Unsupported means the
// platform cannot provide the operation; unavailable means the platform can,
// but a required local tool is currently absent.
func Detect(d Detector) Set {
	goos := strings.TrimSpace(d.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	lookPath := d.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	items := map[Name]Support{
		ProfilePprof: {State: Supported},
	}
	switch goos {
	case "linux":
		items[SystemMetrics] = Support{State: Supported}
		items[ProcessMetrics] = Support{State: Supported}
		items[CPULoadAverage] = Support{State: Supported}
		items[CgroupV2] = Support{State: Supported}
		items[ProfileSample] = Support{State: Unsupported, Reason: "sample profiles require macOS"}
	case "darwin":
		items[SystemMetrics] = Support{State: Supported}
		items[ProcessMetrics] = Support{State: Supported}
		items[CPULoadAverage] = Support{State: Unsupported, Reason: "load averages are not exposed by the macOS gopsutil collector"}
		items[CgroupV2] = Support{State: Unsupported, Reason: "cgroup v2 is Linux-only"}
		if _, err := lookPath("sample"); err != nil {
			items[ProfileSample] = Support{State: Unavailable, Reason: "macOS sample executable not found"}
		} else {
			items[ProfileSample] = Support{State: Supported}
		}
	default:
		reason := fmt.Sprintf("system collection is not supported on %s", goos)
		items[SystemMetrics] = Support{State: Unsupported, Reason: reason}
		items[ProcessMetrics] = Support{State: Unsupported, Reason: reason}
		items[CPULoadAverage] = Support{State: Unsupported, Reason: reason}
		items[CgroupV2] = Support{State: Unsupported, Reason: "cgroup v2 is Linux-only"}
		items[ProfileSample] = Support{State: Unsupported, Reason: "sample profiles require macOS"}
	}
	return Set{OS: goos, Items: items}
}

// SupportFor returns an explicit unavailable state for an unknown capability.
func (s Set) SupportFor(name Name) Support {
	if support, ok := s.Items[name]; ok {
		return support
	}
	return Support{State: Unavailable, Reason: fmt.Sprintf("capability %q was not detected", name)}
}

// Require rejects unsupported and unavailable operations before a collector or
// capture implementation is invoked.
func (s Set) Require(names ...Name) error {
	for _, name := range names {
		support := s.SupportFor(name)
		if support.State == Supported {
			continue
		}
		reason := support.Reason
		if reason == "" {
			reason = string(support.State)
		}
		return &Error{Name: name, State: support.State, Reason: reason}
	}
	return nil
}

// Error is returned when a requested capability cannot be used.
type Error struct {
	Name   Name
	State  State
	Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("capability %s is %s: %s", e.Name, e.State, e.Reason)
}
