// Package cgroup reads cgroup v2 resource limits from /sys/fs/cgroup so the
// collector can report memory/CPU against the container's limit rather than
// the host. It is a no-op off Linux / outside a limited cgroup (Active=false).
package cgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultRoot is the cgroup v2 unified hierarchy mount point.
const DefaultRoot = "/sys/fs/cgroup"

// selfCgroupPath locates the calling process's cgroup. A var so tests can
// point it at a fixture.
var selfCgroupPath = "/proc/self/cgroup"

// Limits is the set of cgroup v2 limits in effect for the current process.
type Limits struct {
	Active     bool    `json:"active"`
	MemLimit   uint64  `json:"mem_limit_bytes,omitempty"`
	MemCurrent uint64  `json:"mem_current_bytes,omitempty"`
	CPUQuota   float64 `json:"cpu_quota_cores,omitempty"`
}

// Read reads cgroup v2 limits for the current process. It resolves the
// process's own cgroup from /proc/self/cgroup and reads the leaf, walking up
// to the nearest ancestor that actually configures a limit. This finds
// systemd service limits (MemoryMax=/CPUQuota=) and works under
// --cgroupns=host, while still handling the Docker default (where the process
// sits at the namespace root, so leaf == DefaultRoot).
func Read() Limits {
	rel := parseSelfCgroupRel(readFile(selfCgroupPath))
	for _, dir := range cgroupDirs(DefaultRoot, rel) {
		if l := ReadFrom(dir); l.Active {
			return l
		}
	}
	return Limits{}
}

// parseSelfCgroupRel extracts the cgroup v2 relative path from /proc/self/cgroup
// content. The unified (v2) entry is the line beginning "0::"; e.g.
// "0::/system.slice/foo.service" -> "/system.slice/foo.service". Returns "" when
// absent (non-Linux, or cgroup v1 only).
func parseSelfCgroupRel(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "0::") {
			return strings.TrimPrefix(line, "0::")
		}
	}
	return ""
}

// cgroupDirs returns the directories to probe for limits, from the process's
// own leaf cgroup up to root. With an empty/"/" rel (Docker default) it is just
// [root].
func cgroupDirs(root, rel string) []string {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return []string{root}
	}
	parts := strings.Split(rel, "/")
	dirs := make([]string, 0, len(parts)+1)
	for i := len(parts); i >= 0; i-- {
		dirs = append(dirs, filepath.Join(append([]string{root}, parts[:i]...)...))
	}
	return dirs
}

// ReadFrom reads cgroup v2 limits from root. Active is true only when a memory
// or CPU limit is actually configured (memory.max / cpu.max not "max").
func ReadFrom(root string) Limits {
	var l Limits
	if lim, ok := parseMemMax(readFile(filepath.Join(root, "memory.max"))); ok {
		l.MemLimit = lim
		l.MemCurrent, _ = parseUint(readFile(filepath.Join(root, "memory.current")))
		l.Active = true
	}
	if q, ok := parseCPUMax(readFile(filepath.Join(root, "cpu.max"))); ok {
		l.CPUQuota = q
		l.Active = true
	}
	return l
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// parseMemMax parses memory.max ("<bytes>" or "max"). ok=false for "max"/empty.
func parseMemMax(s string) (uint64, bool) {
	if s == "" || s == "max" {
		return 0, false
	}
	return parseUint(s)
}

func parseUint(s string) (uint64, bool) {
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// parseCPUMax parses cpu.max ("<quota> <period>" or "max <period>") into a CPU
// count (quota/period). ok=false when unlimited ("max") or malformed.
func parseCPUMax(s string) (float64, bool) {
	fields := strings.Fields(s)
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	quota, err1 := strconv.ParseFloat(fields[0], 64)
	period, err2 := strconv.ParseFloat(fields[1], 64)
	if err1 != nil || err2 != nil || period == 0 {
		return 0, false
	}
	return quota / period, true
}
