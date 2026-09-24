package stacktrace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Checkpoint is one file's saved `stacktrace parse --record` progress
// (the naming ADR §5): the device/inode and size
// observed the last time the file was read, and the byte offset reading
// stopped at.
type Checkpoint struct {
	// Dev is the file's device number (syscall.Stat_t.Dev). Combined with
	// Inode (CC-2, the naming ADR §5), it survives a
	// logrotate-style rename: `mv app.log app.log.1` keeps the same
	// dev/inode, so CheckpointKey{Dev, Inode} finds the SAME checkpoint
	// file under the new name/path -- no separate fallback needed for that
	// case; see LoadCheckpointByKey's doc comment for the ONE case that
	// still needs a path-based fallback (a pre-CC-2 checkpoint saved before
	// Dev existed). Zero on a system where syscall.Stat_t is unavailable,
	// or when decoded from a pre-CC-2 checkpoint that never recorded it.
	Dev    uint64 `json:"dev,omitempty"`
	Inode  uint64 `json:"inode"`
	Size   int64  `json:"size"`
	Offset int64  `json:"offset"`
	// MtimeNs is the file's mtime (UnixNano) observed when this checkpoint
	// was saved -- the offset advanced to whatever this run reached, re-
	// stat'd right before saving, not the mtime seen when the run started.
	// Together with the Offset/Size comparison below, this is CC-1's fix
	// for a log truncated-then-rewritten IN PLACE, same inode, to a size
	// that happens to reach or exceed the old checkpoint's offset again
	// (`cmd 2> app.log` restarted against the same redirect target): the
	// pre-CC-1 inode/size-only reset rule resumed from the stale offset
	// and silently skipped the new content, or mid-block. See
	// ResolveOffset's rewrittenInPlace check.
	MtimeNs int64 `json:"mtime_ns,omitempty"`
	// PrefixHash is PrefixHash(f, Offset) at save time: sha256 (hex) of the
	// file's first min(Offset, prefixHashBytes) bytes. CC-1's belt-and-
	// suspenders check alongside MtimeNs: a rewrite whose mtime happens to
	// land in the same second as the old one (coarse filesystem mtime
	// resolution) or that reaches an equal-or-larger size still changes
	// its own leading bytes almost always, and ResolveOffset compares this
	// against a fresh PrefixHash of the CURRENT file's same byte range.
	PrefixHash string `json:"prefix_hash,omitempty"`
	// Generation counts every reset ResolveOffset has detected for this
	// dev/inode (rotation, truncation, an in-place rewrite): bumped by
	// ResolveOffset whenever it resets to offset 0, otherwise carried
	// through unchanged -- including across a --from-start replay, which
	// is a deliberate REPLAY of the same generation, not a new one (CC-1).
	// internal/cli/stacktrace.go folds this into the reprocess DedupeKey
	// seed alongside dev/inode/offset/blockhash, so a genuinely rewritten
	// file's occurrences get fresh dedupe keys instead of silently
	// deduping against a PREVIOUS generation's occurrence recorded at the
	// same byte offsets, while two --from-start replays of the SAME
	// (unchanged) generation still dedupe against each other correctly.
	Generation int `json:"generation,omitempty"`
}

// CheckpointKey identifies a physical file across a rename by its device
// and inode -- see CheckpointPathForKey and LoadCheckpointByKey.
type CheckpointKey struct {
	Dev   uint64
	Inode uint64
}

// checkpointDir returns $XDG_STATE_HOME/monitor/parse (created private,
// mode 0700), falling back to ~/.local/state/monitor/parse when
// XDG_STATE_HOME is unset -- the same convention internal/incidents'
// registryDir uses for $XDG_STATE_HOME/monitor/incidents.
func checkpointDir() (string, error) {
	root := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		root = filepath.Join(home, ".local", "state")
	} else if !filepath.IsAbs(root) {
		return "", fmt.Errorf("XDG_STATE_HOME must be an absolute path: %q", root)
	}
	dir := filepath.Join(root, "monitor", "parse")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create checkpoint directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure checkpoint directory: %w", err)
	}
	return dir, nil
}

