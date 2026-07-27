package incidents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
)

// stubFcheap swaps hasFcheap/stashSave for the duration of the test and
// restores the originals on cleanup. Every test in this file that touches
// the registry also sets XDG_STATE_HOME to a fresh t.TempDir() so nothing
// ever lands under the real ~/.local/state.
func stubFcheap(t *testing.T, has bool, save func(context.Context, string, string, []string, string) (ecosystem.StashSaveResult, error)) {
	t.Helper()
	origHas, origSave := hasFcheap, stashSave
	t.Cleanup(func() { hasFcheap, stashSave = origHas, origSave })
	hasFcheap = func() bool { return has }
	if save != nil {
		stashSave = save
	}
}

// failingSave is a stashSave stub that always fails with msg.
func failingSave(msg string) func(context.Context, string, string, []string, string) (ecosystem.StashSaveResult, error) {
	return func(_ context.Context, _, _ string, _ []string, _ string) (ecosystem.StashSaveResult, error) {
		return ecosystem.StashSaveResult{}, fmt.Errorf("%s", msg)
	}
}

func TestCaptureFailureRegistersAndResumeSucceeds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubFcheap(t, true, failingSave("disk full"))

	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err == nil {
		t.Fatal("expected an error when stashSave fails")
	}
	if res.RegistryID == "" {
		t.Fatal("RegistryID should be set after a failed capture")
	}

	var manifestErr error
	stashSave = func(_ context.Context, dir, _ string, _ []string, _ string) (ecosystem.StashSaveResult, error) {
		// Check synchronously — Resume removes the bundle dir once
		// stashSave reports success, so any check must happen in-line.
		_, manifestErr = os.Stat(filepath.Join(dir, "manifest.json"))
		return ecosystem.StashSaveResult{ID: "stash-99", Path: "/vault/stash-99"}, nil
	}

	res2, err := Resume(context.Background(), res.RegistryID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res2.StashID != "stash-99" {
		t.Errorf("StashID = %q, want stash-99", res2.StashID)
	}
	if res2.RegistryID != res.RegistryID {
		t.Errorf("RegistryID = %q, want %q", res2.RegistryID, res.RegistryID)
	}
	if manifestErr != nil {
		t.Errorf("stashSave should have been called with a dir containing manifest.json: %v", manifestErr)
	}

	dir, err := registryDir()
	if err != nil {
		t.Fatalf("registryDir: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, res.RegistryID)); !os.IsNotExist(statErr) {
		t.Errorf("registry entry dir should be removed after a successful resume; stat err = %v", statErr)
	}
	entries, err := ListRegistry()
	if err != nil {
		t.Fatalf("ListRegistry: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("ListRegistry should be empty after resume; got %d", len(entries))
	}
}

func TestResumeByBundlePath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	req := &CaptureRequest{
		Snapshot:  collector.SystemInfo{Hostname: "test"},
		Trigger:   "alert",
		Alert:     AlertDetail{Rule: "rss_growth", PID: 7},
		Diagnosis: &Diagnosis{Confidence: "high"},
	}
	if err := writeBundle(dir, req); err != nil {
		t.Fatalf("writeBundle: %v", err)
	}

	var gotName string
	var gotTags []string
	stubFcheap(t, true, func(_ context.Context, _, name string, tags []string, _ string) (ecosystem.StashSaveResult, error) {
		gotName = name
		gotTags = tags
		return ecosystem.StashSaveResult{ID: "stash-1", Path: "/vault/stash-1"}, nil
	})

	res, err := Resume(context.Background(), dir)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.StashID != "stash-1" {
		t.Errorf("StashID = %q, want stash-1", res.StashID)
	}
	if !strings.HasPrefix(gotName, "monitor-alert-") {
		t.Errorf("stash name = %q, want prefix monitor-alert-", gotName)
	}
	for _, want := range []string{"monitor-incident", "trigger:alert", "alert:rss_growth", "pid:7", "confidence:high"} {
		found := false
		for _, tag := range gotTags {
			if tag == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tags %v missing %q", gotTags, want)
		}
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("external bare bundle dir should be preserved after resume; stat err = %v", statErr)
	}
}

func TestResumeUnknownID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, err := Resume(context.Background(), "deadbeef0000")
	if err == nil || !strings.Contains(err.Error(), "no registry entry") {
		t.Fatalf("Resume(unknown id) = %v, want an error containing %q", err, "no registry entry")
	}
}

