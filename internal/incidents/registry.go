package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
)

// maxRegistryEntries caps the durable registry. When a new bundle is
// registered and the cap is exceeded, the oldest entries (by CreatedAt)
// are pruned — evidence recovery is best-effort, not an archive.
const maxRegistryEntries = 20

// registrySchemaVersion is written into every entry.json.
const registrySchemaVersion = 1

var (
	registryIDPattern = regexp.MustCompile(`^[a-f0-9]{12}$`)
	treeHashPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	registryMu        sync.Mutex
)

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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return realDir, nil
}

// register moves bundleDir into the registry as <registry>/<id>/bundle,
// writes entry.json, and prunes entries beyond maxRegistryEntries. On any
// error the original bundleDir is left in place so the caller can still
// surface it. A duplicate ID (same tree-hash captured twice) updates the
// existing entry's attempt bookkeeping and drops the new byte-identical
// copy instead of storing it twice.
func register(bundleDir string, e RegistryEntry) (RegistryEntry, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	dir, err := registryDir()
	if err != nil {
		return RegistryEntry{}, err
	}
	if err := validateEntryIdentity(e); err != nil {
		return RegistryEntry{}, err
	}
	entryDir, err := registryEntryDir(dir, e.ID)
	if err != nil {
		return RegistryEntry{}, err
	}
	entryPath := filepath.Join(entryDir, "entry.json")
	if st, statErr := os.Lstat(entryDir); statErr == nil && st.Mode()&os.ModeSymlink != 0 {
		return RegistryEntry{}, fmt.Errorf("registry entry directory %q is a symlink", e.ID)
	}

	if prev, err := readEntry(entryPath); err == nil {
		prev.BundlePath = filepath.Join(entryDir, "bundle")
		existingHash, _, hashErr := computeTreeHash(prev.BundlePath)
		if hashErr != nil || existingHash != e.TreeHash {
			return RegistryEntry{}, fmt.Errorf("existing registry entry %s failed integrity; fresh bundle retained at %s", e.ID, bundleDir)
		}
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
	if err := os.MkdirAll(entryDir, 0o700); err != nil {
		return RegistryEntry{}, err
	}
	if err := os.Chmod(entryDir, 0o700); err != nil {
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
		if !de.IsDir() || !registryIDPattern.MatchString(de.Name()) {
			continue
		}
		e, err := readEntry(filepath.Join(dir, de.Name(), "entry.json"))
		if err != nil {
			continue
		}
		if e.ID != de.Name() {
			continue
		}
		e.BundlePath = filepath.Join(dir, e.ID, "bundle")
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
	root, err := registryDir()
	if err != nil {
		return RegistryEntry{}, err
	}
	p, registered, err := resolveEntryInput(root, idOrPath)
	if err != nil {
		return RegistryEntry{}, err
	}
	if registered {
		e, err := readEntry(filepath.Join(p, "entry.json"))
		if err != nil {
			return RegistryEntry{}, fmt.Errorf("read registry entry %q: %w", idOrPath, err)
		}
		if e.ID != filepath.Base(p) {
			return RegistryEntry{}, fmt.Errorf("registry entry id %q does not match directory %q", e.ID, filepath.Base(p))
		}
		// BundlePath in entry.json is informational only. Derive the trusted
		// path from the validated registry root and ID to prevent tampered
		// metadata from steering reads or deletes elsewhere.
		e.BundlePath = filepath.Join(p, "bundle")
		if err := requirePlainDirectory(e.BundlePath); err != nil {
			return RegistryEntry{}, fmt.Errorf("registry bundle: %w", err)
		}
		e.Registered = true
		return e, nil
	}
	// Bare bundle dir: synthesize an entry from the manifest.
	if err := validateBundle(p); err != nil {
		return RegistryEntry{}, fmt.Errorf("invalid bare incident bundle: %w", err)
	}
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
		Tags:          buildTags(m.Trigger, m.Alert, m.Diagnosis, treeHash, nil, nil),
		TTL:           m.TTL,
		Trigger:       m.Trigger,
		CreatedAt:     time.Now(),
	}, nil
}

func resolveEntryInput(root, idOrPath string) (path string, registered bool, err error) {
	if registryIDPattern.MatchString(idOrPath) {
		p, err := registryEntryDir(root, idOrPath)
		if err != nil {
			return "", false, err
		}
		if err := requirePlainDirectory(p); err != nil {
			return "", false, fmt.Errorf("no registry entry %q: %w", idOrPath, err)
		}
		return p, true, nil
	}
	if !filepath.IsAbs(idOrPath) && filepath.Clean(idOrPath) == idOrPath && !strings.ContainsRune(idOrPath, filepath.Separator) {
		return "", false, fmt.Errorf("invalid registry id %q", idOrPath)
	}
	abs, err := filepath.Abs(idOrPath)
	if err != nil {
		return "", false, err
	}
	if err := requirePlainDirectory(abs); err != nil {
		return "", false, fmt.Errorf("no bundle directory %q: %w", idOrPath, err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false, err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", false, err
	}
	if filepath.Dir(abs) == rootAbs && registryIDPattern.MatchString(filepath.Base(abs)) {
		return abs, true, nil
	}
	if _, err := os.Lstat(filepath.Join(abs, "entry.json")); err == nil {
		return "", false, fmt.Errorf("external entry.json is not trusted; pass a bare bundle directory containing manifest.json")
	}
	return abs, false, nil
}

func registryEntryDir(root, id string) (string, error) {
	if !registryIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid registry id %q", id)
	}
	return filepath.Join(root, id), nil
}

func requirePlainDirectory(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("%s is not a plain directory", path)
	}
	return nil
}

