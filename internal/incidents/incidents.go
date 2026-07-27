// Package incidents captures the state of a misbehaving process or system
// into an integrity-addressed bundle and stashes it in fcheap for later
// investigation. It is the monitor equivalent of codemap's cache:
//
//  1. Bundle the current system snapshot, a profile of the offending
//     process, and the alert detail that triggered the capture.
//  2. Serialize the bundle to a temp dir under a stable layout.
//  3. Compute a sha256 tree-hash of the bundle's contents.
//  4. Shell out to `fcheap save` with the tree-hash as a tag. The hash is an
//     integrity receipt and grouping input; the returned stash ID is opaque,
//     and Monitor does not assume provider-side deduplication.
//
// The package degrades gracefully: when fcheap is missing or save fails
// for any reason, Capture returns the error but does not panic; callers
// can decide whether to surface it as a status-bar warning. Failed
// archivals are additionally persisted into a durable local registry
// under $XDG_STATE_HOME/monitor/incidents and can be re-attempted with
// `monitor incidents resume-stash`.
package incidents

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
	// Diagnosis is the analyzer's interpretation of why the alert fired
	// (summary, evidence, confidence, next actions). nil when no
	// interpretation is available. Serialized into manifest.json and
	// tagged confidence:<level> so fcheap search can filter on it.
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
	// Trigger is a free-form label for manual captures (e.g. "manual",
	// "doctor", "on-quit"). Defaults to "manual" when empty.
	Trigger string `json:"trigger,omitempty"`
	// TTL is a fcheap duration string ("7d", "24h", "30d"). Empty means
	// never expire.
	TTL string `json:"ttl,omitempty"`
	// Process is the optional process→codebase binding (Node/Go/etc.).
	// Written to process.json when non-nil.
	Process *ProcessBinding `json:"process,omitempty"`
	// Correlations are codemap frame→symbol rows from investigate.
	// Written to correlations.json when non-empty.
	Correlations []map[string]any `json:"correlations,omitempty"`
	// SemanticHits are vecgrep search/similar rows from investigate.
	// Written to semantic.json when non-empty.
	SemanticHits []map[string]any `json:"semantic_hits,omitempty"`
	// Context carries Chalupa/MONITOR correlation IDs for fcheap tags
	// and manifest.json. Never mixed into telemetry V1.
	Context map[string]string `json:"context,omitempty"`
	// ExtraTags are appended to the fcheap tag set (e.g. env:, run:, runtime:).
	ExtraTags []string `json:"extra_tags,omitempty"`
	// ProfileMethod records how the profile was captured (pprof_heap, sample, ...).
	ProfileMethod string `json:"profile_method,omitempty"`
}

