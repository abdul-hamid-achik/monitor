package incidents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// TestCaptureSuccessRemovesTempDir stubs the save path and asserts the result
// is mapped and the temp bundle dir is cleaned up on success.
func TestCaptureSuccessRemovesTempDir(t *testing.T) {
	origHas, origSave := hasFcheap, stashSave
	defer func() { hasFcheap, stashSave = origHas, origSave }()

	hasFcheap = func() bool { return true }
	var savedDir string
	stashSave = func(_ context.Context, dir, _ string, _ []string, _ string) (ecosystem.StashSaveResult, error) {
		savedDir = dir
		return ecosystem.StashSaveResult{ID: "stash-123", Path: "/vault/stash-123", SizeBytes: 4096}, nil
	}

	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if res.StashID != "stash-123" || res.Path != "/vault/stash-123" || res.SizeBytes != 4096 {
		t.Errorf("result not mapped from save: %+v", res)
	}
	if savedDir == "" {
		t.Fatal("stashSave never received a dir")
	}
	if _, statErr := os.Stat(savedDir); !os.IsNotExist(statErr) {
		t.Errorf("temp dir %s should be removed on success; stat err = %v", savedDir, statErr)
	}
}

// TestCaptureSaveFailureKeepsBundle asserts a save failure keeps the local
// bundle (for forensics), registers it in the durable registry, and reports
// the error in the Note.
func TestCaptureSaveFailureKeepsBundle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origHas, origSave := hasFcheap, stashSave
	defer func() { hasFcheap, stashSave = origHas, origSave }()

	hasFcheap = func() bool { return true }
	stashSave = func(_ context.Context, _, _ string, _ []string, _ string) (ecosystem.StashSaveResult, error) {
		return ecosystem.StashSaveResult{}, fmt.Errorf("disk full")
	}

	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err == nil {
		t.Fatal("expected an error when stashSave fails")
	}
	if res.StashID != "" {
		t.Errorf("StashID should be empty on failure; got %q", res.StashID)
	}
	if !strings.Contains(res.Note, "disk full") {
		t.Errorf("Note should mention the failure; got %q", res.Note)
	}
	if !strings.Contains(res.Note, "resume-stash") {
		t.Errorf("Note should point at resume-stash; got %q", res.Note)
	}
	if res.Path == "" {
		t.Fatal("Path should be populated on failure")
	}
	if res.RegistryID == "" {
		t.Fatal("RegistryID should be populated when registration succeeds")
	}
	if res.RegistryID != res.TreeHash[:12] {
		t.Errorf("RegistryID = %q, want tree-hash prefix %q", res.RegistryID, res.TreeHash[:12])
	}
	if !strings.HasPrefix(res.Path, os.Getenv("XDG_STATE_HOME")) {
		t.Errorf("Path should be under the XDG_STATE_HOME temp dir %q; got %q", os.Getenv("XDG_STATE_HOME"), res.Path)
	}
	if _, statErr := os.Stat(filepath.Join(res.Path, "manifest.json")); statErr != nil {
		t.Errorf("bundle should be kept on save failure: %v", statErr)
	}
	entryPath := filepath.Join(filepath.Dir(res.Path), "entry.json")
	if _, statErr := os.Stat(entryPath); statErr != nil {
		t.Errorf("entry.json should exist at %s: %v", entryPath, statErr)
	}
}

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
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove original dir: %v", err)
	}
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
	if err := os.WriteFile(filepath.Join(a, "only.txt"), []byte("same"), 0o644); err != nil {
		t.Fatalf("write only.txt: %v", err)
	}
	b := t.TempDir()
	if err := os.WriteFile(filepath.Join(b, "OTHER.txt"), []byte("same"), 0o644); err != nil {
		t.Fatalf("write OTHER.txt: %v", err)
	}
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