func TestResumeIntegrityMismatch(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubFcheap(t, true, failingSave("disk full"))

	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err == nil {
		t.Fatal("expected the initial capture to fail")
	}

	dir, err := registryDir()
	if err != nil {
		t.Fatalf("registryDir: %v", err)
	}
	entryDir := filepath.Join(dir, res.RegistryID)
	snapPath := filepath.Join(entryDir, "bundle", "snapshot.json")
	f, err := os.OpenFile(snapPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open snapshot.json: %v", err)
	}
	if _, err := f.WriteString("tampered"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	_, err = Resume(context.Background(), res.RegistryID)
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("Resume(tampered) = %v, want an error containing %q", err, "integrity")
	}
	if _, statErr := os.Stat(entryDir); statErr != nil {
		t.Errorf("entry dir should be retained after a refused resume: %v", statErr)
	}
}

func TestResumeFailureRecordsAttempt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubFcheap(t, true, failingSave("disk full"))

	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err == nil {
		t.Fatal("expected the initial capture to fail")
	}

	dir, err := registryDir()
	if err != nil {
		t.Fatalf("registryDir: %v", err)
	}
	entryPath := filepath.Join(dir, res.RegistryID, "entry.json")
	e1, err := readEntry(entryPath)
	if err != nil {
		t.Fatalf("readEntry: %v", err)
	}
	if e1.Attempts != 1 {
		t.Fatalf("Attempts after initial capture = %d, want 1", e1.Attempts)
	}

	if _, err := Resume(context.Background(), res.RegistryID); err == nil {
		t.Fatal("expected Resume to fail (stashSave still failing)")
	}

	e2, err := readEntry(entryPath)
	if err != nil {
		t.Fatalf("readEntry (2): %v", err)
	}
	if e2.Attempts != 2 {
		t.Errorf("Attempts after failed resume = %d, want 2", e2.Attempts)
	}
	if e2.LastError == "" {
		t.Error("LastError should be updated after a failed resume")
	}
	if e2.LastAttemptAt.IsZero() {
		t.Error("LastAttemptAt should be set after a failed resume")
	}
}

func TestResumeWithoutFcheap(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubFcheap(t, true, failingSave("disk full"))

	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err == nil {
		t.Fatal("expected the initial capture to fail")
	}

	hasFcheap = func() bool { return false }

	_, err = Resume(context.Background(), res.RegistryID)
	if err == nil || !strings.Contains(err.Error(), "fcheap not on PATH") {
		t.Fatalf("Resume without fcheap = %v, want an error containing %q", err, "fcheap not on PATH")
	}

	dir, err := registryDir()
	if err != nil {
		t.Fatalf("registryDir: %v", err)
	}
	e, err := readEntry(filepath.Join(dir, res.RegistryID, "entry.json"))
	if err != nil {
		t.Fatalf("readEntry: %v", err)
	}
	if e.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", e.Attempts)
	}
}

func TestRegisterDuplicateIDUpdatesEntry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubFcheap(t, true, failingSave("disk full"))

	req := CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "dup-test"},
		Trigger:  "manual",
	}
	res1, err1 := Capture(context.Background(), req)
	if err1 == nil {
		t.Fatal("expected the first capture to fail")
	}
	res2, err2 := Capture(context.Background(), req)
	if err2 == nil {
		t.Fatal("expected the second capture to fail")
	}
	if res1.RegistryID != res2.RegistryID {
		t.Fatalf("byte-identical bundles should register under the same ID: %q != %q", res1.RegistryID, res2.RegistryID)
	}

	dir, err := registryDir()
	if err != nil {
		t.Fatalf("registryDir: %v", err)
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(des) != 1 {
		t.Fatalf("expected exactly one registry entry dir, got %d", len(des))
	}
	e, err := readEntry(filepath.Join(dir, res1.RegistryID, "entry.json"))
	if err != nil {
		t.Fatalf("readEntry: %v", err)
	}
	if e.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2 (bumped on the duplicate registration)", e.Attempts)
	}
}

