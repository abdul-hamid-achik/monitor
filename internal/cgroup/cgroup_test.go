package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMemMax(t *testing.T) {
	if v, ok := parseMemMax("536870912"); !ok || v != 536870912 {
		t.Errorf("parseMemMax(num) = (%d,%v)", v, ok)
	}
	if _, ok := parseMemMax("max"); ok {
		t.Error("parseMemMax(max) should be unlimited")
	}
	if _, ok := parseMemMax(""); ok {
		t.Error("parseMemMax(empty) should be unlimited")
	}
}

func TestParseCPUMax(t *testing.T) {
	if v, ok := parseCPUMax("50000 100000"); !ok || v != 0.5 {
		t.Errorf("parseCPUMax(quota) = (%v,%v), want 0.5", v, ok)
	}
	if v, ok := parseCPUMax("200000 100000"); !ok || v != 2 {
		t.Errorf("parseCPUMax(2 cores) = (%v,%v), want 2", v, ok)
	}
	if _, ok := parseCPUMax("max 100000"); ok {
		t.Error("parseCPUMax(max) should be unlimited")
	}
	if _, ok := parseCPUMax("garbage"); ok {
		t.Error("parseCPUMax(garbage) should fail")
	}
}

func TestReadFrom(t *testing.T) {
	dir := t.TempDir()
	write := func(name, val string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(val), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("memory.max", "536870912")    // 512 MiB
	write("memory.current", "393216000") // ~375 MiB
	write("cpu.max", "50000 100000")     // 0.5 CPU

	l := ReadFrom(dir)
	if !l.Active || l.MemLimit != 536870912 || l.MemCurrent != 393216000 || l.CPUQuota != 0.5 {
		t.Errorf("ReadFrom = %+v", l)
	}

	// An unlimited cgroup reports Active=false.
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "memory.max"), []byte("max"), 0o644)
	os.WriteFile(filepath.Join(empty, "cpu.max"), []byte("max 100000"), 0o644)
	if l := ReadFrom(empty); l.Active {
		t.Errorf("unlimited cgroup should be inactive; got %+v", l)
	}

	// A missing root (e.g. macOS) is inactive, not an error.
	if l := ReadFrom(filepath.Join(dir, "nope")); l.Active {
		t.Errorf("missing root should be inactive; got %+v", l)
	}
}
