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

// Limits is the set of cgroup v2 limits in effect for the current process.
type Limits struct {
	Active     bool    `json:"active"`
	MemLimit   uint64  `json:"mem_limit_bytes,omitempty"`
	MemCurrent uint64  `json:"mem_current_bytes,omitempty"`
	CPUQuota   float64 `json:"cpu_quota_cores,omitempty"`
}

// Read reads cgroup v2 limits from the default root.
func Read() Limits { return ReadFrom(DefaultRoot) }

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