// Resume re-attempts fcheap archival of a registered (or bare retained)
// bundle — the recovery path for `monitor incidents resume-stash`. On
// success the registry entry and local bundle are deleted and the result
// mirrors a successful Capture. On failure the entry's attempt bookkeeping
// is updated (when registry-managed) and the error is returned.
func Resume(ctx context.Context, idOrPath string) (CaptureResult, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	e, err := LoadEntry(idOrPath)
	if err != nil {
		return CaptureResult{}, err
	}
	stage, h, size, err := stageBundle(e.BundlePath)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("read bundle %s: %w", e.ID, err)
	}
	defer os.RemoveAll(stage)
	if h != e.TreeHash {
		return CaptureResult{}, fmt.Errorf("bundle %s failed integrity check: tree-hash %s != recorded %s (refusing to archive tampered/corrupted evidence)", e.ID, h[:12], e.TreeHash[:12])
	}
	if !hasFcheap() {
		err := fmt.Errorf("fcheap not on PATH")
		recordAttempt(e, err)
		return CaptureResult{TreeHash: e.TreeHash, Path: e.BundlePath, SizeBytes: size, Tags: e.Tags, RegistryID: e.ID}, err
	}
	res, err := stashSave(ctx, stage, e.Name, e.Tags, e.TTL)
	if err != nil {
		recordAttempt(e, err)
		return CaptureResult{TreeHash: e.TreeHash, Path: e.BundlePath, SizeBytes: size, Tags: e.Tags, RegistryID: e.ID, Note: "fcheap save failed again; bundle retained: " + err.Error()}, err
	}
	if err := ecosystem.ValidateLocalStashID(res.ID); err != nil {
		err = fmt.Errorf("invalid fcheap save result: %w", err)
		recordAttempt(e, err)
		return CaptureResult{TreeHash: e.TreeHash, Path: e.BundlePath, SizeBytes: size, Tags: e.Tags, RegistryID: e.ID, Note: "fcheap returned an invalid result; bundle retained"}, err
	}
	postHash, _, err := computeTreeHash(stage)
	if err != nil || postHash != h {
		if err == nil {
			err = fmt.Errorf("staged bundle changed while fcheap was saving it")
		}
		recordAttempt(e, err)
		return CaptureResult{TreeHash: e.TreeHash, Path: e.BundlePath, SizeBytes: size, Tags: e.Tags, RegistryID: e.ID, Note: "bundle integrity changed during save; original retained"}, err
	}
	if e.Registered {
		root, rootErr := registryDir()
		if rootErr == nil {
			if entryDir, pathErr := registryEntryDir(root, e.ID); pathErr == nil {
				_ = os.RemoveAll(entryDir)
			}
		}
	}
	out := CaptureResult{
		StashID:    res.ID,
		TreeHash:   e.TreeHash,
		Path:       stashResultPath(res),
		SizeBytes:  stashResultSize(res, size),
		Tags:       e.Tags,
		CreatedAt:  time.Now(),
		RegistryID: e.ID,
	}
	if !e.Registered {
		out.Note = "external source bundle preserved after archival"
	}
	if ref, refErr := artifactRef(ctx, res.ID, ecosystem.ArtifactRefOpts{
		Kind:         bundleKind,
		ProducerTool: "monitor",
		NativeSchema: "urn:monitor.dev:incident:v1",
		NativeID:     e.TreeHash[:16],
		Entrypoint:   "manifest.json",
	}); refErr == nil && ref.Validate() == nil {
		out.ArtifactRef = artifactRefMap(ref)
	}
	return out, nil
}

const (
	maxBundleFiles           = 32
	maxBundleFileBytes int64 = 128 << 20
	maxBundleBytes     int64 = 256 << 20
	maxManifestBytes   int64 = 1 << 20
)

func stageBundle(src string) (dir, treeHash string, size int64, err error) {
	dir, err = os.MkdirTemp("", "monitor-incident-stage-")
	if err != nil {
		return "", "", 0, err
	}
	cleanup := func() {
		_ = os.RemoveAll(dir)
		dir = ""
	}
	if err = copyFlatDir(src, dir); err != nil {
		cleanup()
		return "", "", 0, err
	}
	if err = validateBundle(dir); err != nil {
		cleanup()
		return "", "", 0, err
	}
	treeHash, size, err = computeTreeHash(dir)
	if err != nil {
		cleanup()
		return "", "", 0, err
	}
	return dir, treeHash, size, nil
}

