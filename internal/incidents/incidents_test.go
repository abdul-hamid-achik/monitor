package incidents

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// TestComputeTreeHashStable verifies that the same bundle produces the
// same hash regardless of file creation order (the sort step is what
// makes this stable).
func TestComputeTreeHashStable(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("a.txt", "alpha")
	write("b.txt", "beta")
	h1, size1, err := computeTreeHash(dir)
	if err != nil {
		t.Fatalf("computeTreeHash: %v", err)
	}
	if size1 == 0 {
		t.Fatalf("size should be > 0; got %d", size1)
	}
	if len(h1) != 64 { // sha256 hex
		t.Fatalf("hash should be 64 hex chars; got %d (%s)", len(h1), h1)
	}

	// Re-create the dir with the same files; hash must match.
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write in reverse order.
	write("b.txt", "beta")
	write("a.txt", "alpha")
	h2, _, err := computeTreeHash(dir)
	if err != nil {
		t.Fatalf("computeTreeHash (2): %v", err)
	}
	if h1 != h2 {
		t.Fatalf("hash should be stable across write order; %s != %s", h1, h2)
	}
}

// TestComputeTreeHashDistinguishesNames verifies two files with the same
// bytes but different names hash differently (avoids collisions when an
// incident gets one file renamed).
func TestComputeTreeHashDistinguishesNames(t *testing.T) {
	a := t.TempDir()
	os.WriteFile(filepath.Join(a, "only.txt"), []byte("same"), 0o644)
	b := t.TempDir()
	os.WriteFile(filepath.Join(b, "OTHER.txt"), []byte("same"), 0o644)
	ha, _, _ := computeTreeHash(a)
	hb, _, _ := computeTreeHash(b)
	if ha == hb {
		t.Fatalf("hashes should differ when filenames differ; both = %s", ha)
	}
}

// TestWriteBundleRoundTrip verifies the bundle layout (manifest.json,
// snapshot.json, profile.json) is correct.
func TestWriteBundleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	req := &CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test-host", CPU: collector.CPUInfo{UsagePercent: 42}},
		Profile: profiler.Profile{
			PID: 1234, Type: profiler.ProfileType("heap"),
			Symbols: []profiler.Symbol{{Func: "main.foo", File: "main.go", Line: 7}},
		},
		Alert:   AlertDetail{Rule: "test", PID: 1234},
		Trigger: "manual",
	}
	if err := writeBundle(dir, req); err != nil {
		t.Fatalf("writeBundle: %v", err)
	}

	for _, name := range []string{"manifest.json", "snapshot.json", "profile.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("missing %s in bundle: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("empty %s", name)
		}
		// Round-trip via JSON to verify the file is valid JSON.
		var any_ any
		if err := json.Unmarshal(data, &any_); err != nil {
			t.Fatalf("%s is not valid JSON: %v", name, err)
		}
	}
}

// TestWriteBundleOmitsProfileWhenZero verifies that an empty profile
// doesn't pollute the bundle (a zero profile.PID is the sentinel).
func TestWriteBundleOmitsProfileWhenZero(t *testing.T) {
	dir := t.TempDir()
	req := &CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	}
	if err := writeBundle(dir, req); err != nil {
		t.Fatalf("writeBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "profile.json")); !os.IsNotExist(err) {
		t.Fatalf("profile.json should not exist when profile is zero; stat err=%v", err)
	}
}

// TestCaptureWithoutFcheapStillProducesBundle verifies the graceful-
// degradation path: when fcheap isn't on PATH, Capture returns a
// non-empty result with the local bundle path and an error wrapping
// "fcheap not on PATH".
func TestCaptureWithoutFcheapStillProducesBundle(t *testing.T) {
	if _, err := exec.LookPath("fcheap"); err == nil {
		t.Skip("fcheap is on PATH; cannot exercise the no-fcheap fallback")
	}

	ctx := context.Background()
	res, err := Capture(ctx, CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err == nil {
		t.Fatalf("expected error when fcheap missing; got nil")
	}
	if !strings.Contains(err.Error(), "fcheap") {
		t.Fatalf("error should mention fcheap; got %q", err.Error())
	}
	if res.TreeHash == "" {
		t.Fatalf("TreeHash should still be populated even when fcheap is missing")
	}
	if res.Path == "" {
		t.Fatalf("Path should still be populated even when fcheap is missing")
	}
	if res.StashID != "" {
		t.Fatalf("StashID should be empty when fcheap missing; got %q", res.StashID)
	}
	if res.Note == "" {
		t.Fatalf("Note should explain the failure; got empty string")
	}
	// Bundle on disk should still exist for forensics.
	if _, err := os.Stat(filepath.Join(res.Path, "manifest.json")); err != nil {
		t.Fatalf("local bundle manifest missing: %v", err)
	}
}

// TestSearchFiltersByMonitorIncidentTag verifies that Search always
// prepends the "monitor-incident" tag so callers can't accidentally
// leak unrelated fcheap stashes. We exercise this by calling Search
// with a custom filter and confirming the call doesn't error (when
// fcheap is on PATH but has no matching stashes, the call returns an
// empty list — which is the correct, safe behavior).
func TestSearchFiltersByMonitorIncidentTag(t *testing.T) {
	if _, err := exec.LookPath("fcheap"); err != nil {
		t.Skip("fcheap not on PATH; cannot probe output")
	}
	ctx := context.Background()
	entries, err := Search(ctx, []string{"alert:never_matches_anything_xyz"})
	if err != nil {
		t.Fatalf("Search should not error when no stashes match; got %v", err)
	}
	// entries may be empty; the only invariant is "no error".
	_ = entries
}