// CheckpointPath returns the LEGACY (pre-CC-2) checkpoint file for absPath:
// $XDG_STATE_HOME/monitor/parse/<sha256(absPath)>.json. Callers should pass
// an already-absolute path (filepath.Abs) so the same physical file always
// hashes to the same checkpoint regardless of the working directory
// `stacktrace parse --record` happened to run from.
//
// New code should use CheckpointPathForKey/LoadCheckpointByKey instead: a
// PATH-keyed checkpoint breaks across a logrotate-style rename (the file
// keeps its inode but this hash changes) and across two spellings of the
// same physical file (e.g. /tmp vs macOS's /private/tmp) -- see CC-2 and
// CC-4 in the naming ADR §5. This is kept as the
// fallback LoadCheckpointByKey reads from a pre-CC-2 checkpoint, and its
// own SaveCheckpoint/LoadCheckpoint remain for that migration path.
func CheckpointPath(absPath string) (string, error) {
	dir, err := checkpointDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(absPath))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}

// CheckpointPathForKey returns the checkpoint file for a dev:inode pair:
// $XDG_STATE_HOME/monitor/parse/<dev>-<inode>.json. Stable across a
// logrotate-style rename (CC-2): `mv app.log app.log.1` keeps the same
// device and inode, so the renamed file's next --record run finds the SAME
// checkpoint file under its new name, with no path-based fallback needed.
func CheckpointPathForKey(key CheckpointKey) (string, error) {
	dir, err := checkpointDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%d-%d.json", key.Dev, key.Inode)), nil
}

// LoadCheckpoint reads absPath's saved checkpoint. A missing or corrupt
// checkpoint file returns the zero Checkpoint and no error: the common case
// is a file being parsed for the first time, and a corrupt checkpoint (e.g.
// truncated by a crash mid-write, before SaveCheckpoint's rename-into-place
// was added elsewhere) must never crash --record or block it forever --
// falling back to "no checkpoint" just re-reads from the start, which
// DedupeKey and the store's ObservedAt-gated reopen rule still keep
// idempotent.
func LoadCheckpoint(absPath string) (Checkpoint, error) {
	path, err := CheckpointPath(absPath)
	if err != nil {
		return Checkpoint{}, err
	}
	return readCheckpointFile(path)
}

// readCheckpointFile reads the checkpoint at the exact file path. A missing
// or corrupt file returns the zero Checkpoint and no error -- see
// LoadCheckpoint's doc comment for why that must never block --record.
func readCheckpointFile(path string) (Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Checkpoint{}, nil
		}
		return Checkpoint{}, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return Checkpoint{}, nil
	}
	return cp, nil
}

// LoadCheckpointByKey reads key's checkpoint (CheckpointPathForKey), the
// primary CC-2 lookup: stable across a logrotate-style rename, since a
// rename keeps the file's dev/inode. When no checkpoint has EVER been saved
// under key yet, it falls back to the LEGACY path-keyed checkpoint
// (CheckpointPath(resolved fallbackAbsPath), EvalSymlinks'd so two
// spellings of the same physical file -- e.g. /tmp vs macOS's
// /private/tmp, CC-4 -- still find it) -- the one case dev/inode keying
// alone cannot cover: a checkpoint saved by a pre-CC-2 monitor binary,
// before Dev/CheckpointPathForKey existed, for a file that has not
// rotated since. A missing checkpoint under BOTH lookups returns the zero
// Checkpoint and no error, matching LoadCheckpoint's own contract.
func LoadCheckpointByKey(key CheckpointKey, fallbackAbsPath string) (Checkpoint, error) {
	path, err := CheckpointPathForKey(key)
	if err != nil {
		return Checkpoint{}, err
	}
	cp, err := readCheckpointFile(path)
	if err != nil {
		return Checkpoint{}, err
	}
	if cp != (Checkpoint{}) {
		return cp, nil
	}
	// Try the RAW absPath first: a pre-CC-2 checkpoint was saved from
	// exactly filepath.Abs(opts.file), never symlink-resolved, so an
	// unchanged invocation spelling matches it directly. Only fall back to
	// the EvalSymlinks'd spelling (helps the CC-4 case: the same physical
	// file invoked two different ways) when the raw one misses too.
	if cp, err := LoadCheckpoint(fallbackAbsPath); err != nil {
		return Checkpoint{}, err
	} else if cp != (Checkpoint{}) {
		return cp, nil
	}
	if resolved, err := filepath.EvalSymlinks(fallbackAbsPath); err == nil && resolved != fallbackAbsPath {
		return LoadCheckpoint(resolved)
	}
	return Checkpoint{}, nil
}