func TestRegistryPrunesOldest(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubFcheap(t, true, failingSave("disk full"))

	for i := 0; i < maxRegistryEntries+3; i++ {
		_, err := Capture(context.Background(), CaptureRequest{
			Snapshot: collector.SystemInfo{Hostname: fmt.Sprintf("h%d", i)},
			Trigger:  "manual",
		})
		if err == nil {
			t.Fatalf("expected capture %d to fail", i)
		}
	}

	entries, err := ListRegistry()
	if err != nil {
		t.Fatalf("ListRegistry: %v", err)
	}
	if len(entries) != maxRegistryEntries {
		t.Errorf("len(entries) = %d, want %d", len(entries), maxRegistryEntries)
	}
}

func TestListRegistrySkipsCorruptEntries(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, err := registryDir()
	if err != nil {
		t.Fatalf("registryDir: %v", err)
	}
	bogusDir := filepath.Join(dir, "bogus")
	if err := os.MkdirAll(bogusDir, 0o755); err != nil {
		t.Fatalf("mkdir bogus: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bogusDir, "entry.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write bogus entry.json: %v", err)
	}

	stubFcheap(t, true, failingSave("disk full"))
	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err == nil {
		t.Fatal("expected the capture to fail")
	}

	entries, err := ListRegistry()
	if err != nil {
		t.Fatalf("ListRegistry should skip corrupt entries, not error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (the corrupt one skipped)", len(entries))
	}
	if entries[0].ID != res.RegistryID {
		t.Errorf("entries[0].ID = %q, want %q", entries[0].ID, res.RegistryID)
	}
}

func TestLoadEntryForms(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubFcheap(t, true, failingSave("disk full"))

	res, err := Capture(context.Background(), CaptureRequest{
		Snapshot: collector.SystemInfo{Hostname: "test"},
		Trigger:  "manual",
	})
	if err == nil {
		t.Fatal("expected the capture to fail")
	}

	dir, err := registryDir()
	if err != nil {
		t.Fatalf("registryDir: %v", err)
	}
	entryDirPath := filepath.Join(dir, res.RegistryID)

	bareDir := t.TempDir()
	if err := copyFlatDir(filepath.Join(entryDirPath, "bundle"), bareDir); err != nil {
		t.Fatalf("copyFlatDir: %v", err)
	}

	tests := []struct {
		name  string
		input string
	}{
		{"registry id", res.RegistryID},
		{"entry dir path", entryDirPath},
		{"bare bundle path", bareDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := LoadEntry(tt.input)
			if err != nil {
				t.Fatalf("LoadEntry(%q): %v", tt.input, err)
			}
			if e.ID != res.RegistryID {
				t.Errorf("ID = %q, want %q", e.ID, res.RegistryID)
			}
		})
	}

	t.Run("neither", func(t *testing.T) {
		_, err := LoadEntry(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "neither") {
			t.Fatalf("LoadEntry(empty dir) = %v, want an error containing %q", err, "neither")
		}
	})
}

// sanity check that time.Time zero values round-trip through JSON as
// expected by readEntry's id/tree_hash validation (documents the contract
// LoadEntry relies on).
func TestReadEntryRejectsMissingIDOrHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entry.json")
	if err := writeJSON(path, RegistryEntry{CreatedAt: time.Now()}); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	if _, err := readEntry(path); err == nil {
		t.Fatal("readEntry should reject an entry missing id/tree_hash")
	}
}

func TestLoadBareRejectsWrongKindVersionAndMissingFiles(t *testing.T) {
	for name, mutate := range map[string]func(*bundleManifest){
		"wrong kind":    func(m *bundleManifest) { m.Kind = "other" },
		"wrong version": func(m *bundleManifest) { m.SchemaVersion = "999" },
		"missing file":  func(m *bundleManifest) { m.Files = []string{"manifest.json"} },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := writeBundle(dir, &CaptureRequest{Snapshot: collector.SystemInfo{Hostname: "h"}, Trigger: "manual"}); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
			var manifest bundleManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			mutate(&manifest)
			if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadEntry(dir); err == nil {
				t.Fatal("invalid bare bundle accepted")
			}
		})
	}
}