// ProcessBinding is a JSON-stable copy of procbind.Binding so incidents
// does not import procbind (keeps the package graph shallow).
type ProcessBinding struct {
	PID          int32    `json:"pid"`
	Name         string   `json:"name,omitempty"`
	Exe          string   `json:"exe,omitempty"`
	Cwd          string   `json:"cwd,omitempty"`
	Cmdline      []string `json:"-"`
	ArgvRedacted bool     `json:"argv_redacted,omitempty"`
	Runtime      string   `json:"runtime,omitempty"`
	MainScript   string   `json:"main_script,omitempty"`
	CodebaseRoot string   `json:"codebase_root,omitempty"`
	InspectAddr  string   `json:"inspect_addr,omitempty"`
	Markers      []string `json:"markers,omitempty"`
	Limitations  []string `json:"limitations,omitempty"`
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

// Diagnosis mirrors analyzer.Diagnosis (Sprint 4.1) field-for-field so the
// bundle schema stays decoupled from analyzer internals — the same pattern
// AlertDetail uses for collector.Alert. It explains WHY an incident fired:
// human-readable summary, the evidence lines behind it, a confidence level,
// and suggested next actions for an agent.
type Diagnosis struct {
	Summary     string   `json:"summary,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	Confidence  string   `json:"confidence,omitempty"` // "low" | "medium" | "high"
	NextActions []string `json:"next_actions,omitempty"`
}

// CaptureResult is what Capture returns.
type CaptureResult struct {
	StashID   string    `json:"stash_id"`
	TreeHash  string    `json:"tree_hash"`
	Path      string    `json:"path"`
	SizeBytes int64     `json:"size_bytes"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	Note      string    `json:"note,omitempty"`
	// RegistryID is set when fcheap archival failed and the bundle was
	// persisted into the local registry ($XDG_STATE_HOME/monitor/incidents).
	// Retry with `monitor incidents resume-stash <RegistryID>`.
	RegistryID string `json:"registry_id,omitempty"`
	// ArtifactRef is a credential-free fcheap-local pointer emitted when
	// stash succeeds and `fcheap artifact-ref` is available. Chalupa and
	// other tools should store this instead of raw stash bytes.
	ArtifactRef map[string]any `json:"artifact_ref,omitempty"`
}

// stashSave, hasFcheap, and artifactRef are package vars so tests can stub
// the success / save-failure / artifact-ref paths without real binaries.
var (
	stashSave   = ecosystem.StashSave
	hasFcheap   = fcheapAvailable
	artifactRef = ecosystem.ArtifactRef
)

// Capture builds the bundle, computes the tree-hash, and shells out to
// `fcheap save`. The temp directory is created with `t.TempDir`-style
// semantics (caller does not need to clean up; Capture removes it on
// success and leaves it on failure for forensics).
//
// On any fcheap failure, Capture returns the underlying error AND the
// tree-hash so callers can still surface the incident in their own
// UI (e.g. "investigation failed; bundle registered locally; resume with
// `monitor incidents resume-stash <id>`").
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

	tags := buildTags(req.Trigger, req.Alert, req.Diagnosis, treeHash, req.ExtraTags, req.Process)

	name := fmt.Sprintf("monitor-%s-%s", req.Trigger, treeHash[:12])

	// Check fcheap availability first; the wrappers return a Wrap on
	// every failure so we can distinguish "binary missing" from
	// "save failed".
	if !hasFcheap() {
		saveErr := fmt.Errorf("fcheap not on PATH")
		return registerFailedCapture(dir, treeHash, sizeBytes, tags, name, &req, saveErr)
	}

	res, err := stashSave(ctx, dir, name, tags, req.TTL)
	if err != nil {
		return registerFailedCapture(dir, treeHash, sizeBytes, tags, name, &req, err)
	}
	if err := ecosystem.ValidateLocalStashID(res.ID); err != nil {
		return registerFailedCapture(dir, treeHash, sizeBytes, tags, name, &req, fmt.Errorf("invalid fcheap save result: %w", err))
	}
	postHash, _, err := computeTreeHash(dir)
	if err != nil || postHash != treeHash {
		if err == nil {
			err = fmt.Errorf("bundle changed while fcheap was saving it")
		}
		return registerFailedCapture(dir, treeHash, sizeBytes, tags, name, &req, err)
	}
	cleanup() // success — drop the temp dir; fcheap owns the bytes now.

	out := CaptureResult{
		StashID:   res.ID,
		TreeHash:  treeHash,
		Path:      stashResultPath(res),
		SizeBytes: stashResultSize(res, sizeBytes),
		Tags:      tags,
		CreatedAt: time.Now(),
	}
	// Best-effort ArtifactRefV1 for Chalupa/file.cheap handoff. Failure is
	// non-fatal: the stash_id alone is still usable.
	if ref, refErr := artifactRef(ctx, res.ID, ecosystem.ArtifactRefOpts{
		Kind:         "monitor.incident",
		ProducerTool: "monitor",
		NativeSchema: "urn:monitor.dev:incident:v1",
		NativeID:     treeHash[:16],
		Entrypoint:   "manifest.json",
	}); refErr == nil && ref.Validate() == nil {
		out.ArtifactRef = artifactRefMap(ref)
	}
	return out, nil
}

// buildTags builds the fcheap tag set for a bundle. Shared by Capture and
// LoadEntry (bare-bundle resume) so re-attempted saves carry identical tags.
func buildTags(trigger string, alert AlertDetail, diag *Diagnosis, treeHash string, extra []string, proc *ProcessBinding) []string {
	tags := []string{
		"monitor-incident",
		"trigger:" + trigger,
		"snapshot:" + treeHash[:12],
	}
	if alert.Rule != "" {
		tags = append(tags, "alert:"+alert.Rule)
		if alert.PID > 0 {
			tags = append(tags, fmt.Sprintf("pid:%d", alert.PID))
		}
	}
	if diag != nil && diag.Confidence != "" {
		tags = append(tags, "confidence:"+diag.Confidence)
	}
	if proc != nil {
		if proc.Runtime != "" && proc.Runtime != "unknown" {
			tags = append(tags, "runtime:"+proc.Runtime)
		}
		if proc.CodebaseRoot != "" {
			// Short, searchable basename — full path lives in process.json.
			tags = append(tags, "codebase:"+filepath.Base(proc.CodebaseRoot))
		}
	}
	tags = append(tags, extra...)
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, strings.TrimSpace(tag))
		runes := []rune(tag)
		if len(runes) > 96 {
			tag = string(runes[:96])
		}
		if tag != "" {
			cleaned = append(cleaned, tag)
		}
	}
	return cleaned
}