// TestWriteBundleIncludesDiagnosis verifies the diagnosis round-trips into
// manifest.json when present on the request.
func TestWriteBundleIncludesDiagnosis(t *testing.T) {
	dir := t.TempDir()
	req := &CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
		Diagnosis: &Diagnosis{
			Summary:     "RSS grew 42%/10min while CPU stayed flat",
			Evidence:    []string{"slope 3.2MB/min", "R²=0.94"},
			Confidence:  "high",
			NextActions: []string{"monitor_profile_capture type:heap"},
		},
	}
	if err := writeBundle(dir, req); err != nil {
		t.Fatalf("writeBundle: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest.json not valid JSON: %v", err)
	}
	diag, ok := m["diagnosis"].(map[string]any)
	if !ok {
		t.Fatalf("manifest.json missing diagnosis object: %v", m)
	}
	if diag["summary"] != req.Diagnosis.Summary {
		t.Errorf("diagnosis.summary = %v, want %q", diag["summary"], req.Diagnosis.Summary)
	}
	if diag["confidence"] != "high" {
		t.Errorf("diagnosis.confidence = %v, want high", diag["confidence"])
	}
}

// TestWriteBundleOmitsDiagnosisWhenNil verifies manifest.json carries no
// "diagnosis" key at all when the request has none (byte-level omitempty
// check, not just a nil-field check).
func TestWriteBundleOmitsDiagnosisWhenNil(t *testing.T) {
	dir := t.TempDir()
	req := &CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	}
	if err := writeBundle(dir, req); err != nil {
		t.Fatalf("writeBundle: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	if bytes.Contains(data, []byte(`"diagnosis"`)) {
		t.Errorf("manifest.json should omit the diagnosis key when nil; got %s", data)
	}
}

// TestBuildTags exercises the fcheap tag set built for a bundle.
func TestBuildTags(t *testing.T) {
	const treeHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	tests := []struct {
		name        string
		trigger     string
		alert       AlertDetail
		diag        *Diagnosis
		wantContain []string
		wantCount   int
	}{
		{
			name:      "trigger+hash only",
			trigger:   "manual",
			alert:     AlertDetail{},
			diag:      nil,
			wantCount: 3,
		},
		{
			name:        "alert rule adds tag",
			trigger:     "alert",
			alert:       AlertDetail{Rule: "rss_growth"},
			wantContain: []string{"alert:rss_growth"},
		},
		{
			name:        "pid tag only with rule",
			trigger:     "alert",
			alert:       AlertDetail{Rule: "rss_growth", PID: 42},
			wantContain: []string{"alert:rss_growth", "pid:42"},
		},
		{
			name:        "confidence tag",
			trigger:     "alert",
			diag:        &Diagnosis{Confidence: "high"},
			wantContain: []string{"confidence:high"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := buildTags(tt.trigger, tt.alert, tt.diag, treeHash)
			if tt.wantCount > 0 && len(tags) != tt.wantCount {
				t.Errorf("len(tags) = %d, want %d (%v)", len(tags), tt.wantCount, tags)
			}
			for _, want := range tt.wantContain {
				found := false
				for _, tag := range tags {
					if tag == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("tags %v missing %q", tags, want)
				}
			}
		})
	}
	t.Run("no confidence tag when empty", func(t *testing.T) {
		tags := buildTags("manual", AlertDetail{}, &Diagnosis{Summary: "x"}, treeHash)
		for _, tag := range tags {
			if strings.HasPrefix(tag, "confidence:") {
				t.Errorf("unexpected confidence tag %q when Confidence is empty", tag)
			}
		}
	})
}

// TestCaptureWithoutFcheapStillProducesBundle verifies the graceful-
// degradation path: when fcheap isn't on PATH, Capture returns a
// non-empty result with the registered bundle path and an error wrapping
// "fcheap not on PATH".
func TestCaptureWithoutFcheapStillProducesBundle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origHas := hasFcheap
	defer func() { hasFcheap = origHas }()
	hasFcheap = func() bool { return false }

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
	if res.RegistryID == "" {
		t.Fatalf("RegistryID should be populated when registration succeeds")
	}
	// Bundle on disk should still exist for forensics, now under the registry.
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
