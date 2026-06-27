// Package incidents captures the state of a misbehaving process or system
// into a content-addressed bundle and stashes it in fcheap for later
// investigation. It is the monitor equivalent of codemap's cache:
//
//  1. Bundle the current system snapshot, a profile of the offending
//     process, and the alert detail that triggered the capture.
//  2. Serialize the bundle to a temp dir under a stable layout.
//  3. Compute a sha256 tree-hash of the bundle's contents.
//  4. Shell out to `fcheap save` with the tree-hash as a tag. The tag is
//     content-addressed over the bundle bytes, so two byte-identical
//     bundles dedup to the same stash ID. Note that snapshots and profiles
//     embed wall-clock timestamps, so in practice each real capture differs
//     and gets a distinct ID — dedup is incidental, not guaranteed.
//
// The package degrades gracefully: when fcheap is missing or save fails
// for any reason, Capture returns the error but does not panic; callers
// can decide whether to surface it as a status-bar warning.
package incidents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// CaptureRequest is the input to Capture. Zero value is not useful; callers
// must fill in at least Snapshot and Alert (or Trigger).
type CaptureRequest struct {
	// Snapshot is the current SystemInfo (cpu/mem/network/disk/processes).
	Snapshot collector.SystemInfo `json:"snapshot"`
	// Profile is the profile captured for the offending process (heap,
	// cpu, goroutine, sample). May be the zero value when the capture
	// is system-wide rather than per-process.
	Profile profiler.Profile `json:"profile,omitempty"`
	// Alert describes the alert rule that triggered this capture, if
	// any. Empty for manual captures.
	Alert AlertDetail `json:"alert,omitempty"`
	// Trigger is a free-form label for manual captures (e.g. "manual",
	// "doctor", "on-quit"). Defaults to "manual" when empty.
	Trigger string `json:"trigger,omitempty"`
	// TTL is a fcheap duration string ("7d", "24h", "30d"). Empty means
	// never expire.
	TTL string `json:"ttl,omitempty"`
}

// AlertDetail is the alert-shaped metadata that explains why a stash was
// captured. Kept structurally identical to analyzer.Alert so callers can
// pass through the alert payload directly.
type AlertDetail struct {
	Severity string `json:"severity,omitempty"`
	Rule     string `json:"rule,omitempty"`
	PID      int32  `json:"pid,omitempty"`
	Process  string `json:"process,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// CaptureResult is what Capture returns.
type CaptureResult struct {
	StashID    string    `json:"stash_id"`
	TreeHash   string    `json:"tree_hash"`
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"size_bytes"`
	Tags       []string  `json:"tags"`
	CreatedAt  time.Time `json:"created_at"`
	Note       string    `json:"note,omitempty"`
}

// stashSave and hasFcheap are the fcheap save entry point and availability
// check as package vars, so tests can stub the success / save-failure paths
// without a real fcheap binary.
var (
	stashSave = ecosystem.StashSave
	hasFcheap = fcheapAvailable
)