// registerFailedCapture persists the retained bundle into the durable
// registry so it survives reboots and tmp cleaning, and maps it into the
// CaptureResult the caller surfaces. When registration itself fails the
// temp bundle is left in place (the pre-registry behavior) and the Note
// says so. The original archival error is always the returned error.
func registerFailedCapture(dir, treeHash string, sizeBytes int64, tags []string, name string, req *CaptureRequest, saveErr error) (CaptureResult, error) {
	entry := RegistryEntry{
		SchemaVersion: registrySchemaVersion,
		ID:            treeHash[:12],
		TreeHash:      treeHash,
		Name:          name,
		SizeBytes:     sizeBytes,
		Tags:          tags,
		TTL:           req.TTL,
		Trigger:       req.Trigger,
		CreatedAt:     time.Now(),
		LastError:     saveErr.Error(),
	}
	reg, regErr := register(dir, entry)
	if regErr != nil {
		return CaptureResult{
			TreeHash:  treeHash,
			Path:      dir,
			SizeBytes: sizeBytes,
			Tags:      tags,
			CreatedAt: time.Now(),
			Note:      fmt.Sprintf("fcheap archival failed (%v); registry unavailable (%v); bundle kept at %s", saveErr, regErr, dir),
		}, saveErr
	}
	return CaptureResult{
		TreeHash:   treeHash,
		Path:       reg.BundlePath,
		SizeBytes:  sizeBytes,
		Tags:       tags,
		CreatedAt:  time.Now(),
		RegistryID: reg.ID,
		Note:       fmt.Sprintf("fcheap archival failed (%v); bundle registered locally as %s — run `monitor incidents resume-stash %s` to retry", saveErr, reg.ID, reg.ID),
	}, saveErr
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

// bundleManifest is the manifest.json schema — the lightweight bundle
// header. Package-level so writeBundle and LoadEntry (registry.go) share it.
type bundleManifest struct {
	SchemaVersion string            `json:"schema_version"`
	Kind          string            `json:"kind"`
	Trigger       string            `json:"trigger"`
	Alert         AlertDetail       `json:"alert,omitempty"`
	TTL           string            `json:"ttl,omitempty"`
	Diagnosis     *Diagnosis        `json:"diagnosis,omitempty"`
	Context       map[string]string `json:"context,omitempty"`
	ProfileMethod string            `json:"profile_method,omitempty"`
	CodebaseRoot  string            `json:"codebase_root,omitempty"`
	Runtime       string            `json:"runtime,omitempty"`
	MainScript    string            `json:"main_script,omitempty"`
	Files         []string          `json:"files,omitempty"`
	RawProfile    string            `json:"raw_profile,omitempty"`
}

const (
	bundleSchemaVersion = "1"
	bundleKind          = "monitor.incident"
)

// writeBundle serializes the request into a stable file layout under dir:
//
//	manifest.json      — lightweight header (trigger / alert / ttl / diagnosis / context)
//	snapshot.json      — req.Snapshot
//	profile.json       — req.Profile (when non-empty)
//	process.json       — process→codebase binding (when present)
//	correlations.json  — codemap frame correlations (when present)
//	semantic.json      — vecgrep hits (when present)
//
// The heavy Snapshot/Profile payloads live ONLY in their own files. The
// tree-hash is computed over a sorted concatenation of the file contents.
func writeBundle(dir string, req *CaptureRequest) error {
	files := []string{"manifest.json", "snapshot.json"}
	manifest := bundleManifest{
		SchemaVersion: bundleSchemaVersion,
		Kind:          bundleKind,
		Trigger:       req.Trigger,
		Alert:         req.Alert,
		TTL:           req.TTL,
		Diagnosis:     req.Diagnosis,
		Context:       req.Context,
		ProfileMethod: req.ProfileMethod,
	}
	if req.Process != nil {
		manifest.CodebaseRoot = req.Process.CodebaseRoot
		manifest.Runtime = req.Process.Runtime
		manifest.MainScript = req.Process.MainScript
	}
	if req.Profile.PID != 0 {
		files = append(files, "profile.json")
		if req.Profile.Path != "" {
			manifest.RawProfile = "profile.data"
			files = append(files, manifest.RawProfile)
		}
	}
	if req.Process != nil {
		files = append(files, "process.json")
	}
	if len(req.Correlations) > 0 {
		files = append(files, "correlations.json")
	}
	if len(req.SemanticHits) > 0 {
		files = append(files, "semantic.json")
	}
	manifest.Files = files

	if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "snapshot.json"), req.Snapshot); err != nil {
		return err
	}
	if req.Profile.PID != 0 {
		profile := req.Profile
		if manifest.RawProfile != "" {
			if err := copyProfileArtifact(req.Profile.Path, filepath.Join(dir, manifest.RawProfile)); err != nil {
				return fmt.Errorf("copy raw profile: %w", err)
			}
			// Never persist a machine-specific temporary source path. The
			// bundle-relative path is stable after fcheap saves the evidence.
			profile.Path = manifest.RawProfile
		}
		if err := writeJSON(filepath.Join(dir, "profile.json"), profile); err != nil {
			return err
		}
	}
	if req.Process != nil {
		process := *req.Process
		// Command-line arguments frequently contain tokens and credentials.
		// Runtime, executable, cwd and main script are sufficient to bind the
		// process to code without persisting argv in an incident bundle.
		process.ArgvRedacted = process.ArgvRedacted || len(process.Cmdline) > 0
		process.Cmdline = nil
		if err := writeJSON(filepath.Join(dir, "process.json"), process); err != nil {
			return err
		}
	}
	if len(req.Correlations) > 0 {
		if err := writeJSON(filepath.Join(dir, "correlations.json"), req.Correlations); err != nil {
			return err
		}
	}
	if len(req.SemanticHits) > 0 {
		if err := writeJSON(filepath.Join(dir, "semantic.json"), req.SemanticHits); err != nil {
			return err
		}
	}
	return nil
}