func validateBundle(dir string) error {
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest.json: %w", err)
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > maxManifestBytes {
		return fmt.Errorf("manifest.json must be a regular file no larger than %d bytes", maxManifestBytes)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest.json: %w", err)
	}
	var manifest bundleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse manifest.json: %w", err)
	}
	if manifest.SchemaVersion != bundleSchemaVersion || manifest.Kind != bundleKind {
		return fmt.Errorf("unsupported schema/kind %q/%q", manifest.SchemaVersion, manifest.Kind)
	}
	if len(manifest.Files) > maxBundleFiles {
		return fmt.Errorf("bundle declares %d files; maximum is %d", len(manifest.Files), maxBundleFiles)
	}
	want := make(map[string]struct{}, len(manifest.Files))
	for _, name := range manifest.Files {
		if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
			return fmt.Errorf("invalid manifest file %q", name)
		}
		if _, exists := want[name]; exists {
			return fmt.Errorf("duplicate manifest file %q", name)
		}
		want[name] = struct{}{}
	}
	for _, required := range []string{"manifest.json", "snapshot.json"} {
		if _, ok := want[required]; !ok {
			return fmt.Errorf("manifest missing required file %q", required)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != len(want) {
		return fmt.Errorf("bundle files do not match manifest")
	}
	var totalBytes int64
	for _, entry := range entries {
		if _, ok := want[entry.Name()]; !ok {
			return fmt.Errorf("undeclared bundle file %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("bundle file %q is not regular", entry.Name())
		}
		if info.Size() > maxBundleFileBytes {
			return fmt.Errorf("bundle entry %q exceeds %d bytes", entry.Name(), maxBundleFileBytes)
		}
		totalBytes += info.Size()
		if totalBytes > maxBundleBytes {
			return fmt.Errorf("bundle exceeds %d total bytes", maxBundleBytes)
		}
	}
	if manifest.RawProfile != "" {
		if _, ok := want[manifest.RawProfile]; !ok {
			return fmt.Errorf("raw profile %q is not declared", manifest.RawProfile)
		}
	}
	return nil
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
	root, err := registryDir()
	if err != nil {
		return
	}
	entryDir, err := registryEntryDir(root, e.ID)
	if err != nil {
		return
	}
	_ = writeJSON(filepath.Join(entryDir, "entry.json"), e)
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
		if entryDir, err := registryEntryDir(dir, e.ID); err == nil {
			_ = os.RemoveAll(entryDir)
		}
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
	if err := validateEntryIdentity(e); err != nil {
		return RegistryEntry{}, fmt.Errorf("entry %s: %w", path, err)
	}
	return e, nil
}

func validateEntryIdentity(e RegistryEntry) error {
	if !registryIDPattern.MatchString(e.ID) {
		return fmt.Errorf("invalid id %q", e.ID)
	}
	if !treeHashPattern.MatchString(e.TreeHash) {
		return fmt.Errorf("invalid tree_hash")
	}
	if !strings.HasPrefix(e.TreeHash, e.ID) {
		return fmt.Errorf("id does not match tree_hash prefix")
	}
	return nil
}

// copyFlatDir copies the regular files of src into dst (created 0o700).
// Bundles are flat by construction (writeBundle emits only top-level
// files), so no recursion is needed.
func copyFlatDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	des, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if len(des) > maxBundleFiles {
		return fmt.Errorf("bundle contains %d entries; maximum is %d", len(des), maxBundleFiles)
	}
	var totalBytes int64
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle entry %q is not a regular file", de.Name())
		}
		srcPath := filepath.Join(src, de.Name())
		in, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		openedInfo, statErr := in.Stat()
		pathInfo, lstatErr := os.Lstat(srcPath)
		if statErr != nil || lstatErr != nil || !openedInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
			_ = in.Close()
			return fmt.Errorf("bundle entry %q changed or is not a stable regular file", de.Name())
		}
		if openedInfo.Size() > maxBundleFileBytes {
			_ = in.Close()
			return fmt.Errorf("bundle entry %q exceeds %d bytes", de.Name(), maxBundleFileBytes)
		}
		totalBytes += openedInfo.Size()
		if totalBytes > maxBundleBytes {
			_ = in.Close()
			return fmt.Errorf("bundle exceeds %d total bytes", maxBundleBytes)
		}
		out, err := os.OpenFile(filepath.Join(dst, de.Name()), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return errors.Join(err, in.Close())
		}
		written, copyErr := io.Copy(out, io.LimitReader(in, maxBundleFileBytes+1))
		if copyErr == nil && written > maxBundleFileBytes {
			copyErr = fmt.Errorf("bundle entry %q grew beyond %d bytes", de.Name(), maxBundleFileBytes)
		}
		if err := errors.Join(copyErr, in.Close(), out.Close()); err != nil {
			return err
		}
	}
	return nil
}
