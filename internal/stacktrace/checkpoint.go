package stacktrace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Checkpoint is one file's saved `stacktrace parse --record` progress
// (the naming ADR §5): the inode and size observed
// the last time the file was read, and the byte offset reading stopped at.
type Checkpoint struct {
	Inode  uint64 `json:"inode"`
	Size   int64  `json:"size"`
	Offset int64  `json:"offset"`
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

// CheckpointPath returns the checkpoint file for absPath:
// $XDG_STATE_HOME/monitor/parse/<sha256(absPath)>.json. Callers should pass
// an already-absolute path (filepath.Abs) so the same physical file always
// hashes to the same checkpoint regardless of the working directory
// `stacktrace parse --record` happened to run from.
func CheckpointPath(absPath string) (string, error) {
	dir, err := checkpointDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(absPath))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
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

// SaveCheckpoint writes absPath's checkpoint, replacing any previous one.
// The write lands in a temp file in the same directory and is renamed into
// place, so a process killed mid-write never leaves a half-written (and
// therefore corrupt) checkpoint for the next --record to trip over.
func SaveCheckpoint(absPath string, cp Checkpoint) error {
	path, err := CheckpointPath(absPath)
	if err != nil {
		return err
	}
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

// StatInode returns f's current inode and size (the two identity signals
// ResolveOffset needs to detect rotation/truncation).
func StatInode(f *os.File) (inode uint64, size int64, err error) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino), info.Size(), nil
	}
	return 0, info.Size(), nil
}

// ResolveOffset decides the byte offset `stacktrace parse --record` should
//
//	seek absPath to before reading, applying the reset rule (naming ADR §5): start over from 0
//
// when fromStart is set, when
// the file's current inode no longer matches the saved checkpoint's
// (rotation), or when the file's current size is smaller than the saved
// offset (truncation) -- both signal a new file at the same path rather
// than steady growth. inode/size are the file's CURRENT stat (from
// StatInode), taken once before reading. Returns the offset to seek to and
// a Checkpoint pre-filled with the current inode/size and that same
// starting offset; the caller advances .Offset as it reads and passes the
// final value to SaveCheckpoint.
func ResolveOffset(absPath string, inode uint64, size int64, fromStart bool) (start int64, cp Checkpoint) {
	cp = Checkpoint{Inode: inode, Size: size}
	if fromStart {
		return 0, cp
	}
	prev, err := LoadCheckpoint(absPath)
	if err != nil || prev.Inode != inode || size < prev.Offset {
		return 0, cp
	}
	cp.Offset = prev.Offset
	return prev.Offset, cp
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
