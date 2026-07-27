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
	origHas, origSave, origRef := hasFcheap, stashSave, artifactRef
	defer func() { hasFcheap, stashSave, artifactRef = origHas, origSave, origRef }()

	hasFcheap = func() bool { return true }
	var savedDir string
	stashSave = func(_ context.Context, dir, _ string, _ []string, _ string) (ecosystem.StashSaveResult, error) {
		savedDir = dir
		return ecosystem.StashSaveResult{ID: "stash-123", Path: "/vault/stash-123", SizeBytes: 4096}, nil
	}
	artifactRef = func(_ context.Context, id string, _ ecosystem.ArtifactRefOpts) (ecosystem.ArtifactRefV1, error) {
		return ecosystem.ArtifactRefV1{
			Schema: "urn:filecheap.dev:artifact-ref:v1", Version: 1, Provider: "fcheap-local", URI: "fcheap://stash/" + id,
			ArtifactID: id, Kind: "monitor.incident",
		}, nil
	}

	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if res.StashID != "stash-123" || res.Path != "fcheap://stash/stash-123" || res.SizeBytes != 4096 {
		t.Errorf("result not mapped from save: %+v", res)
	}
	if savedDir == "" {
		t.Fatal("stashSave never received a dir")
	}
	if _, statErr := os.Stat(savedDir); !os.IsNotExist(statErr) {
		t.Errorf("temp dir %s should be removed on success; stat err = %v", savedDir, statErr)
	}
}

func TestWriteBundleCopiesRawProfileAndDropsArgv(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(t.TempDir(), "cpu.pprof")
	raw := []byte{0x1f, 0x8b, 0x08, 0x00, 0xff}
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	req := &CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "host"},
		Profile:  profiler.Profile{PID: 42, Type: profiler.ProfileCPU, Path: rawPath},
		Process:  &ProcessBinding{PID: 42, Cmdline: []string{"server", "--token", "secret-value"}, Runtime: "go"},
		Trigger:  "investigate",
	}
	if err := writeBundle(dir, req); err != nil {
		t.Fatal(err)
	}
	gotRaw, err := os.ReadFile(filepath.Join(dir, "profile.data"))
	if err != nil || !bytes.Equal(gotRaw, raw) {
		t.Fatalf("raw profile = %x, %v; want %x", gotRaw, err, raw)
	}
	profileJSON, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil || !bytes.Contains(profileJSON, []byte(`"path": "profile.data"`)) || bytes.Contains(profileJSON, []byte(rawPath)) {
		t.Fatalf("profile metadata did not use bundle-relative path: %s (%v)", profileJSON, err)
	}
	processJSON, err := os.ReadFile(filepath.Join(dir, "process.json"))
	if err != nil || bytes.Contains(processJSON, []byte("secret-value")) || bytes.Contains(processJSON, []byte(`"cmdline"`)) {
		t.Fatalf("process evidence leaked argv: %s (%v)", processJSON, err)
	}
	var manifest bundleManifest
	manifestJSON, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil || manifest.RawProfile != "profile.data" {
		t.Fatalf("manifest raw profile = %q, %v", manifest.RawProfile, err)
	}
}

func TestCopyProfileArtifactRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := copyProfileArtifact(link, filepath.Join(dir, "copy")); err == nil {
		t.Fatal("copyProfileArtifact accepted a symlink")
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
	wantStateRoot, _ := filepath.EvalSymlinks(os.Getenv("XDG_STATE_HOME"))
	if !strings.HasPrefix(res.Path, wantStateRoot) {
		t.Errorf("Path should be under the XDG_STATE_HOME temp dir %q; got %q", wantStateRoot, res.Path)
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

func TestComputeTreeHashUsesUnambiguousFraming(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "a"), []byte{'X', 'b', 0, 'Y'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "a"), []byte{'X'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "b"), []byte{'Y'}, 0o600); err != nil {
		t.Fatal(err)
	}
	ha, _, err := computeTreeHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, _, err := computeTreeHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha == hb {
		t.Fatalf("ambiguous trees produced the same hash: %s", ha)
	}
}

func TestCaptureRetainsEvidenceOnInvalidOrMutatingSave(t *testing.T) {
	for name, save := range map[string]func(context.Context, string, string, []string, string) (ecosystem.StashSaveResult, error){
		"empty id": func(context.Context, string, string, []string, string) (ecosystem.StashSaveResult, error) {
			return ecosystem.StashSaveResult{}, nil
		},
		"bundle changed": func(_ context.Context, dir, _ string, _ []string, _ string) (ecosystem.StashSaveResult, error) {
			if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(`{"changed":true}`), 0o600); err != nil {
				return ecosystem.StashSaveResult{}, err
			}
			return ecosystem.StashSaveResult{ID: "stash-1"}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			origHas, origSave := hasFcheap, stashSave
			defer func() { hasFcheap, stashSave = origHas, origSave }()
			hasFcheap = func() bool { return true }
			stashSave = save
			res, err := Capture(context.Background(), CaptureRequest{Snapshot: collector.SystemInfo{Hostname: "h"}, Trigger: "manual"})
			if err == nil || res.RegistryID == "" {
				t.Fatalf("Capture = %+v, %v; want retained registered evidence", res, err)
			}
			if _, err := os.Stat(filepath.Join(res.Path, "manifest.json")); err != nil {
				t.Fatalf("retained evidence missing: %v", err)
			}
		})
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
			tags := buildTags(tt.trigger, tt.alert, tt.diag, treeHash, nil, nil)
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
		tags := buildTags("manual", AlertDetail{}, &Diagnosis{Summary: "x"}, treeHash, nil, nil)
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
	t.Setenv("FCHEAP_STASH_DIR", t.TempDir())
	ctx := context.Background()
	entries, err := Search(ctx, []string{"alert:never_matches_anything_xyz"})
	if err != nil {
		t.Fatalf("Search should not error when no stashes match; got %v", err)
	}
	// entries may be empty; the only invariant is "no error".
	_ = entries
}

func TestWriteBundleIncludesProcessCorrelationsSemantic(t *testing.T) {
	dir := t.TempDir()
	req := CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "h"},
		Trigger:  "investigate",
		Process: &ProcessBinding{
			PID: 7, Runtime: "node", CodebaseRoot: "/app", MainScript: "/app/index.js",
		},
		Correlations:  []map[string]any{{"file": "/app/index.js", "line": 1, "fqn": "main"}},
		SemanticHits:  []map[string]any{{"file": "src/a.ts", "score": 0.9}},
		Context:       map[string]string{"run_id": "r1", "environment": "ci"},
		ProfileMethod: "sample",
		ExtraTags:     []string{"run:r1"},
	}
	if err := writeBundle(dir, &req); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "snapshot.json", "process.json", "correlations.json", "semantic.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m bundleManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Kind != bundleKind || m.Runtime != "node" || m.CodebaseRoot != "/app" || m.Context["run_id"] != "r1" {
		t.Fatalf("manifest = %+v", m)
	}
	tags := buildTags(req.Trigger, req.Alert, nil, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd", req.ExtraTags, req.Process)
	joined := strings.Join(tags, " ")
	for _, want := range []string{"runtime:node", "codebase:app", "run:r1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tags %v missing %q", tags, want)
		}
	}
}