func TestCopyFlatDirRejectsTooManyEntries(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "copy")
	for i := 0; i < maxBundleFiles+1; i++ {
		name := filepath.Join(src, fmt.Sprintf("entry-%02d", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyFlatDir(src, dst); err == nil {
		t.Fatal("copyFlatDir accepted an unbounded bundle")
	}
}

func TestValidateBundleRejectsManifestAndByteBounds(t *testing.T) {
	t.Run("oversized manifest", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(filepath.Join(dir, "manifest.json"), maxManifestBytes+1); err != nil {
			t.Fatal(err)
		}
		if err := validateBundle(dir); err == nil || !strings.Contains(err.Error(), "manifest.json") {
			t.Fatalf("oversized manifest error = %v", err)
		}
	})

	t.Run("oversized entry", func(t *testing.T) {
		dir := t.TempDir()
		if err := writeBundle(dir, &CaptureRequest{Snapshot: collector.SystemInfo{Hostname: "h"}, Trigger: "manual"}); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(filepath.Join(dir, "snapshot.json"), maxBundleFileBytes+1); err != nil {
			t.Fatal(err)
		}
		if err := validateBundle(dir); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized entry error = %v", err)
		}
	})

	t.Run("total bytes", func(t *testing.T) {
		dir := t.TempDir()
		files := []string{"manifest.json", "snapshot.json", "one.data", "two.data", "three.data"}
		manifest := bundleManifest{
			SchemaVersion: bundleSchemaVersion, Kind: bundleKind, Trigger: "manual", Files: files,
		}
		if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"one.data", "two.data", "three.data"} {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, 90<<20); err != nil {
				t.Fatal(err)
			}
		}
		if err := validateBundle(dir); err == nil || !strings.Contains(err.Error(), "total bytes") {
			t.Fatalf("total byte bound error = %v", err)
		}
	})
}

func TestValidateBundleRejectsAmbiguousOrUnexpectedFiles(t *testing.T) {
	tests := map[string]func(string, *bundleManifest){
		"duplicate": func(_ string, manifest *bundleManifest) {
			manifest.Files = append(manifest.Files, "snapshot.json")
		},
		"traversal": func(_ string, manifest *bundleManifest) {
			manifest.Files = append(manifest.Files, "../secret")
		},
		"undeclared file": func(dir string, _ *bundleManifest) {
			if err := os.WriteFile(filepath.Join(dir, "extra.json"), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"undeclared raw profile": func(_ string, manifest *bundleManifest) {
			manifest.RawProfile = "profile.data"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := writeBundle(dir, &CaptureRequest{Snapshot: collector.SystemInfo{Hostname: "h"}, Trigger: "manual"}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest bundleManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			mutate(dir, &manifest)
			if name != "undeclared file" {
				if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateBundle(dir); err == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}

func TestRegistryIgnoresTamperedBundlePathAndUsesPrivateModes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubFcheap(t, false, nil)
	res, err := Capture(context.Background(), CaptureRequest{Snapshot: collector.SystemInfo{Hostname: "h"}, Trigger: "manual"})
	if err == nil {
		t.Fatal("expected missing fcheap error")
	}
	entryPath := filepath.Join(filepath.Dir(res.Path), "entry.json")
	entry, err := readEntry(entryPath)
	if err != nil {
		t.Fatal(err)
	}
	entry.BundlePath = t.TempDir()
	if err := writeJSON(entryPath, entry); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEntry(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BundlePath != res.Path {
		t.Fatalf("trusted tampered bundle path %q; want %q", loaded.BundlePath, res.Path)
	}
	for _, path := range []string{filepath.Dir(res.Path), res.Path} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("mode(%s) = %v, %v; want 0700", path, info.Mode().Perm(), err)
		}
	}
	for _, path := range []string{entryPath, filepath.Join(res.Path, "manifest.json"), filepath.Join(res.Path, "snapshot.json")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode(%s) = %v, %v; want 0600", path, info.Mode().Perm(), err)
		}
	}
}
