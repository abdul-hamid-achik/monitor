package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// maxRegistryEntries caps the durable registry. When a new bundle is
// registered and the cap is exceeded, the oldest entries (by CreatedAt)
// are pruned — evidence recovery is best-effort, not an archive.
const maxRegistryEntries = 20

// registrySchemaVersion is written into every entry.json.
const registrySchemaVersion = 1

// RegistryEntry is the durable record of an incident bundle whose fcheap
// archival failed. Layout on disk:
//
//	$XDG_STATE_HOME/monitor/incidents/<id>/entry.json  — this struct
//	$XDG_STATE_HOME/monitor/incidents/<id>/bundle/     — the retained bundle
//	                                                     (manifest.json, snapshot.json, profile.json)
//
// falling back to ~/.local/state/monitor/incidents when $XDG_STATE_HOME is
// unset. <id> is the first 12 hex chars of the bundle tree-hash, matching
// the stash-name suffix Capture uses.
type RegistryEntry struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	TreeHash      string    `json:"tree_hash"`
	Name          string    `json:"name"` // fcheap stash name to reuse on resume
	BundlePath    string    `json:"bundle_path"`
	SizeBytes     int64     `json:"size_bytes"`
	Tags          []string  `json:"tags"`
	TTL           string    `json:"ttl,omitempty"`
	Trigger       string    `json:"trigger"`
	CreatedAt     time.Time `json:"created_at"`
	Attempts      int       `json:"attempts"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	LastError     string    `json:"last_error,omitempty"`

	// Registered is true when the entry is registry-managed (loaded from
	// entry.json) as opposed to synthesized from a bare bundle dir. Not
	// serialized.
	Registered bool `json:"-"`
}

// registryDir resolves (and creates) the registry root. Honors
// $XDG_STATE_HOME per the XDG base-dir spec; falls back to
// ~/.local/state/monitor/incidents.
func registryDir() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(root, "monitor", "incidents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// register moves bundleDir into the registry as <registry>/<id>/bundle,
// writes entry.json, and prunes entries beyond maxRegistryEntries. On any
// error the original bundleDir is left in place so the caller can still
// surface it. A duplicate ID (same tree-hash captured twice) updates the
// existing entry's attempt bookkeeping and drops the new byte-identical
// copy instead of storing it twice.
func register(bundleDir string, e RegistryEntry) (RegistryEntry, error) {
	dir, err := registryDir()
	if err != nil {
		return RegistryEntry{}, err
	}
	entryDir := filepath.Join(dir, e.ID)
	entryPath := filepath.Join(entryDir, "entry.json")

	if prev, err := readEntry(entryPath); err == nil {
		prev.Attempts++
		prev.LastAttemptAt = time.Now()
		prev.LastError = e.LastError
		if err := writeJSON(entryPath, prev); err != nil {
			return RegistryEntry{}, err
		}
		_ = os.RemoveAll(bundleDir) // same content already registered
		prev.Registered = true
		return prev, nil
	}

	dest := filepath.Join(entryDir, "bundle")
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		return RegistryEntry{}, err
	}
	if err := os.Rename(bundleDir, dest); err != nil {
		// $TMPDIR and the state dir can sit on different filesystems
		// (EXDEV); fall back to a flat copy — bundles have no subdirs.
		if cerr := copyFlatDir(bundleDir, dest); cerr != nil {
			_ = os.RemoveAll(entryDir)
			return RegistryEntry{}, fmt.Errorf("move bundle into registry: %w", cerr)
		}
		_ = os.RemoveAll(bundleDir)
	}

	e.SchemaVersion = registrySchemaVersion
	e.BundlePath = dest
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	e.Attempts = 1
	e.LastAttemptAt = time.Now()
	if err := writeJSON(entryPath, e); err != nil {
		return RegistryEntry{}, err
	}
	pruneRegistry(dir)
	e.Registered = true
	return e, nil
}

// ListRegistry returns the registered, not-yet-archived incident bundles,
// newest first. A missing registry directory yields an empty list. Entries
// with an unreadable entry.json are skipped (never an error) so one corrupt
// entry can't hide the rest.
func ListRegistry() ([]RegistryEntry, error) {
	dir, err := registryDir()
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RegistryEntry
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		e, err := readEntry(filepath.Join(dir, de.Name(), "entry.json"))
		if err != nil {
			continue
		}
		e.Registered = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// LoadEntry resolves idOrPath to a registry entry. Accepted forms:
//   - a registry ID (the 12-char tree-hash prefix shown by
//     `monitor incidents pending`)
//   - a path to a registry entry dir (contains entry.json)
//   - a path to a bare retained bundle dir (contains manifest.json but no
//     entry.json — e.g. the pre-registry /tmp layout, or a bundle whose
//     registration failed); a synthetic entry is built from manifest.json
//     and a freshly computed tree-hash.
func LoadEntry(idOrPath string) (RegistryEntry, error) {
	p := idOrPath
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		dir, derr := registryDir()
		if derr != nil {
			return RegistryEntry{}, derr
		}
		p = filepath.Join(dir, idOrPath)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return RegistryEntry{}, fmt.Errorf("no registry entry or bundle directory %q", idOrPath)
		}
	}
	if e, err := readEntry(filepath.Join(p, "entry.json")); err == nil {
		if e.BundlePath == "" {
			e.BundlePath = filepath.Join(p, "bundle")
		}
		e.Registered = true
		return e, nil
	}
	// Bare bundle dir: synthesize an entry from the manifest.
	data, err := os.ReadFile(filepath.Join(p, "manifest.json"))
	if err != nil {
		return RegistryEntry{}, fmt.Errorf("%q is neither a registry entry (entry.json) nor an incident bundle (manifest.json)", idOrPath)
	}
	var m bundleManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return RegistryEntry{}, fmt.Errorf("parse manifest.json: %w", err)
	}
	if m.Trigger == "" {
		m.Trigger = "manual"
	}
	treeHash, size, err := computeTreeHash(p)
	if err != nil {
		return RegistryEntry{}, fmt.Errorf("hash bundle: %w", err)
	}
	return RegistryEntry{
		SchemaVersion: registrySchemaVersion,
		ID:            treeHash[:12],
		TreeHash:      treeHash,
		Name:          fmt.Sprintf("monitor-%s-%s", m.Trigger, treeHash[:12]),
		BundlePath:    p,
		SizeBytes:     size,
		Tags:          buildTags(m.Trigger, m.Alert, m.Diagnosis, treeHash),
		TTL:           m.TTL,
		Trigger:       m.Trigger,
		CreatedAt:     time.Now(),
	}, nil
}

// Resume re-attempts fcheap archival of a registered (or bare retained)
// bundle — the recovery path for `monitor incidents resume-stash`. On
// success the registry entry and local bundle are deleted and the result
// mirrors a successful Capture. On failure the entry's attempt bookkeeping
// is updated (when registry-managed) and the error is returned.
func Resume(ctx context.Context, idOrPath string) (CaptureResult, error) {
	e, err := LoadEntry(idOrPath)
	if err != nil {
		return CaptureResult{}, err
	}
	h, size, err := computeTreeHash(e.BundlePath)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("read bundle %s: %w", e.ID, err)
	}
	if h != e.TreeHash {
		return CaptureResult{}, fmt.Errorf("bundle %s failed integrity check: tree-hash %s != recorded %s (refusing to archive tampered/corrupted evidence)", e.ID, h[:12], e.TreeHash[:12])
	}
	if !hasFcheap() {
		err := fmt.Errorf("fcheap not on PATH")
		recordAttempt(e, err)
		return CaptureResult{TreeHash: e.TreeHash, Path: e.BundlePath, SizeBytes: size, Tags: e.Tags, RegistryID: e.ID}, err
	}
	res, err := stashSave(ctx, e.BundlePath, e.Name, e.Tags, e.TTL)
	if err != nil {
		recordAttempt(e, err)
		return CaptureResult{TreeHash: e.TreeHash, Path: e.BundlePath, SizeBytes: size, Tags: e.Tags, RegistryID: e.ID, Note: "fcheap save failed again; bundle retained: " + err.Error()}, err
	}
	if e.Registered {
		_ = os.RemoveAll(filepath.Dir(e.BundlePath)) // <registry>/<id>/
	} else {
		_ = os.RemoveAll(e.BundlePath)
	}
	sz := res.SizeBytes
	if sz == 0 {
		sz = size
	}
	return CaptureResult{
		StashID:    res.ID,
		TreeHash:   e.TreeHash,
		Path:       res.Path,
		SizeBytes:  sz,
		Tags:       e.Tags,
		CreatedAt:  time.Now(),
		RegistryID: e.ID,
	}, nil
}

// recordAttempt bumps attempt bookkeeping on a registry-managed entry.
// Best-effort: bookkeeping failures must never mask the archival error.
func recordAttempt(e RegistryEntry, attemptErr error) {
	if !e.Registered {
		return
	}
	e.Attempts++
	e.LastAttemptAt = time.Now()
	e.LastError = attemptErr.Error()
	_ = writeJSON(filepath.Join(filepath.Dir(e.BundlePath), "entry.json"), e)
}

// pruneRegistry keeps at most maxRegistryEntries entries, oldest (by
// CreatedAt) first to go. Best-effort: errors are ignored — pruning must
// never block a capture.
func pruneRegistry(dir string) {
	entries, err := ListRegistry()
	if err != nil || len(entries) <= maxRegistryEntries {
		return
	}
	for _, e := range entries[maxRegistryEntries:] { // list is newest-first
		_ = os.RemoveAll(filepath.Join(dir, e.ID))
	}
}

// readEntry loads and validates one entry.json.
func readEntry(path string) (RegistryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RegistryEntry{}, err
	}
	var e RegistryEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return RegistryEntry{}, err
	}
	if e.ID == "" || e.TreeHash == "" {
		return RegistryEntry{}, fmt.Errorf("entry %s missing id/tree_hash", path)
	}
	return e, nil
}

// copyFlatDir copies the regular files of src into dst (created 0o755).
// Bundles are flat by construction (writeBundle emits only top-level
// files), so no recursion is needed.
func copyFlatDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	des, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		in, err := os.Open(filepath.Join(src, de.Name()))
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dst, de.Name()), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return errors.Join(err, in.Close())
		}
		_, copyErr := io.Copy(out, in)
		if err := errors.Join(copyErr, in.Close(), out.Close()); err != nil {
			return err
		}
	}
	return nil
}
