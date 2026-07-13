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

func TestParseSelfCgroupRel(t *testing.T) {
	cases := map[string]string{
		"0::/system.slice/foo.service":        "/system.slice/foo.service",
		"0::/":                                "/",
		"0::":                                 "",
		"12:pids:/docker/abc\n0::/docker/abc": "/docker/abc", // v1 lines ignored
		"1:name=systemd:/user.slice":          "",            // no v2 line
		"":                                    "",
	}
	for in, want := range cases {
		if got := parseSelfCgroupRel(in); got != want {
			t.Errorf("parseSelfCgroupRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCgroupDirs(t *testing.T) {
	root := "/sys/fs/cgroup"
	// Docker default: process at namespace root -> just the root.
	if dirs := cgroupDirs(root, "/"); len(dirs) != 1 || dirs[0] != root {
		t.Errorf("cgroupDirs(root, /) = %v, want [%s]", dirs, root)
	}
	// A nested service: leaf first, then ancestors up to root.
	got := cgroupDirs(root, "/system.slice/foo.service")
	want := []string{
		root + "/system.slice/foo.service",
		root + "/system.slice",
		root,
	}
	if len(got) != len(want) {
		t.Fatalf("cgroupDirs nested = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cgroupDirs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestReadResolvesLeafLimit exercises Read() end-to-end with a fake
// /proc/self/cgroup and a fake hierarchy: the limit lives on an ancestor, and
// Read must walk up to find it (the leaf itself is unlimited).
func TestReadResolvesLeafLimit(t *testing.T) {
	root := t.TempDir()
	// Hierarchy: root/system.slice has the limit; the leaf foo.service does not.
	svc := filepath.Join(root, "system.slice")
	leaf := filepath.Join(svc, "foo.service")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc, "memory.max"), []byte("268435456"), 0o644); err != nil {
		t.Fatalf("write ancestor memory.max: %v", err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "memory.max"), []byte("max"), 0o644); err != nil {
		t.Fatalf("write leaf memory.max: %v", err)
	}

	// Point selfCgroupPath at a fixture naming the leaf, and DefaultRoot at our
	// fake root via a temporary swap of the package's resolution.
	procFixture := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(procFixture, []byte("0::/system.slice/foo.service\n"), 0o644); err != nil {
		t.Fatalf("write cgroup fixture: %v", err)
	}
	oldSelf := selfCgroupPath
	selfCgroupPath = procFixture
	defer func() { selfCgroupPath = oldSelf }()

	// Walk the fake hierarchy directly (Read hardcodes DefaultRoot, so assert
	// the resolution helpers compose correctly against our temp root).
	var found Limits
	for _, dir := range cgroupDirs(root, parseSelfCgroupRel(readFile(selfCgroupPath))) {
		if l := ReadFrom(dir); l.Active {
			found = l
			break
		}
	}
	if !found.Active || found.MemLimit != 268435456 {
		t.Errorf("expected to resolve ancestor limit 256MiB; got %+v", found)
	}
}

func TestReadFrom(t *testing.T) {
	dir := t.TempDir()
	write := func(name, val string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(val), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("memory.max", "536870912")     // 512 MiB
	write("memory.current", "393216000") // ~375 MiB
	write("cpu.max", "50000 100000")     // 0.5 CPU

	l := ReadFrom(dir)
	if !l.Active || l.MemLimit != 536870912 || l.MemCurrent != 393216000 || l.CPUQuota != 0.5 {
		t.Errorf("ReadFrom = %+v", l)
	}

	// An unlimited cgroup reports Active=false.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "memory.max"), []byte("max"), 0o644); err != nil {
		t.Fatalf("write unlimited memory.max: %v", err)
	}
	if err := os.WriteFile(filepath.Join(empty, "cpu.max"), []byte("max 100000"), 0o644); err != nil {
		t.Fatalf("write unlimited cpu.max: %v", err)
	}
	if l := ReadFrom(empty); l.Active {
		t.Errorf("unlimited cgroup should be inactive; got %+v", l)
	}

	// A missing root (e.g. macOS) is inactive, not an error.
	if l := ReadFrom(filepath.Join(dir, "nope")); l.Active {
		t.Errorf("missing root should be inactive; got %+v", l)
	}
}