// Capture builds the bundle, computes the tree-hash, and shells out to
// `fcheap save`. The temp directory is created with `t.TempDir`-style
// semantics (caller does not need to clean up; Capture removes it on
// success and leaves it on failure for forensics).
//
// On any fcheap failure, Capture returns the underlying error AND the
// tree-hash so callers can still surface the incident in their own
// UI (e.g. "investigation failed; bundle at /tmp/monitor-XYZ").
func Capture(ctx context.Context, req CaptureRequest) (CaptureResult, error) {
	if req.Trigger == "" {
		req.Trigger = "manual"
	}

	dir, err := os.MkdirTemp("", "monitor-incident-")
	if err != nil {
		return CaptureResult{}, fmt.Errorf("mkdir temp: %w", err)
	}
	// Best-effort cleanup; on failure we keep the dir for forensics.
	cleanup := func() { _ = os.RemoveAll(dir) }

	if err := writeBundle(dir, &req); err != nil {
		// A half-written bundle has no forensic value — clean it up. (The
		// fcheap-missing / save-failed paths below intentionally keep the
		// dir so the caller can recover a complete bundle.)
		cleanup()
		return CaptureResult{}, fmt.Errorf("write bundle: %w", err)
	}

	treeHash, sizeBytes, err := computeTreeHash(dir)
	if err != nil {
		cleanup()
		return CaptureResult{}, fmt.Errorf("compute tree-hash: %w", err)
	}

	tags := []string{
		"monitor-incident",
		"trigger:" + req.Trigger,
		"snapshot:" + treeHash[:12],
	}
	if req.Alert.Rule != "" {
		tags = append(tags, "alert:"+req.Alert.Rule)
		if req.Alert.PID > 0 {
			tags = append(tags, fmt.Sprintf("pid:%d", req.Alert.PID))
		}
	}

	name := fmt.Sprintf("monitor-%s-%s", req.Trigger, treeHash[:12])

	// Check fcheap availability first; the wrappers return a Wrap on
	// every failure so we can distinguish "binary missing" from
	// "save failed".
	if !hasFcheap() {
		// Don't clean up the temp dir — caller can pick it up.
		return CaptureResult{
			StashID:   "",
			TreeHash:  treeHash,
			Path:      dir,
			SizeBytes: sizeBytes,
			Tags:      tags,
			CreatedAt: time.Now(),
			Note:      "fcheap not on PATH; bundle saved locally only",
		}, fmt.Errorf("fcheap not on PATH")
	}

	res, err := stashSave(ctx, dir, name, tags, req.TTL)
	if err != nil {
		return CaptureResult{
			TreeHash:  treeHash,
			Path:      dir,
			SizeBytes: sizeBytes,
			Tags:      tags,
			CreatedAt: time.Now(),
			Note:      "fcheap save failed; bundle kept locally: " + err.Error(),
		}, err
	}
	cleanup() // success — drop the temp dir; fcheap owns the bytes now.

	return CaptureResult{
		StashID:   res.ID,
		TreeHash:  treeHash,
		Path:      res.Path,
		SizeBytes: res.SizeBytes,
		Tags:      tags,
		CreatedAt: time.Now(),
	}, nil
}

// Search returns the list of stashes matching the given tags. Convenience
// wrapper around ecosystem.StashList with monitor-specific defaults.
func Search(ctx context.Context, tags []string) ([]ecosystem.StashListEntry, error) {
	// Always scope to monitor stashes.
	all := append([]string{"monitor-incident"}, tags...)
	return ecosystem.StashList(ctx, all)
}

// fcheapAvailable reports whether fcheap is on PATH. It resolves the binary
// via exec.LookPath (a PATH scan, no subprocess), bypassing the probe cache
// so a freshly installed tool is picked up immediately.
func fcheapAvailable() bool {
	_, err := exec.LookPath("fcheap")
	return err == nil
}

// writeBundle serializes the request into a stable file layout under dir:
//
//	manifest.json   — lightweight header (trigger / alert / ttl)
//	snapshot.json   — req.Snapshot
//	profile.json    — req.Profile (when non-empty)
//
// The heavy Snapshot/Profile payloads live ONLY in their own files (the
// manifest used to embed the whole CaptureRequest, double-serializing them
// and doubling the hash input). The tree-hash is computed over a sorted
// concatenation of the file contents, so the layout is stable regardless of
// map ordering.
func writeBundle(dir string, req *CaptureRequest) error {
	manifest := struct {
		Trigger string      `json:"trigger"`
		Alert   AlertDetail `json:"alert,omitempty"`
		TTL     string      `json:"ttl,omitempty"`
	}{Trigger: req.Trigger, Alert: req.Alert, TTL: req.TTL}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "snapshot.json"), req.Snapshot); err != nil {
		return err
	}
	if req.Profile.PID != 0 {
		if err := writeJSON(filepath.Join(dir, "profile.json"), req.Profile); err != nil {
			return err
		}
	}
	return nil
}

// computeTreeHash returns the sha256 of the sorted file contents of dir
// plus the total size of those contents.
func computeTreeHash(dir string) (string, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	h := sha256.New()
	var total int64
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", 0, err
		}
		// Hash the file name first, then a separator, then the contents,
		// so two files with the same bytes but different names produce
		// different hashes (avoids collisions when an incident bundles
		// one file renamed).
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		total += int64(len(data))
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