// SaveCheckpoint writes absPath's checkpoint, replacing any previous one.
// The write lands in a temp file in the same directory and is renamed into
// place, so a process killed mid-write never leaves a half-written (and
// therefore corrupt) checkpoint for the next --record to trip over.
func SaveCheckpoint(absPath string, cp Checkpoint) error {
	path, err := CheckpointPath(absPath)
	if err != nil {
		return err
	}
	return writeCheckpointFile(path, cp)
}

// writeCheckpointFile is SaveCheckpoint/SaveCheckpointByKey's shared,
// crash-safe write: a temp file in the same directory, renamed into place,
// so a process killed mid-write never leaves a half-written (and therefore
// corrupt) checkpoint for the next --record to trip over.
func writeCheckpointFile(path string, cp Checkpoint) error {
	data, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit checkpoint: %w", err)
	}
	return nil
}

// SaveCheckpointByKey writes key's checkpoint (CheckpointPathForKey),
// replacing any previous one -- the CC-2 counterpart of LoadCheckpointByKey.
// It never also writes the legacy path-keyed location: once a file has been
// read under the new scheme, its checkpoint lives at the dev/inode path for
// good, and a rename keeps finding it there directly.
func SaveCheckpointByKey(key CheckpointKey, cp Checkpoint) error {
	path, err := CheckpointPathForKey(key)
	if err != nil {
		return err
	}
	return writeCheckpointFile(path, cp)
}

// StatInode returns f's current inode and size (two of the identity
// signals ResolveOffset needs). Kept for existing callers that only need
// inode/size; ResolveOffset itself uses the richer statFile (also reads
// device and mtime -- see Checkpoint.Dev/MtimeNs).
func StatInode(f *os.File) (inode uint64, size int64, err error) {
	_, inode, size, _, err = statFile(f)
	return inode, size, err
}

// statFile returns f's current device, inode, size, and mtime (as
// UnixNano) -- the identity/content signals ResolveOffset needs to detect
// rotation (CC-2), truncation, and an in-place rewrite (CC-1). dev/inode
// are both 0 on a platform where syscall.Stat_t is unavailable.
func statFile(f *os.File) (dev, inode uint64, size, mtimeNs int64, err error) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		dev, inode = uint64(st.Dev), uint64(st.Ino)
	}
	return dev, inode, info.Size(), info.ModTime().UnixNano(), nil
}

// prefixHashBytes bounds how many of a file's leading bytes PrefixHash
// reads: large enough to catch a realistic in-place rewrite, small enough
// to read cheaply on every --record run.
const prefixHashBytes = 4096

// PrefixHash returns sha256 (hex) of f's first min(upTo, prefixHashBytes)
// bytes, read via ReadAt so it never disturbs f's own file offset. upTo<=0
// (nothing has been read from f yet, so there is nothing to fingerprint)
// returns "".
func PrefixHash(f *os.File, upTo int64) string {
	if upTo <= 0 {
		return ""
	}
	if upTo > prefixHashBytes {
		upTo = prefixHashBytes
	}
	buf := make([]byte, upTo)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	sum := sha256.Sum256(buf[:n])
	return hex.EncodeToString(sum[:])
}