const maxRawProfileBytes int64 = 128 << 20

func copyProfileArtifact(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	openedInfo, err := in.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		return fmt.Errorf("source must be a regular file")
	}
	if openedInfo.Size() > maxRawProfileBytes {
		return fmt.Errorf("source is %d bytes; maximum is %d", openedInfo.Size(), maxRawProfileBytes)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(in, maxRawProfileBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	if written > maxRawProfileBytes {
		_ = os.Remove(dst)
		return fmt.Errorf("source grew beyond maximum of %d bytes", maxRawProfileBytes)
	}
	return nil
}

func stashResultPath(res ecosystem.StashSaveResult) string {
	if res.ID != "" {
		return "fcheap://stash/" + res.ID
	}
	return ""
}

func stashResultSize(res ecosystem.StashSaveResult, fallback int64) int64 {
	if res.SizeBytes > 0 {
		return res.SizeBytes
	}
	if res.TotalSize > 0 {
		return res.TotalSize
	}
	return fallback
}

func artifactRefMap(ref ecosystem.ArtifactRefV1) map[string]any {
	b, err := json.Marshal(ref)
	if err != nil {
		return map[string]any{
			"version":     ref.Version,
			"provider":    ref.Provider,
			"uri":         ref.URI,
			"artifact_id": ref.ArtifactID,
			"kind":        ref.Kind,
		}
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
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
		info, err := e.Info()
		if err != nil {
			return "", 0, err
		}
		if !info.Mode().IsRegular() {
			return "", 0, fmt.Errorf("bundle entry %q is not a regular file", e.Name())
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
		// Length-prefix both fields. A NUL separator alone is ambiguous when
		// file contents can contain arbitrary bytes (including NULs and what
		// looks like the next filename).
		_ = binary.Write(h, binary.BigEndian, uint64(len(name)))
		_, _ = h.Write([]byte(name))
		_ = binary.Write(h, binary.BigEndian, uint64(len(data)))
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
	return os.WriteFile(path, data, 0o600)
}