// ResolveOffset decides the byte offset `stacktrace parse --record` should
// seek f to before reading, applying the reset rule (the naming ADR
// §5): start over from 0 when fromStart is set, or
// when ANY of the following signal a new file rather than steady growth at
// the same dev/inode --
//
//   - rotation: the file's current dev/inode no longer matches the saved
//     checkpoint's (a pre-CC-2 checkpoint with Dev==0 matches by Inode
//     alone -- see LoadCheckpointByKey);
//   - truncation: the file's current size is smaller than the saved
//     offset;
//   - an in-place rewrite (CC-1): the file's mtime has advanced past the
//     checkpoint's saved MtimeNs while its size did NOT grow past the
//     saved Size -- an ordinary append always grows the file, so mtime
//     advancing without growth means the same bytes were rewritten
//     (`cmd 2> app.log` restarted against the same redirect target);
//   - a content mismatch (CC-1's belt-and-suspenders check): PrefixHash of
//     the file's current first Offset bytes no longer matches the saved
//     PrefixHash, catching a rewrite whose mtime/size happen to coincide
//     with the old file's (coarse mtime resolution, or a rewrite that
//     reproduces the exact same size).
//
// f is stat'd and its leading bytes read (via PrefixHash, using ReadAt)
// once, before any sequential reading begins; f's own file offset is left
// untouched. Returns the offset to seek to and a Checkpoint pre-filled with
// the current dev/inode/size/mtime, that same starting offset, and the
// resolved Generation (bumped on a reset, otherwise carried through
// unchanged -- including for fromStart, a deliberate replay of the SAME
// generation, not a new one); the caller advances .Offset (and, before
// saving, .PrefixHash) as it reads and passes the final value to
// SaveCheckpointByKey.
func ResolveOffset(f *os.File, absPath string, fromStart bool) (start int64, cp Checkpoint, err error) {
	dev, inode, size, mtimeNs, err := statFile(f)
	if err != nil {
		return 0, Checkpoint{}, err
	}
	prev, loadErr := LoadCheckpointByKey(CheckpointKey{Dev: dev, Inode: inode}, absPath)
	if loadErr != nil {
		// Never block --record on a checkpoint read failure; treat it the
		// same as LoadCheckpoint's own "no checkpoint" contract.
		prev = Checkpoint{}
	}
	cp = Checkpoint{Dev: dev, Inode: inode, Size: size, MtimeNs: mtimeNs, Generation: prev.Generation}
	if fromStart {
		return 0, cp, nil
	}

	// hadPriorCheckpoint distinguishes "nothing at all was ever recorded
	// for this file" (LoadCheckpointByKey missed under every lookup it
	// tries) from "a checkpoint was found and its identity no longer
	// matches": only the latter is a RESET (and bumps Generation below) --
	// a never-before-seen file starting at offset 0 is not a reset of
	// anything.
	hadPriorCheckpoint := prev.Inode != 0 || prev.Dev != 0 || prev.Offset != 0
	// A legacy (pre-CC-2) checkpoint never recorded Dev; treat it as
	// matching by Inode alone rather than wrongly resetting every migrated
	// checkpoint's very first --record run after upgrading.
	identityChanged := hadPriorCheckpoint && (prev.Inode != inode || (prev.Dev != 0 && prev.Dev != dev))
	truncated := size < prev.Offset
	rewrittenInPlace := !identityChanged && !truncated &&
		prev.MtimeNs != 0 && mtimeNs > prev.MtimeNs && size <= prev.Size
	contentChanged := !identityChanged && !truncated && !rewrittenInPlace &&
		prev.PrefixHash != "" && prev.Offset > 0 &&
		PrefixHash(f, prev.Offset) != prev.PrefixHash

	if identityChanged || truncated || rewrittenInPlace || contentChanged {
		cp.Generation = prev.Generation + 1
		return 0, cp, nil
	}
	cp.Offset = prev.Offset
	return prev.Offset, cp, nil
}

// SettleWindow is how long a file's mtime must predate "now" before
// `stacktrace parse --record` trusts hitting EOF as a genuine end-of-trace
// rather than the moment it caught an active writer mid-block. It mirrors
// the Joiner's own DefaultIdle: a writer that is really done printing an
// exception's lines will typically not touch the file again within one
// idle window either, the same signal the Joiner itself uses to decide a
// live stream has gone quiet. See internal/cli/stacktrace.go's
// runStacktraceRecord, which is the only caller: EOF on an unsettled file
// must NOT force-close (and therefore parse and record) whatever block is
// still open, or a trace caught mid-write gets recorded as a bogus,
// wrongly-typed issue and its checkpoint then skips past the very bytes
// that would have completed it correctly on the next run.
const SettleWindow = DefaultIdle

// HashBlock returns a stable hex-encoded sha256 of a detected block's raw
// text, the "hash(exception block)" component both live (monitor run --)
// and reprocessed (stacktrace parse --record) DedupeKeys are built from
// (the naming ADR §5). Hashing the raw, pre-parse
// text rather than the parsed Exception keeps the key independent of
// anything scrub or ApplyGitRoot later change.
func HashBlock(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
